//go:build unit

package service

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func (r *tokenRefreshAccountRepo) claudeStateForMutation(expected *Account) *Account {
	if r.accountsByID == nil {
		r.accountsByID = map[int64]*Account{}
	}
	if r.accountsByID[expected.ID] == nil {
		r.accountsByID[expected.ID] = snapshotOAuthRefreshAccount(expected)
	}
	if r.beforeConditionalState != nil {
		hook := r.beforeConditionalState
		r.beforeConditionalState = nil
		hook()
	}
	return r.accountsByID[expected.ID]
}

func (r *tokenRefreshAccountRepo) SetClaudeOAuthRefreshFailureIfUnchanged(_ context.Context, expected *Account, message string, until *time.Time) (bool, error) {
	current := r.claudeStateForMutation(expected)
	if !current.IsAnthropicOAuthOrSetupToken() || current.Status != StatusActive ||
		current.GetCredential("access_token") != expected.GetCredential("access_token") ||
		current.GetCredential("refresh_token") != expected.GetCredential("refresh_token") ||
		!reflect.DeepEqual(current.ProxyID, expected.ProxyID) ||
		!reflect.DeepEqual(current.TempUnschedulableUntil, expected.TempUnschedulableUntil) ||
		current.TempUnschedulableReason != expected.TempUnschedulableReason {
		return false, nil
	}
	if until == nil {
		r.setErrorCalls++
		r.lastErrorMessage = message
		current.Status, current.ErrorMessage = StatusError, message
	} else {
		r.setTempUnschedCalls++
		r.lastTempUnschedReason = message
		current.TempUnschedulableUntil, current.TempUnschedulableReason = until, message
	}
	return true, nil
}

func (r *tokenRefreshAccountRepo) ClearClaudeOAuthStateIfUnchanged(_ context.Context, expected *Account, clearError, clearTemporary bool) (bool, error) {
	current := r.claudeStateForMutation(expected)
	if !current.IsAnthropicOAuthOrSetupToken() ||
		current.GetCredential("access_token") != expected.GetCredential("access_token") ||
		current.GetCredential("refresh_token") != expected.GetCredential("refresh_token") ||
		clearError && (current.Status != expected.Status || current.ErrorMessage != expected.ErrorMessage) ||
		clearTemporary && (!reflect.DeepEqual(current.TempUnschedulableUntil, expected.TempUnschedulableUntil) || current.TempUnschedulableReason != expected.TempUnschedulableReason) {
		return false, nil
	}
	if clearError {
		current.Status, current.ErrorMessage = StatusActive, ""
	}
	if clearTemporary {
		r.clearTempCalls++
		current.TempUnschedulableUntil, current.TempUnschedulableReason = nil, ""
	}
	return true, nil
}

type claudeFailingRefresh struct {
	tokenRefresherStub
	onRefresh func()
}

func (r *claudeFailingRefresh) Refresh(context.Context, *Account) (map[string]any, error) {
	r.onRefresh()
	return nil, r.err
}

func TestClaudeStaleRefreshFailureCannotDisableReauthorizedAccount(t *testing.T) {
	for _, unified := range []bool{false, true} {
		for _, cause := range []string{"invalid_grant: revoked", "temporary provider timeout"} {
			t.Run(map[bool]string{false: "direct/", true: "unified/"}[unified]+cause, func(t *testing.T) {
				account := &Account{ID: 961, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Credentials: map[string]any{"access_token": "old", "refresh_token": "same-refresh"}}
				repo := &tokenRefreshAccountRepo{snapshotReads: true}
				repo.accountsByID = map[int64]*Account{account.ID: account}
				blocker := &tokenRefreshRuntimeBlocker{}
				invalidator := &tokenCacheInvalidatorStub{}
				svc := &TokenRefreshService{accountRepo: repo, cfg: &config.TokenRefreshConfig{MaxRetries: 1}, cacheInvalidator: invalidator, runtimeBlocker: blocker, refreshPolicy: DefaultBackgroundRefreshPolicy()}
				if unified {
					svc.refreshAPI = NewOAuthRefreshAPI(repo, nil)
				}
				refresher := &claudeFailingRefresh{tokenRefresherStub: tokenRefresherStub{err: errors.New(cause)}, onRefresh: func() {
					fresh := snapshotOAuthRefreshAccount(account)
					fresh.Credentials["access_token"] = "reauthorized"
					repo.accountsByID[account.ID] = fresh
				}}
				err := svc.refreshWithRetry(context.Background(), account, refresher, refresher, time.Hour)
				require.ErrorIs(t, err, errRefreshSkipped)
				require.Zero(t, repo.setErrorCalls)
				require.Zero(t, repo.setTempUnschedCalls)
				require.Zero(t, blocker.blockCalls)
				require.Zero(t, invalidator.calls)
				require.Equal(t, StatusActive, repo.accountsByID[account.ID].Status)
			})
		}
	}
}

type claudeConcurrentRecoveryRepo struct {
	tokenRefreshAccountRepo
}

func (r *claudeConcurrentRecoveryRepo) ClearTempUnschedulable(_ context.Context, id int64) error {
	r.claudeStateForMutation(r.accountsByID[id])
	r.accountsByID[id].TempUnschedulableUntil, r.accountsByID[id].TempUnschedulableReason = nil, ""
	return nil
}

func TestClaudeRecoveryPreservesNewOperatorStop(t *testing.T) {
	until := time.Now().Add(time.Hour)
	account := &Account{ID: 962, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Status: StatusActive, Credentials: map[string]any{"access_token": "new"}, TempUnschedulableUntil: &until, TempUnschedulableReason: "OAuth 401: expired"}
	repo := &claudeConcurrentRecoveryRepo{}
	repo.accountsByID = map[int64]*Account{account.ID: account}
	repo.beforeConditionalState = func() {
		current := snapshotOAuthRefreshAccount(account)
		current.TempUnschedulableReason = "operator pause"
		repo.accountsByID[account.ID] = current
	}
	cache := &tempUnschedCacheStub{}
	recovered, err := (&RateLimitService{accountRepo: repo, tempUnschedCache: cache}).RecoverClaudeOAuthAccount(context.Background(), account.ID)
	require.NoError(t, err)
	require.Equal(t, "operator pause", recovered.TempUnschedulableReason)
	require.Equal(t, &until, repo.accountsByID[account.ID].TempUnschedulableUntil)
	require.Zero(t, cache.deleteCalls)
}

func TestClaudeRefreshFailurePreservesExistingOperatorStop(t *testing.T) {
	until := time.Now().Add(time.Hour)
	account := &Account{ID: 963, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Credentials: map[string]any{"access_token": "old", "refresh_token": "refresh"}, TempUnschedulableUntil: &until, TempUnschedulableReason: "operator pause"}
	repo := &tokenRefreshAccountRepo{}
	repo.accountsByID = map[int64]*Account{account.ID: account}
	svc := &TokenRefreshService{accountRepo: repo, cfg: &config.TokenRefreshConfig{MaxRetries: 1}, refreshPolicy: DefaultBackgroundRefreshPolicy()}
	refresher := &tokenRefresherStub{err: errors.New("temporary provider timeout")}
	require.Error(t, svc.refreshWithRetry(context.Background(), account, refresher, nil, time.Hour))
	require.Zero(t, repo.setTempUnschedCalls)
	require.Equal(t, "operator pause", repo.accountsByID[account.ID].TempUnschedulableReason)
}

func TestClaudePermanentRefreshFailurePreservesSchedulingSetting(t *testing.T) {
	for _, schedulable := range []bool{false, true} {
		account := &Account{ID: 964, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: schedulable, Credentials: map[string]any{"access_token": "old", "refresh_token": "refresh"}}
		repo := &tokenRefreshAccountRepo{}
		repo.accountsByID = map[int64]*Account{account.ID: account}
		svc := &TokenRefreshService{accountRepo: repo, cfg: &config.TokenRefreshConfig{MaxRetries: 1}, refreshPolicy: DefaultBackgroundRefreshPolicy()}
		refresher := &tokenRefresherStub{err: errors.New("invalid_grant: revoked")}
		require.Error(t, svc.refreshWithRetry(context.Background(), account, refresher, nil, time.Hour))
		require.False(t, repo.accountsByID[account.ID].IsSchedulable())
		repo.accountsByID[account.ID].Credentials = map[string]any{"access_token": "reauthorized", "refresh_token": "new-refresh"}
		recovered, err := (&RateLimitService{accountRepo: repo}).RecoverClaudeOAuthAccount(context.Background(), account.ID)
		require.NoError(t, err)
		require.Equal(t, schedulable, recovered.IsSchedulable())
	}
}

type claudeRateLimitStateRepo struct{ tokenRefreshAccountRepo }

func (r *claudeRateLimitStateRepo) SetError(_ context.Context, id int64, message string) error {
	r.accountsByID[id].Status, r.accountsByID[id].ErrorMessage, r.accountsByID[id].Schedulable = StatusError, message, false
	return nil
}

func TestClaudeMissingRefreshToken401CanRecoverAfterReauthorization(t *testing.T) {
	account := &Account{ID: 965, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Credentials: map[string]any{"access_token": "old"}}
	repo := &claudeRateLimitStateRepo{}
	repo.accountsByID = map[int64]*Account{account.ID: account}
	svc := &RateLimitService{accountRepo: repo, cfg: &config.Config{}}
	require.True(t, svc.HandleUpstreamError(context.Background(), account, http.StatusUnauthorized, http.Header{}, []byte(`{"error":{"message":"expired"}}`)))
	require.False(t, repo.accountsByID[account.ID].IsSchedulable())
	repo.accountsByID[account.ID].Credentials = map[string]any{"access_token": "reauthorized", "refresh_token": "new-refresh"}
	recovered, err := svc.RecoverClaudeOAuthAccount(context.Background(), account.ID)
	require.NoError(t, err)
	require.True(t, recovered.IsSchedulable())
}
