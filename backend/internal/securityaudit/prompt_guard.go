package securityaudit

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

type GuardEvaluator struct {
	scanner PromptScanner
	repo    JobRepository
	metrics Metrics
	clock   Clock

	global       chan struct{}
	perNodeLimit int
	nodeMu       sync.Mutex
	nodes        map[string]chan struct{}
	blockCache   *blockVerdictCache
}

func NewGuardEvaluator(scanner PromptScanner, repo JobRepository, metrics Metrics) *GuardEvaluator {
	return newGuardEvaluator(scanner, repo, metrics, 64, 16)
}

func newGuardEvaluator(scanner PromptScanner, repo JobRepository, metrics Metrics, globalLimit, perNodeLimit int) *GuardEvaluator {
	if globalLimit < 1 {
		globalLimit = 64
	}
	if perNodeLimit < 1 {
		perNodeLimit = 16
	}
	clock := realClock{}
	return &GuardEvaluator{scanner: scanner, repo: repo, metrics: metrics, clock: clock,
		global: make(chan struct{}, globalLimit), perNodeLimit: perNodeLimit, nodes: map[string]chan struct{}{},
		blockCache: newBlockVerdictCache(DefaultBlockVerdictTTL, maxBlockVerdictEntries, clock)}
}

func (g *GuardEvaluator) Evaluate(ctx context.Context, cfg ActiveConfig, snapshot PromptSnapshot) (*PromptDecision, error) {
	if g == nil || g.scanner == nil {
		if g != nil && g.metrics != nil {
			g.metrics.Observe(DecisionUnavailable, 0)
		}
		logGuardFailure(snapshot, cfg, DecisionUnavailable, ErrorCodeUnavailable, "", 0)
		return nil, &GuardError{Code: ErrorCodeUnavailable}
	}
	start := g.clock.Now()
	baseFields := snapshotLogFields(snapshot)
	baseFields["config_version"] = cfg.ConfigVersion
	endpoints := cfg.EnabledEndpoints()
	if len(endpoints) == 0 {
		if g.metrics != nil {
			g.metrics.Observe(DecisionUnavailable, g.clock.Now().Sub(start))
		}
		logGuardFailure(snapshot, cfg, DecisionUnavailable, ErrorCodeUnavailable, "", g.clock.Now().Sub(start))
		g.recordFailure(ctx, cfg, snapshot, "no_enabled_endpoint", g.clock.Now().Sub(start))
		return nil, &GuardError{Code: ErrorCodeUnavailable}
	}
	// Replay a recent rejection of this exact prompt before spending an audit
	// call on it. This runs ahead of the bulkhead so a retry storm cannot
	// exhaust the concurrency budget and start failing open.
	if cached, ok := g.blockCache.get(cfg.ConfigVersion, snapshot.PromptHash); ok {
		return g.replayCachedBlock(ctx, cfg, snapshot, baseFields, cached, start), nil
	}
	select {
	case g.global <- struct{}{}:
		defer func() { <-g.global }()
	default:
		if g.metrics != nil {
			g.metrics.IncBulkheadFull()
			g.metrics.Observe(DecisionUnavailable, g.clock.Now().Sub(start))
		}
		logGuardFailure(snapshot, cfg, DecisionUnavailable, ErrorCodeUnavailable, "", g.clock.Now().Sub(start))
		g.recordFailure(ctx, cfg, snapshot, "bulkhead_full", g.clock.Now().Sub(start))
		return nil, &GuardError{Code: ErrorCodeUnavailable}
	}
	timeout := time.Duration(endpoints[0].TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = DefaultTimeoutMS * time.Millisecond
	}
	evalCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	inputLimit := minimumInputLimit(endpoints)
	chunks := SplitRunes(snapshot.ScanText, inputLimit)
	if len(chunks) == 0 {
		if g.metrics != nil {
			g.metrics.Observe(DecisionAllow, g.clock.Now().Sub(start))
		}
		return &PromptDecision{Kind: DecisionAllow, AllowNextStage: true}, nil
	}
	LogInfo(EventEvaluationStarted, mergeLogFields(baseFields, map[string]any{"chunk_total": len(chunks), "status": "started"}))
	results := make([]*NormalizedResult, 0, len(chunks))
	for index, chunk := range chunks {
		chunkStarted := g.clock.Now()
		LogInfo(EventChunkStarted, mergeLogFields(baseFields, map[string]any{
			"chunk_index": index + 1, "chunk_total": len(chunks),
			"chunk_chars": len([]rune(chunk)), "input_chars": snapshot.PromptLength, "input_limit": inputLimit,
			"status": "started",
		}))
		result, err := g.scanChunk(evalCtx, cfg, endpoints, chunk)
		if err != nil {
			code := guardErrorCode(err)
			LogWarn(EventChunkFailed, mergeLogFields(baseFields, map[string]any{
				"chunk_index": index + 1, "chunk_total": len(chunks),
				"chunk_chars": len([]rune(chunk)), "input_chars": snapshot.PromptLength, "input_limit": inputLimit,
				"latency_ms": g.clock.Now().Sub(chunkStarted).Milliseconds(), "error_code": code, "status": "failed",
			}))
			kind := DecisionUnavailable
			// A truncated reply is a malformed verdict like any other; only the
			// remedy differs, which the error code already carries.
			if code == ErrorCodeInvalidResponse || code == ErrorCodeOutputTruncated {
				kind = DecisionInvalid
			}
			if g.metrics != nil {
				g.metrics.Observe(kind, g.clock.Now().Sub(start))
				var guardErr *GuardError
				if errors.As(err, &guardErr) && guardErr.Timeout {
					g.metrics.IncTimeout()
				}
			}
			logGuardFailure(snapshot, cfg, kind, code, "", g.clock.Now().Sub(start))
			var timeoutErr *GuardError
			if errors.As(err, &timeoutErr) && timeoutErr.Timeout {
				// Distinguish the timeout case issue #3678 calls out explicitly.
				code = "timeout"
			}
			g.recordFailure(ctx, cfg, snapshot, code, g.clock.Now().Sub(start))
			return nil, err
		}
		result.ChunkTotal = len(chunks)
		results = append(results, result)
		LogInfo(EventChunkCompleted, mergeLogFields(baseFields, map[string]any{
			"chunk_index": index + 1, "chunk_total": len(chunks),
			"chunk_chars": len([]rune(chunk)), "input_chars": snapshot.PromptLength, "input_limit": inputLimit,
			"guard_endpoint_id": result.GuardEndpointID, "action": result.Action,
			"latency_ms": g.clock.Now().Sub(chunkStarted).Milliseconds(), "status": "completed",
		}))
		if result.Action == ActionBlock {
			break
		}
	}
	aggregated, err := AggregateResults(results, g.clock.Now().Sub(start))
	if err != nil {
		if g.metrics != nil {
			g.metrics.Observe(DecisionInvalid, g.clock.Now().Sub(start))
		}
		logGuardFailure(snapshot, cfg, DecisionInvalid, ErrorCodeInvalidResponse, "", g.clock.Now().Sub(start))
		g.recordFailure(ctx, cfg, snapshot, ErrorCodeInvalidResponse, g.clock.Now().Sub(start))
		return nil, &GuardError{Code: ErrorCodeInvalidResponse, Cause: err}
	}
	aggregated.ChunkTotal = len(chunks)
	kind := DecisionAllow
	if aggregated.Action == ActionWarn {
		kind = DecisionFlag
	}
	if aggregated.Action == ActionBlock {
		kind = DecisionBlock
	}
	decision := &PromptDecision{Kind: kind, Result: aggregated, AllowNextStage: kind == DecisionAllow || kind == DecisionFlag}
	if kind == DecisionBlock {
		decision.ErrorCode = ErrorCodeBlocked
		applyBlockResponse(decision, cfg, snapshot)
		// Remember the rejection so the next attempt at the same prompt does
		// not get a fresh roll of a nondeterministic audit model.
		g.blockCache.put(cfg.ConfigVersion, snapshot.PromptHash, aggregated)
	}
	if g.metrics != nil {
		g.metrics.Observe(kind, g.clock.Now().Sub(start))
	}
	LogInfo(EventChunksAggregated, mergeLogFields(baseFields, map[string]any{
		"decision":   kind,
		"risk_level": aggregated.RiskLevel, "action": aggregated.Action, "chunk_total": aggregated.ChunkTotal,
		"latency_ms": aggregated.LatencyMS, "guard_endpoint_id": aggregated.GuardEndpointID, "stage": snapshot.Stage,
		"status": "completed",
	}))
	if g.repo != nil {
		if _, recordErr := g.repo.RecordBlocking(ctx, snapshot.Redacted(), cfg.ConfigVersion, aggregated, cfg.StorePassEvents); recordErr != nil {
			if g.metrics != nil {
				g.metrics.IncRecordFailed()
			}
			LogWarn(EventResultRecordFailed, mergeLogFields(baseFields, map[string]any{
				"decision": kind, "error_code": "result_record_failed", "stage": snapshot.Stage,
				"status": "failed",
			}))
		}
	}
	if kind == DecisionBlock {
		LogWarn(EventGuardBlocked, mergeLogFields(baseFields, map[string]any{
			"guard_endpoint_id": aggregated.GuardEndpointID,
			"decision":          kind, "risk_level": aggregated.RiskLevel, "action": aggregated.Action, "chunk_total": aggregated.ChunkTotal,
			"latency_ms": aggregated.LatencyMS, "status": "blocked", "error_code": ErrorCodeBlocked,
			"stage": snapshot.Stage, "upstream_dispatched": false, "billing_preconsumed": false,
		}))
	} else {
		LogInfo(EventGuardAllowed, mergeLogFields(baseFields, map[string]any{
			"decision": kind, "risk_level": aggregated.RiskLevel, "action": aggregated.Action,
			"guard_endpoint_id": aggregated.GuardEndpointID, "chunk_total": aggregated.ChunkTotal,
			"latency_ms": aggregated.LatencyMS, "stage": snapshot.Stage, "status": "allowed",
		}))
	}
	return decision, nil
}

// replayCachedBlock serves a previously cached rejection without calling the
// audit model. The event is still recorded and logged, so an operator can see
// that a client retried a rejected prompt and how often, rather than the
// retries silently disappearing.
func (g *GuardEvaluator) replayCachedBlock(
	ctx context.Context, cfg ActiveConfig, snapshot PromptSnapshot,
	baseFields map[string]any, cached NormalizedResult, start time.Time,
) *PromptDecision {
	elapsed := g.clock.Now().Sub(start)
	cached.LatencyMS = int(elapsed.Milliseconds())
	decision := &PromptDecision{Kind: DecisionBlock, Result: &cached, ErrorCode: ErrorCodeBlocked, CachedBlock: true}
	applyBlockResponse(decision, cfg, snapshot)
	if g.metrics != nil {
		g.metrics.Observe(DecisionBlock, elapsed)
	}
	if g.repo != nil {
		if _, err := g.repo.RecordBlocking(ctx, snapshot.Redacted(), cfg.ConfigVersion, &cached, cfg.StorePassEvents); err != nil {
			if g.metrics != nil {
				g.metrics.IncRecordFailed()
			}
			LogWarn(EventResultRecordFailed, mergeLogFields(baseFields, map[string]any{
				"decision": DecisionBlock, "error_code": "result_record_failed",
				"stage": snapshot.Stage, "status": "failed",
			}))
		}
	}
	LogWarn(EventGuardBlocked, mergeLogFields(baseFields, map[string]any{
		"guard_endpoint_id": cached.GuardEndpointID, "decision": DecisionBlock,
		"risk_level": cached.RiskLevel, "action": cached.Action, "chunk_total": cached.ChunkTotal,
		"latency_ms": cached.LatencyMS, "status": "blocked", "error_code": ErrorCodeBlocked,
		"stage": snapshot.Stage, "upstream_dispatched": false, "billing_preconsumed": false,
		"cached_block": true,
	}))
	return decision
}

// applyBlockResponse stamps the administrator-configured status and message
// onto a block. The message names the matched risk categories and the request
// ID but never the audit model's own reasoning: the reason text explains which
// wording tripped the filter, which is precisely the feedback an attacker
// needs to iterate a prompt past it. The request ID lets an operator pull the
// full record, reason included, from the audit event log.
func applyBlockResponse(decision *PromptDecision, cfg ActiveConfig, snapshot PromptSnapshot) {
	if decision == nil {
		return
	}
	status, message := cfg.BlockResponse()
	decision.HTTPStatus = status
	details := make([]string, 0, 2)
	if decision.Result != nil && len(decision.Result.Categories) > 0 {
		details = append(details, "风险类别: "+strings.Join(decision.Result.Categories, ", "))
	}
	if id := strings.TrimSpace(snapshot.RequestID); id != "" {
		details = append(details, "请求 ID: "+id)
	}
	if len(details) > 0 {
		message += "（" + strings.Join(details, "；") + "）"
	}
	decision.ClientMessage = message
}

func logGuardFailure(snapshot PromptSnapshot, cfg ActiveConfig, kind DecisionKind, code, guardEndpointID string, latency time.Duration) {
	fields := snapshotLogFields(snapshot)
	fields["config_version"] = cfg.ConfigVersion
	LogWarn(EventGuardFailed, mergeLogFields(fields, map[string]any{
		"decision": kind, "guard_endpoint_id": guardEndpointID, "latency_ms": latency.Milliseconds(),
		"status": "failed", "error_code": code, "fail_open": cfg.BlockingFailOpen,
		"upstream_dispatched": cfg.BlockingFailOpen, "billing_preconsumed": false,
	}))
}

// auditFailureRecordTimeout bounds the detached write below so a slow database
// cannot hold a request open after the audit already failed.
const auditFailureRecordTimeout = 3 * time.Second

// recordFailure persists an audit failure as an event so the admin view shows
// requests the guard could not judge, and whether each was passed through or
// rejected. A log line alone left rejected traffic invisible in the UI.
func (g *GuardEvaluator) recordFailure(ctx context.Context, cfg ActiveConfig, snapshot PromptSnapshot, code string, latency time.Duration) {
	if g == nil || g.repo == nil {
		return
	}
	// Detached from the caller on purpose: a hung guard often coincides with the
	// client giving up, and a cancelled request context would drop exactly the
	// record that explains why the request failed.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), auditFailureRecordTimeout)
	defer cancel()
	result := UnavailableResult(code, cfg.BlockingFailOpen, latency)
	// Failure events are always stored: StorePassEvents governs benign traffic,
	// and an unjudged request is never benign.
	if _, err := g.repo.RecordBlocking(ctx, snapshot.Redacted(), cfg.ConfigVersion, result, true); err != nil {
		if g.metrics != nil {
			g.metrics.IncRecordFailed()
		}
		LogWarn(EventResultRecordFailed, mergeLogFields(snapshotLogFields(snapshot), map[string]any{
			"decision": DecisionUnavailable, "error_code": "result_record_failed", "status": "failed",
		}))
	}
}

func (g *GuardEvaluator) scanChunk(ctx context.Context, cfg ActiveConfig, endpoints []ActiveEndpoint, chunk string) (*NormalizedResult, error) {
	var lastErr error
	for index, endpoint := range endpoints {
		semaphore := g.nodeSemaphore(endpoint.ID)
		select {
		case semaphore <- struct{}{}:
		case <-ctx.Done():
			return nil, &GuardError{Code: ErrorCodeUnavailable, Retryable: true, Timeout: errors.Is(ctx.Err(), context.DeadlineExceeded), Cause: ctx.Err()}
		default:
			if g.metrics != nil {
				g.metrics.IncBulkheadFull()
			}
			lastErr = &GuardError{Code: ErrorCodeUnavailable, Retryable: true}
			if index < len(endpoints)-1 && g.metrics != nil {
				g.metrics.IncFailover()
			}
			continue
		}
		result, err := callPromptScanner(ctx, g.scanner, endpoint, chunk, cfg.Scanners)
		<-semaphore
		if err == nil && result != nil {
			return result, nil
		}
		if err == nil {
			err = &GuardError{Code: ErrorCodeInvalidResponse, Retryable: false}
		}
		lastErr = err
		var guardErr *GuardError
		if !errors.As(err, &guardErr) || !guardErr.Retryable {
			return nil, err
		}
		if index < len(endpoints)-1 && g.metrics != nil {
			g.metrics.IncFailover()
		}
	}
	if lastErr == nil {
		lastErr = &GuardError{Code: ErrorCodeUnavailable}
	}
	return nil, lastErr
}

func callPromptScanner(ctx context.Context, scanner PromptScanner, endpoint ActiveEndpoint, chunk string, scanners []string) (result *NormalizedResult, err error) {
	defer func() {
		if recover() != nil {
			result = nil
			err = &GuardError{Code: ErrorCodeUnavailable, Retryable: false}
		}
	}()
	return scanner.Scan(ctx, endpoint, chunk, scanners)
}

func (g *GuardEvaluator) nodeSemaphore(id string) chan struct{} {
	g.nodeMu.Lock()
	defer g.nodeMu.Unlock()
	semaphore := g.nodes[id]
	if semaphore == nil {
		semaphore = make(chan struct{}, g.perNodeLimit)
		g.nodes[id] = semaphore
	}
	return semaphore
}

func minimumInputLimit(endpoints []ActiveEndpoint) int {
	limit := DefaultInputLimit
	for index, endpoint := range endpoints {
		value := endpoint.InputLimit
		if value <= 0 {
			value = DefaultInputLimit
		}
		if index == 0 || value < limit {
			limit = value
		}
	}
	return limit
}

func guardErrorCode(err error) string {
	var guardErr *GuardError
	if errors.As(err, &guardErr) && guardErr.Code != "" {
		return guardErr.Code
	}
	return ErrorCodeUnavailable
}

func pointerLogID(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}
