//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestClaudeRefreshCannotOverwriteReauthorizationOrModelEdits(t *testing.T) {
	for _, reauthorized := range []bool{false, true} {
		account := &Account{ID: 953, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Status: StatusActive, Credentials: map[string]any{"access_token": "old", "refresh_token": "old-refresh", "model_mapping": map[string]any{"alias": "before"}}}
		repo := &refreshAPIAccountRepo{account: account}
		executor := &refreshAPIExecutorStub{needsRefresh: true, credentials: map[string]any{"access_token": "rotated", "refresh_token": "rotated-refresh", "model_mapping": map[string]any{"alias": "before"}}, onRefresh: func() {
			copy := snapshotOAuthRefreshAccount(account)
			copy.Credentials["model_mapping"] = map[string]any{"alias": "edited"}
			if reauthorized {
				copy.Credentials["access_token"] = "reauthorized"
				copy.Credentials["refresh_token"] = "reauthorized-refresh"
			}
			repo.account = copy
		}}
		result, err := NewOAuthRefreshAPI(repo, nil).RefreshIfNeeded(context.Background(), account, executor, time.Hour)
		require.NoError(t, err)
		require.Equal(t, map[string]any{"alias": "edited"}, repo.account.Credentials["model_mapping"])
		if reauthorized {
			require.False(t, result.Refreshed)
			require.Equal(t, "reauthorized", repo.account.GetCredential("access_token"))
		} else {
			require.True(t, result.Refreshed)
			require.Equal(t, "rotated", repo.account.GetCredential("access_token"))
		}
	}
}

func TestClaudeAdminRefreshUsesTheSameCredentialCondition(t *testing.T) {
	account := &Account{ID: 954, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Status: StatusActive, Credentials: map[string]any{"access_token": "reauthorized", "refresh_token": "new-refresh"}}
	repo := &refreshAPIAccountRepo{account: account}
	svc := &adminServiceImpl{accountRepo: repo}
	updated, err := svc.UpdateAccount(context.Background(), account.ID, &UpdateAccountInput{OAuthRefreshExpected: map[string]any{"access_token": "old", "refresh_token": "old-refresh"}, Credentials: map[string]any{"access_token": "stale-rotation", "refresh_token": "stale-refresh"}})
	require.NoError(t, err)
	require.Equal(t, "reauthorized", updated.GetCredential("access_token"))
	require.Zero(t, repo.updateCredentialsCalls)
}

func TestClaudeAdminRefreshDoesNotOverwriteChangedAccountType(t *testing.T) {
	account := &Account{ID: 960, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Status: StatusActive, Credentials: map[string]any{"api_key": "new-key"}}
	repo := &refreshAPIAccountRepo{account: account}
	updated, err := (&adminServiceImpl{accountRepo: repo}).UpdateAccount(context.Background(), account.ID, &UpdateAccountInput{OAuthRefreshExpected: map[string]any{"access_token": "old", "refresh_token": "old-refresh"}, Credentials: map[string]any{"access_token": "stale", "refresh_token": "stale-refresh"}})
	require.NoError(t, err)
	require.Equal(t, "new-key", updated.GetCredential("api_key"))
	require.Empty(t, updated.GetCredential("access_token"))
	require.Zero(t, repo.updateCalls)
}

func TestClaudeCredentialRecoveryPreservesIndependentStops(t *testing.T) {
	for _, reason := range []string{"operator pause", "OAuth 401: expired"} {
		until := time.Now().Add(time.Hour)
		account := &Account{ID: 955, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Status: StatusError, ErrorMessage: "Authentication failed (401): expired", RateLimitResetAt: &until, TempUnschedulableUntil: &until, TempUnschedulableReason: reason}
		repo := &tokenRefreshAccountRepo{}
		repo.accountsByID = map[int64]*Account{account.ID: account}
		cache := &tempUnschedCacheStub{}
		svc := &RateLimitService{accountRepo: repo, tempUnschedCache: cache}
		updated, err := svc.RecoverClaudeOAuthAccount(context.Background(), account.ID)
		require.NoError(t, err)
		require.Equal(t, StatusActive, updated.Status)
		require.Equal(t, &until, updated.RateLimitResetAt)
		if reason == "operator pause" {
			require.Equal(t, &until, updated.TempUnschedulableUntil)
			require.Zero(t, cache.deleteCalls)
		} else {
			require.Nil(t, updated.TempUnschedulableUntil)
			require.Equal(t, 1, cache.deleteCalls)
		}
	}
}
