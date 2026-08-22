package securityaudit

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"time"
)

const (
	promptAuditAdmissionLockKey int64 = 579147893221901921
	promptAuditConfigLockKey    int64 = 579147893221901922
)

// Bounds for retrying a lost admission lock. The guarded critical section is one
// counting query plus one INSERT, so a contended enqueue only has to outwait the
// transaction ahead of it; six attempts spread over roughly a tenth of a second
// absorb the burst sizes an agent client produces while staying far inside the
// caller's own budget.
const (
	promptAuditAdmissionAttempts   = 6
	promptAuditAdmissionBackoffMin = 2 * time.Millisecond
	promptAuditAdmissionBackoffMax = 32 * time.Millisecond
)

var (
	ErrQueueFull          = errors.New("prompt audit queue full")
	ErrQueueAdmissionBusy = errors.New("prompt audit queue admission busy")
	ErrLeaseLost          = errors.New("prompt audit worker lease lost")
	ErrEventNotFound      = errors.New("prompt audit event not found")
)

type Job struct {
	ID                  int64
	Snapshot            PromptSnapshot
	ExecutionMode       Mode
	ConfigVersion       int64
	Status              string
	Attempts            int
	MaxAttempts         int
	ClaimVersion        int64
	NextAttemptAt       time.Time
	ProcessingStartedAt *time.Time
	ProcessedAt         *time.Time
	LastErrorCode       string
	LastErrorMessage    string
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

type Event struct {
	ID              int64              `json:"id"`
	JobID           int64              `json:"job_id"`
	Snapshot        PromptSnapshot     `json:"snapshot"`
	Decision        EventDecision      `json:"decision"`
	RiskLevel       RiskLevel          `json:"risk_level"`
	Action          Action             `json:"action"`
	Categories      []string           `json:"categories"`
	MatchedScanners []string           `json:"matched_scanners"`
	ScannerScores   map[string]float64 `json:"scanner_scores"`
	ScannerEvidence map[string]string  `json:"scanner_evidence"`
	ScannerBackend  string             `json:"scanner_backend"`
	ScannerVersion  string             `json:"scanner_version"`
	Reason          string             `json:"reason"`
	ErrorCode       string             `json:"error_code"`
	GuardEndpointID string             `json:"guard_endpoint_id"`
	PolicyID        string             `json:"policy_id"`
	PolicyVersion   int                `json:"policy_version"`
	ConfigVersion   int64              `json:"config_version"`
	ChunkTotal      int                `json:"chunk_total"`
	LatencyMS       int                `json:"latency_ms"`
	IssueSummaries  []IssueSummary     `json:"issue_summaries"`
	CreatedAt       time.Time          `json:"created_at"`
}

type JobRepository interface {
	CreateStagingWithCapacity(ctx context.Context, snapshot PromptSnapshot, configVersion int64, maxAttempts, capacity int) (*Job, error)
	PublishQueued(ctx context.Context, jobID int64) error
	MarkStagingFailed(ctx context.Context, jobID int64, code, message string) error
	ClaimNextJob(ctx context.Context, now time.Time) (*Job, bool, error)
	RefreshLease(ctx context.Context, jobID, claimVersion int64, now time.Time) error
	Complete(ctx context.Context, job *Job, result *NormalizedResult, storePassEvents bool) (*Event, error)
	// RecordJobFailure attaches an event to a job that already reached a terminal
	// failed state, so an unjudged request is visible in the events view.
	RecordJobFailure(ctx context.Context, jobID int64, snapshot PromptSnapshot, configVersion int64, result *NormalizedResult) (*Event, error)
	Retry(ctx context.Context, jobID, claimVersion int64, next time.Time, code, message string) error
	Fail(ctx context.Context, jobID, claimVersion int64, code, message string) error
	ReclaimStale(ctx context.Context, stagingBefore, processingBefore time.Time, limit int) (int64, error)
	QueueStats(ctx context.Context) (QueueStats, error)
	RecordBlocking(ctx context.Context, snapshot PromptSnapshot, configVersion int64, result *NormalizedResult, storePassEvents bool) (*Event, error)
}

type PostgreSQLRepository struct {
	db    *sql.DB
	clock Clock
}

func NewPostgreSQLRepository(db *sql.DB) *PostgreSQLRepository {
	return &PostgreSQLRepository{db: db, clock: realClock{}}
}

// CreateStagingWithCapacity admits one async audit job, serialising the capacity
// check with the staging INSERT so concurrent instances cannot each observe
// spare capacity and collectively overshoot queue_capacity.
//
// Losing the admission lock is retried rather than dropped. Enqueue runs off the
// request's critical path with a seconds-long budget, so a bounded wait costs the
// caller nothing, whereas giving up on the first miss discards audit coverage for
// precisely the traffic that causes misses — an agent fanning several requests
// out within the same millisecond. Sustained contention still surfaces as
// ErrQueueAdmissionBusy once the attempts are spent.
//
// The wait happens in Go between transactions rather than inside the database on
// pg_advisory_xact_lock, because enqueue concurrency far exceeds the connection
// pool: parking waiters on a pooled connection would starve the gateway's own
// queries for the duration of a burst.
func (r *PostgreSQLRepository) CreateStagingWithCapacity(ctx context.Context, snapshot PromptSnapshot, configVersion int64, maxAttempts, capacity int) (*Job, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("prompt audit database unavailable")
	}
	for attempt := 0; ; attempt++ {
		job, err := r.createStagingOnce(ctx, snapshot, configVersion, maxAttempts, capacity)
		if !errors.Is(err, ErrQueueAdmissionBusy) || attempt >= promptAuditAdmissionAttempts-1 {
			return job, err
		}
		// A cancelled or expired caller propagates as itself. Reporting it as
		// contention would describe the same lost caller two different ways
		// depending on whether it died during a backoff or inside the next
		// transaction's BeginTx.
		if waitErr := sleepWithContext(ctx, admissionBackoff(attempt)); waitErr != nil {
			return nil, waitErr
		}
	}
}

func (r *PostgreSQLRepository) createStagingOnce(ctx context.Context, snapshot PromptSnapshot, configVersion int64, maxAttempts, capacity int) (*Job, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var locked bool
	if err := tx.QueryRowContext(ctx, `SELECT pg_try_advisory_xact_lock($1)`, promptAuditAdmissionLockKey).Scan(&locked); err != nil {
		return nil, err
	}
	if !locked {
		return nil, ErrQueueAdmissionBusy
	}
	var active int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM prompt_audit_jobs
		WHERE status IN ('staging','queued','processing','retry')`).Scan(&active); err != nil {
		return nil, err
	}
	if capacity <= 0 || active >= capacity {
		return nil, ErrQueueFull
	}
	if maxAttempts <= 0 {
		maxAttempts = 3
	}
	job, err := insertJob(ctx, tx, snapshot.Redacted(), ModeAsync, configVersion, "staging", maxAttempts)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return job, nil
}

// admissionBackoff grows the pause between admission attempts and jitters it.
// The jitter is what makes the retry work: without it, enqueues that collided
// once sleep for identical durations and simply collide again every round.
func admissionBackoff(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	base := promptAuditAdmissionBackoffMax
	if attempt < 16 {
		if shifted := promptAuditAdmissionBackoffMin << attempt; shifted < base {
			base = shifted
		}
	}
	half := base / 2
	return half + time.Duration(rand.Int64N(int64(half)+1))
}

func sleepWithContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (r *PostgreSQLRepository) PublishQueued(ctx context.Context, jobID int64) error {
	result, err := r.db.ExecContext(ctx, `
		UPDATE prompt_audit_jobs SET status='queued', next_attempt_at=NOW(), updated_at=NOW()
		WHERE id=$1 AND status='staging'`, jobID)
	return requireOneRow(result, err, ErrLeaseLost)
}

func (r *PostgreSQLRepository) MarkStagingFailed(ctx context.Context, jobID int64, code, _ string) error {
	code, message := sanitizeStoredError(code)
	result, err := r.db.ExecContext(ctx, `
		UPDATE prompt_audit_jobs
		SET status='failed', processed_at=NOW(), updated_at=NOW(), last_error_code=$2, last_error_message=$3
		WHERE id=$1 AND status='staging'`, jobID, code, message)
	return requireOneRow(result, err, ErrLeaseLost)
}

func (r *PostgreSQLRepository) ClaimNextJob(ctx context.Context, now time.Time) (*Job, bool, error) {
	row := r.db.QueryRowContext(ctx, `
		WITH candidate AS (
			SELECT id FROM prompt_audit_jobs
			WHERE status IN ('queued','retry') AND next_attempt_at <= $1
			ORDER BY next_attempt_at, id
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		UPDATE prompt_audit_jobs AS j
		SET status='processing', attempts=j.attempts+1, claim_version=j.claim_version+1,
			processing_started_at=$1, updated_at=$1
		FROM candidate
		WHERE j.id=candidate.id
		RETURNING `+jobColumns("j"), now.UTC())
	job, err := scanJob(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	return job, err == nil, err
}

func (r *PostgreSQLRepository) RefreshLease(ctx context.Context, jobID, claimVersion int64, now time.Time) error {
	result, err := r.db.ExecContext(ctx, `
		UPDATE prompt_audit_jobs SET processing_started_at=$3, updated_at=$3
		WHERE id=$1 AND status='processing' AND claim_version=$2`, jobID, claimVersion, now.UTC())
	return requireOneRow(result, err, ErrLeaseLost)
}

func (r *PostgreSQLRepository) Complete(ctx context.Context, job *Job, result *NormalizedResult, storePassEvents bool) (*Event, error) {
	if job == nil || result == nil {
		return nil, errors.New("prompt audit completion requires job and result")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	updateResult, err := tx.ExecContext(ctx, `
		UPDATE prompt_audit_jobs SET status='done', processed_at=NOW(), updated_at=NOW(),
			last_error_code='', last_error_message=''
		WHERE id=$1 AND status='processing' AND claim_version=$2`, job.ID, job.ClaimVersion)
	if err := requireOneRow(updateResult, err, ErrLeaseLost); err != nil {
		return nil, err
	}
	var event *Event
	if shouldStorePromptAuditEvent(result.Decision, storePassEvents) {
		event, err = insertEvent(ctx, tx, job.ID, job.Snapshot.Redacted(), job.ConfigVersion, result)
		if err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return event, nil
}

func (r *PostgreSQLRepository) Retry(ctx context.Context, jobID, claimVersion int64, next time.Time, code, _ string) error {
	code, message := sanitizeStoredError(code)
	result, err := r.db.ExecContext(ctx, `
		UPDATE prompt_audit_jobs SET status='retry', next_attempt_at=$3, processing_started_at=NULL,
			updated_at=NOW(), last_error_code=$4, last_error_message=$5
		WHERE id=$1 AND status='processing' AND claim_version=$2`,
		jobID, claimVersion, next.UTC(), code, message)
	return requireOneRow(result, err, ErrLeaseLost)
}

func (r *PostgreSQLRepository) Fail(ctx context.Context, jobID, claimVersion int64, code, _ string) error {
	code, message := sanitizeStoredError(code)
	result, err := r.db.ExecContext(ctx, `
		UPDATE prompt_audit_jobs SET status='failed', processed_at=NOW(), processing_started_at=NULL,
			updated_at=NOW(), last_error_code=$3, last_error_message=$4
		WHERE id=$1 AND status='processing' AND claim_version=$2`,
		jobID, claimVersion, code, message)
	return requireOneRow(result, err, ErrLeaseLost)
}

func (r *PostgreSQLRepository) ReclaimStale(ctx context.Context, stagingBefore, processingBefore time.Time, limit int) (int64, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	result, err := r.db.ExecContext(ctx, `
		WITH stale AS (
			SELECT id FROM prompt_audit_jobs
			WHERE (status='staging' AND updated_at < $1)
			   OR (status='processing' AND processing_started_at < $2)
			ORDER BY updated_at, id FOR UPDATE SKIP LOCKED LIMIT $3
		)
		UPDATE prompt_audit_jobs AS j
		SET status=CASE
			WHEN j.status='staging' THEN 'failed'
			WHEN j.attempts < j.max_attempts THEN 'retry'
			ELSE 'failed' END,
			next_attempt_at=CASE WHEN j.status='processing' AND j.attempts < j.max_attempts THEN NOW() ELSE j.next_attempt_at END,
			processing_started_at=NULL,
			processed_at=CASE WHEN j.status='staging' OR j.attempts >= j.max_attempts THEN NOW() ELSE NULL END,
			last_error_code=CASE WHEN j.status='staging' THEN 'staging_timeout' ELSE 'processing_lease_expired' END,
			last_error_message='', updated_at=NOW()
		FROM stale WHERE j.id=stale.id`, stagingBefore.UTC(), processingBefore.UTC(), limit)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (r *PostgreSQLRepository) QueueStats(ctx context.Context) (QueueStats, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT status, COUNT(*) FROM prompt_audit_jobs GROUP BY status`)
	if err != nil {
		return QueueStats{}, err
	}
	defer func() { _ = rows.Close() }()
	var stats QueueStats
	for rows.Next() {
		var status string
		var count int64
		if err := rows.Scan(&status, &count); err != nil {
			return QueueStats{}, err
		}
		switch status {
		case "staging":
			stats.Staging = count
		case "queued":
			stats.Queued = count
		case "processing":
			stats.Processing = count
		case "retry":
			stats.Retry = count
		case "done":
			stats.Done = count
		case "failed":
			stats.Failed = count
		}
	}
	stats.Active = stats.Staging + stats.Queued + stats.Processing + stats.Retry
	return stats, rows.Err()
}

func (r *PostgreSQLRepository) RecordJobFailure(ctx context.Context, jobID int64, snapshot PromptSnapshot, configVersion int64, result *NormalizedResult) (*Event, error) {
	if result == nil {
		return nil, errors.New("prompt guard result required")
	}
	return insertEvent(ctx, r.db, jobID, snapshot.Redacted(), configVersion, result)
}

func (r *PostgreSQLRepository) RecordBlocking(ctx context.Context, snapshot PromptSnapshot, configVersion int64, result *NormalizedResult, storePassEvents bool) (*Event, error) {
	if result == nil {
		return nil, errors.New("prompt guard result required")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	job, err := insertJob(ctx, tx, snapshot.Redacted(), ModeBlocking, configVersion, "done", 1)
	if err != nil {
		return nil, err
	}
	var event *Event
	if shouldStorePromptAuditEvent(result.Decision, storePassEvents) {
		event, err = insertEvent(ctx, tx, job.ID, snapshot.Redacted(), configVersion, result)
		if err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return event, nil
}

// shouldStorePromptAuditEvent keeps store_pass_events scoped to safe results.
// Risk events are always persisted while prompt auditing itself is enabled.
func shouldStorePromptAuditEvent(decision EventDecision, storePassEvents bool) bool {
	return decision != EventPass || storePassEvents
}

// UnavailableResult builds the event payload for an audit that could not be
// completed. Action reflects what the gateway did with the request, so a
// fail-open pass-through and a fail-closed rejection are distinguishable.
func UnavailableResult(errorCode string, allowed bool, latency time.Duration) *NormalizedResult {
	action := ActionBlock
	if allowed {
		action = ActionAllow
	}
	return &NormalizedResult{
		Decision: EventUnavailable, RiskLevel: RiskLow, Action: action,
		Categories: []string{}, MatchedScanners: []string{},
		ScannerScores: map[string]float64{}, ScannerEvidence: map[string]string{},
		ScannerBackend: "unavailable", ErrorCode: errorCode,
		PolicyID: "priority", PolicyVersion: 1, LatencyMS: int(latency.Milliseconds()),
	}
}

type sqlQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func insertJob(ctx context.Context, queryer sqlQueryer, snapshot PromptSnapshot, mode Mode, configVersion int64, status string, maxAttempts int) (*Job, error) {
	processedExpr := "NULL"
	if status == "done" || status == "failed" {
		processedExpr = "NOW()"
	}
	row := queryer.QueryRowContext(ctx, `
		INSERT INTO prompt_audit_jobs (
			request_id,user_id,username_snapshot,user_email_snapshot,api_key_id,api_key_name_snapshot,
			group_id,group_name,provider,endpoint,protocol,model,prompt_hash,redacted_preview,
			prompt_length,message_count,stage,execution_mode,config_version,status,max_attempts,processed_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,`+processedExpr+`)
		RETURNING `+jobColumns("prompt_audit_jobs"),
		snapshot.RequestID, nullableID(snapshot.UserID), snapshot.UsernameSnapshot, snapshot.UserEmailSnapshot,
		nullableID(snapshot.APIKeyID), snapshot.APIKeyNameSnapshot, snapshot.GroupID, snapshot.GroupName,
		snapshot.Provider, snapshot.Endpoint, snapshot.Protocol, snapshot.Model, snapshot.PromptHash,
		snapshot.RedactedPreview, snapshot.PromptLength, snapshot.MessageCount, normalizeStage(snapshot.Stage),
		string(mode), configVersion, status, maxAttempts)
	return scanJob(row)
}

func insertEvent(ctx context.Context, queryer sqlQueryer, jobID int64, snapshot PromptSnapshot, configVersion int64, result *NormalizedResult) (*Event, error) {
	categories, _ := json.Marshal(result.Categories)
	matched, _ := json.Marshal(result.MatchedScanners)
	scores, _ := json.Marshal(result.ScannerScores)
	evidence := make(map[string]string, len(result.ScannerEvidence))
	for key, value := range result.ScannerEvidence {
		evidence[key] = RedactPreview(value, 160)
	}
	evidenceJSON, _ := json.Marshal(evidence)
	row := queryer.QueryRowContext(ctx, `
		INSERT INTO prompt_audit_events (
			job_id,request_id,user_id,username_snapshot,user_email_snapshot,api_key_id,api_key_name_snapshot,
			group_id,group_name,provider,endpoint,protocol,model,prompt_hash,redacted_preview,stage,
			decision,risk_level,action,categories,matched_scanners,scanner_scores,scanner_evidence,
			scanner_backend,scanner_version,guard_endpoint_id,policy_id,policy_version,config_version,chunk_total,latency_ms,
			reason,error_code,full_prompt
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,
			$20::jsonb,$21::jsonb,$22::jsonb,$23::jsonb,$24,$25,$26,$27,$28,$29,$30,$31,$32,$33,$34)
		RETURNING `+eventDetailColumns("prompt_audit_events"),
		jobID, snapshot.RequestID, nullableID(snapshot.UserID), snapshot.UsernameSnapshot, snapshot.UserEmailSnapshot,
		nullableID(snapshot.APIKeyID), snapshot.APIKeyNameSnapshot, snapshot.GroupID, snapshot.GroupName,
		snapshot.Provider, snapshot.Endpoint, snapshot.Protocol, snapshot.Model, snapshot.PromptHash,
		snapshot.RedactedPreview, normalizeStage(snapshot.Stage), string(result.Decision), string(result.RiskLevel),
		string(result.Action), categories, matched, scores, evidenceJSON, result.ScannerBackend, result.ScannerVersion,
		result.GuardEndpointID, result.PolicyID, result.PolicyVersion, configVersion, result.ChunkTotal, result.LatencyMS,
		RedactPreview(result.Reason, MaxAuditReasonRunes), result.ErrorCode, snapshot.FullPrompt)
	return scanEvent(row, true)
}

type rowScanner interface{ Scan(...any) error }

func scanJob(row rowScanner) (*Job, error) {
	job := &Job{}
	var userID, apiKeyID, groupID sql.NullInt64
	var processingStarted, processed sql.NullTime
	err := row.Scan(
		&job.ID, &job.Snapshot.RequestID, &userID, &job.Snapshot.UsernameSnapshot, &job.Snapshot.UserEmailSnapshot,
		&apiKeyID, &job.Snapshot.APIKeyNameSnapshot, &groupID, &job.Snapshot.GroupName, &job.Snapshot.Provider,
		&job.Snapshot.Endpoint, &job.Snapshot.Protocol, &job.Snapshot.Model, &job.Snapshot.PromptHash,
		&job.Snapshot.RedactedPreview, &job.Snapshot.PromptLength, &job.Snapshot.MessageCount, &job.Snapshot.Stage,
		&job.ExecutionMode, &job.ConfigVersion, &job.Status, &job.Attempts, &job.MaxAttempts, &job.ClaimVersion,
		&job.NextAttemptAt, &processingStarted, &processed, &job.LastErrorCode, &job.LastErrorMessage,
		&job.CreatedAt, &job.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	job.Snapshot.UserID = nullableInt64Value(userID)
	job.Snapshot.APIKeyID = nullableInt64Value(apiKeyID)
	job.Snapshot.GroupID = nullableInt64Ptr(groupID)
	if processingStarted.Valid {
		value := processingStarted.Time
		job.ProcessingStartedAt = &value
	}
	if processed.Valid {
		value := processed.Time
		job.ProcessedAt = &value
	}
	return job, nil
}

func jobColumns(alias string) string {
	return fmt.Sprintf(`%[1]s.id,%[1]s.request_id,%[1]s.user_id,%[1]s.username_snapshot,%[1]s.user_email_snapshot,
		%[1]s.api_key_id,%[1]s.api_key_name_snapshot,%[1]s.group_id,%[1]s.group_name,%[1]s.provider,
		%[1]s.endpoint,%[1]s.protocol,%[1]s.model,%[1]s.prompt_hash,%[1]s.redacted_preview,
		%[1]s.prompt_length,%[1]s.message_count,%[1]s.stage,%[1]s.execution_mode,%[1]s.config_version,%[1]s.status,
		%[1]s.attempts,%[1]s.max_attempts,%[1]s.claim_version,%[1]s.next_attempt_at,
		%[1]s.processing_started_at,%[1]s.processed_at,%[1]s.last_error_code,%[1]s.last_error_message,
		%[1]s.created_at,%[1]s.updated_at`, alias)
}

func normalizeStage(stage string) string {
	stage = strings.TrimSpace(stage)
	if stage == "" {
		return "http"
	}
	return stage
}

func requireOneRow(result sql.Result, err error, missing error) error {
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return missing
	}
	return nil
}

func nullableID(value int64) any {
	if value <= 0 {
		return nil
	}
	return value
}

func nullableInt64Value(value sql.NullInt64) int64 {
	if !value.Valid {
		return 0
	}
	return value.Int64
}

func nullableInt64Ptr(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	result := value.Int64
	return &result
}
