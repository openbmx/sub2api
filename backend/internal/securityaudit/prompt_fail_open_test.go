package securityaudit

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Issue #3678 asks for pass-through when the audit cannot run, so audit
// availability never gates traffic. The default stays fail-closed to preserve
// the established posture, so the choice must be explicit and effective.
func TestBlockingFailOpenControlsRoutingOnAuditFailure(t *testing.T) {
	cases := []struct {
		name         string
		failOpen     bool
		promptErr    error
		wantKind     DecisionKind
		wantStatus   int
		wantNextStep bool
		wantCode     string
	}{
		{
			name: "fail closed rejects on unavailable", failOpen: false, promptErr: errors.New("guard down"),
			wantKind: DecisionUnavailable, wantStatus: http.StatusServiceUnavailable, wantNextStep: false,
			wantCode: ErrorCodeUnavailable,
		},
		{
			name: "fail open passes through on unavailable", failOpen: true, promptErr: errors.New("guard down"),
			wantKind: DecisionUnavailable, wantStatus: http.StatusOK, wantNextStep: true,
			wantCode: ErrorCodeUnavailable,
		},
		{
			name: "fail closed rejects on unparseable reply", failOpen: false, promptErr: &GuardError{Code: ErrorCodeInvalidResponse},
			wantKind: DecisionInvalid, wantStatus: http.StatusServiceUnavailable, wantNextStep: false,
			wantCode: ErrorCodeInvalidResponse,
		},
		{
			name: "fail open passes through on unparseable reply", failOpen: true, promptErr: &GuardError{Code: ErrorCodeInvalidResponse},
			wantKind: DecisionInvalid, wantStatus: http.StatusOK, wantNextStep: true,
			wantCode: ErrorCodeInvalidResponse,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prompt := &fakePromptEngine{mode: ModeBlocking, failOpen: tc.failOpen, err: tc.promptErr}
			decision := NewCoordinator(&fakeLegacyEngine{}, prompt).Check(context.Background(), Request{Body: []byte(`{}`)})
			require.Equal(t, tc.wantKind, decision.Kind, "the decision kind must stay truthful for logs and metrics")
			require.Equal(t, tc.wantStatus, decision.HTTPStatus)
			require.Equal(t, tc.wantNextStep, decision.AllowNextStage)
			require.Equal(t, tc.wantCode, decision.ErrorCode)
		})
	}
}

// Fail-open must never weaken a real finding: only the "could not judge" case
// changes, and a legacy block still wins.
func TestBlockingFailOpenDoesNotWeakenRealFindings(t *testing.T) {
	blockPrompt := &fakePromptEngine{mode: ModeBlocking, failOpen: true, decision: &PromptDecision{Kind: DecisionBlock}}
	decision := NewCoordinator(&fakeLegacyEngine{}, blockPrompt).Check(context.Background(), Request{})
	require.Equal(t, DecisionBlock, decision.Kind)
	require.False(t, decision.AllowNextStage)
	require.Equal(t, http.StatusForbidden, decision.HTTPStatus)

	legacyBlock := &fakeLegacyEngine{decision: &LegacyDecision{Blocked: true, StatusCode: http.StatusForbidden, ErrorCode: "content_policy_violation"}}
	failing := &fakePromptEngine{mode: ModeBlocking, failOpen: true, err: errors.New("guard down")}
	decision = NewCoordinator(legacyBlock, failing).Check(context.Background(), Request{})
	require.Equal(t, DecisionBlock, decision.Kind)
	require.False(t, decision.AllowNextStage, "a content-moderation block outranks a fail-open pass-through")
}

func TestBlockingFailOpenRoundTripsThroughConfig(t *testing.T) {
	manager := &ConfigManager{encryptor: prefixEncryptor{}, encryptionKeyConfigured: true}
	request := UpdateConfigRequest{
		ExpectedConfigVersion: 1, Enabled: true, BlockingEnabled: true, BlockingFailOpen: true,
		Strategy: "priority", WorkerCount: 1, QueueCapacity: 10, Scanners: []string{"pii"}, AllGroups: true,
		Endpoints: []UpdateEndpoint{{
			ID: "guard-1", Name: "Guard", Protocol: "openai_compatible", BaseURL: "http://127.0.0.1:8080",
			Model: DefaultGuardModel, TimeoutMS: 1000, InputLimit: 1000, Enabled: true,
		}},
	}
	next, err := manager.buildNextStorage(DefaultStorageConfig(), request, 9)
	require.NoError(t, err)
	require.True(t, next.BlockingFailOpen)
	require.Contains(t, changeSummary(next), `"blocking_fail_open":true`)

	active, err := ActiveFromStorage(next, true, prefixEncryptor{})
	require.NoError(t, err)
	require.True(t, active.BlockingFailOpen)
	require.True(t, PublicFromStorage(next, true, nil).BlockingFailOpen)
}

func TestDefaultConfigStaysFailClosed(t *testing.T) {
	storage, err := ParseStorageConfig("")
	require.NoError(t, err)
	require.False(t, storage.BlockingFailOpen, "existing deployments must keep rejecting on audit failure")
}

// A config that cannot be activated must still honor the administrator's
// fail-open choice, otherwise a degraded reload rejects all traffic.
func TestBlockingFailOpenSurvivesDegradedConfig(t *testing.T) {
	manager := &ConfigManager{clock: fixedClock{}}
	raw, err := json.Marshal(map[string]any{
		"enabled": true, "blocking_enabled": true, "blocking_fail_open": true, "config_version": 4,
	})
	require.NoError(t, err)

	manager.observeExpectedState(string(raw), true)
	require.True(t, manager.BlockingFailOpen(), "intent must be readable without an activated snapshot")

	manager.markConfigUntrusted()
	require.True(t, manager.BlockingFailOpen(), "an untrusted config must not silently drop the choice")
}

func TestUnavailableResultRecordsWhatTheGatewayDid(t *testing.T) {
	blocked := UnavailableResult("timeout", false, 1500*time.Millisecond)
	require.Equal(t, EventUnavailable, blocked.Decision)
	require.Equal(t, ActionBlock, blocked.Action, "fail-closed rejected the request")
	require.Equal(t, "timeout", blocked.ErrorCode)
	require.Equal(t, 1500, blocked.LatencyMS)

	allowed := UnavailableResult(ErrorCodeUnavailable, true, 0)
	require.Equal(t, ActionAllow, allowed.Action, "fail-open passed the request through")
	// Empty collections rather than nil so the event serializes as [] / {}.
	require.NotNil(t, allowed.Categories)
	require.NotNil(t, allowed.ScannerScores)
}

// A blocking audit that cannot complete must leave a row in the events view,
// not just a service log line.
func TestBlockingFailureRecordsAnEvent(t *testing.T) {
	cases := []struct {
		name       string
		failOpen   bool
		wantAction Action
	}{
		{name: "fail closed records a rejection", failOpen: false, wantAction: ActionBlock},
		{name: "fail open records a pass-through", failOpen: true, wantAction: ActionAllow},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &fakeJobRepository{}
			evaluator := newGuardEvaluator(&scriptedScanner{}, repo, NewAtomicMetrics(), 4, 2)
			cfg := guardConfig(ActiveEndpoint{ID: "bad", Enabled: true, TimeoutMS: 1000, InputLimit: 100})
			cfg.BlockingFailOpen = tc.failOpen

			_, err := evaluator.Evaluate(context.Background(), cfg, PromptSnapshot{RequestID: "r", ScanText: "hello", PromptLength: 5})
			require.Error(t, err)
			require.Equal(t, 1, repo.recordBlockingCalls, "the unjudged request must be recorded")
			require.Equal(t, EventUnavailable, repo.recordBlockingResult.Decision)
			require.Equal(t, tc.wantAction, repo.recordBlockingResult.Action)
			require.NotEmpty(t, repo.recordBlockingResult.ErrorCode)
		})
	}
}

func TestBlockingFailureWithNoEnabledEndpointIsRecorded(t *testing.T) {
	repo := &fakeJobRepository{}
	evaluator := newGuardEvaluator(&scriptedScanner{}, repo, NewAtomicMetrics(), 4, 2)
	_, err := evaluator.Evaluate(context.Background(), guardConfig(), PromptSnapshot{RequestID: "r", ScanText: "hello"})
	require.Error(t, err)
	require.Equal(t, 1, repo.recordBlockingCalls)
	require.Equal(t, "no_enabled_endpoint", repo.recordBlockingResult.ErrorCode)
}

// A hung guard often coincides with the client giving up. The failure record
// must survive that, otherwise the outage is invisible exactly when it matters.
func TestBlockingFailureIsRecordedEvenWhenTheCallerContextIsCanceled(t *testing.T) {
	repo := &recordingContextJobRepository{}
	evaluator := newGuardEvaluator(&scriptedScanner{}, repo, NewAtomicMetrics(), 4, 2)
	cfg := guardConfig(ActiveEndpoint{ID: "bad", Enabled: true, TimeoutMS: 1000, InputLimit: 100})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := evaluator.Evaluate(ctx, cfg, PromptSnapshot{RequestID: "r", ScanText: "hello"})
	require.Error(t, err)
	require.Equal(t, 1, repo.calls)
	require.NoError(t, repo.seenCtxErr, "the record write must not inherit the caller's cancellation")
	deadline, ok := repo.seenDeadline()
	require.True(t, ok, "the detached write must still be bounded")
	require.LessOrEqual(t, time.Until(deadline), auditFailureRecordTimeout)
}

// recordingContextJobRepository captures the context the repository was handed
// so the detachment can be asserted directly.
type recordingContextJobRepository struct {
	fakeJobRepository
	calls      int
	seenCtxErr error
	seenCtx    context.Context
}

func (r *recordingContextJobRepository) RecordBlocking(ctx context.Context, _ PromptSnapshot, _ int64, _ *NormalizedResult, _ bool) (*Event, error) {
	r.calls++
	r.seenCtx, r.seenCtxErr = ctx, ctx.Err()
	return nil, nil
}

func (r *recordingContextJobRepository) seenDeadline() (time.Time, bool) {
	if r.seenCtx == nil {
		return time.Time{}, false
	}
	return r.seenCtx.Deadline()
}

// Recording is best-effort: a failing events table must not turn an audit
// failure into a different failure mode for the caller.
func TestBlockingFailureRecordingErrorIsSwallowed(t *testing.T) {
	repo := &fakeJobRepository{recordBlockingErr: errors.New("db down")}
	evaluator := newGuardEvaluator(&scriptedScanner{}, repo, NewAtomicMetrics(), 4, 2)
	cfg := guardConfig(ActiveEndpoint{ID: "bad", Enabled: true, TimeoutMS: 1000, InputLimit: 100})

	_, err := evaluator.Evaluate(context.Background(), cfg, PromptSnapshot{RequestID: "r", ScanText: "hello"})
	var guardErr *GuardError
	require.ErrorAs(t, err, &guardErr)
	require.Equal(t, ErrorCodeUnavailable, guardErr.Code)
}
