//go:build unit

package service

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/oauth"
	"github.com/stretchr/testify/require"
)

func TestClaudeOAuthConfiguredProxyMustResolve(t *testing.T) {
	for _, operation := range []string{"authorize", "exchange", "cookie", "refresh"} {
		for _, missing := range []bool{false, true} {
			t.Run(operation+"/"+map[bool]string{false: "lookup_error", true: "missing"}[missing], func(t *testing.T) {
				calls := 0
				client := &mockClaudeOAuthClient{
					getOrgUUIDFunc: func(context.Context, string, string) (string, error) { calls++; return "org", nil },
					getAuthCodeFunc: func(context.Context, string, string, string, string, string, string) (string, error) {
						calls++
						return "synthetic-code", nil
					},
					exchangeCodeFunc: func(context.Context, string, string, string, string, bool) (*oauth.TokenResponse, error) {
						calls++
						return &oauth.TokenResponse{AccessToken: "synthetic"}, nil
					},
					refreshTokenFunc: func(context.Context, string, string) (*oauth.TokenResponse, error) {
						calls++
						return &oauth.TokenResponse{AccessToken: "synthetic"}, nil
					},
				}
				cause := errors.New("proxy database unavailable: invalid_grant is unrelated")
				svc := NewOAuthService(&mockProxyRepoForOAuth{getByIDFunc: func(context.Context, int64) (*Proxy, error) {
					if missing {
						return nil, nil
					}
					return nil, cause
				}}, client)
				t.Cleanup(svc.Stop)
				id := int64(41)
				var err error
				switch operation {
				case "authorize":
					_, err = svc.GenerateAuthURL(context.Background(), &id)
				case "exchange":
					session, sessionErr := svc.GenerateAuthURL(context.Background(), nil)
					require.NoError(t, sessionErr)
					_, err = svc.ExchangeCode(context.Background(), &ExchangeCodeInput{SessionID: session.SessionID, Code: "synthetic", ProxyID: &id})
				case "cookie":
					_, err = svc.CookieAuth(context.Background(), &CookieAuthInput{SessionKey: "synthetic", ProxyID: &id})
				case "refresh":
					_, err = svc.RefreshAccountToken(context.Background(), &Account{ProxyID: &id, Credentials: map[string]any{"refresh_token": "synthetic"}})
				}
				require.Error(t, err)
				require.Zero(t, calls, "an explicitly bound proxy must not silently fall back")
				if !missing {
					require.ErrorIs(t, err, cause)
				}
			})
		}
	}
}

func TestClaudeRefreshPreservesUnrelatedTemporaryStops(t *testing.T) {
	for _, reason := range []string{"operator investigation", `{"type":"scheduling_threshold","scope":"account"}`, "OAuth 401: expired token", "token refresh retry exhausted: temporary failure"} {
		t.Run(reason, func(t *testing.T) {
			repo := &tokenRefreshAccountRepo{}
			cache := &tempUnschedCacheStub{}
			svc := &TokenRefreshService{accountRepo: repo, tempUnschedCache: cache}
			until := time.Now().Add(time.Hour)
			account := &Account{ID: 941, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Status: StatusActive, TempUnschedulableUntil: &until, TempUnschedulableReason: reason}
			svc.postRefreshActions(context.Background(), account)
			want := 0
			if reason == "OAuth 401: expired token" || reason == "token refresh retry exhausted: temporary failure" {
				want = 1
			}
			require.Equal(t, want, repo.clearTempCalls)
			require.Equal(t, want, cache.deleteCalls)
		})
	}
}

type claudeRecoveryRepo struct {
	tokenRefreshAccountRepo
	clearErrorCalls int
}

func (r *claudeRecoveryRepo) ClearError(context.Context, int64) error {
	r.clearErrorCalls++
	return nil
}

func TestClaudeUsageSuccessDoesNotRepairRefreshCredentials(t *testing.T) {
	repo := &claudeRecoveryRepo{}
	svc := &AccountUsageService{accountRepo: repo}
	account := &Account{ID: 942, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Status: StatusError, ErrorMessage: "Token refresh failed (non-retryable): invalid_grant"}
	svc.tryClearRecoverableAccountError(context.Background(), account)
	require.Zero(t, repo.clearErrorCalls)
	require.Equal(t, StatusError, account.Status)
}

func TestClaudeSuccessfulRefreshRepairsOnlyItsAuthenticationError(t *testing.T) {
	for _, message := range []string{"Token refresh failed (non-retryable): invalid_grant", "OAuth 401 (no refresh_token): expired", "Access forbidden (403): policy denial"} {
		repo := &claudeRecoveryRepo{}
		svc := &TokenRefreshService{accountRepo: repo}
		account := &Account{ID: 945, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Status: StatusError, ErrorMessage: message}
		svc.postRefreshActions(context.Background(), account)
		if message != "Access forbidden (403): policy denial" {
			require.Equal(t, StatusActive, account.Status)
			require.Empty(t, account.ErrorMessage)
		} else {
			require.Zero(t, repo.clearErrorCalls)
			require.Equal(t, StatusError, account.Status)
		}
	}
}

func TestClaudeRefreshPersistenceFailureIsNotReplayed(t *testing.T) {
	repo := &tokenRefreshAccountRepo{updateErr: errors.New("database unavailable")}
	cfg := &config.Config{TokenRefresh: config.TokenRefreshConfig{MaxRetries: 3}}
	svc := NewTokenRefreshService(repo, nil, nil, nil, nil, nil, nil, cfg, nil)
	account := &Account{ID: 943, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "old", "refresh_token": "old-refresh"}}
	refresher := &tokenRefresherStub{credentials: map[string]any{"access_token": "new", "refresh_token": "rotated"}}
	err := svc.refreshWithRetry(context.Background(), account, refresher, refresher, time.Hour)
	require.Error(t, err)
	require.Equal(t, 1, repo.updateCredentialsCalls, "a successful token exchange with failed storage must not be replayed")
	require.Zero(t, repo.setErrorCalls)
}

func TestClaudeRefreshResultSurvivesPersistenceRetryAndCancellation(t *testing.T) {
	calls := 0
	firstCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	svc := NewOAuthService(nil, &mockClaudeOAuthClient{refreshTokenFunc: func(context.Context, string, string) (*oauth.TokenResponse, error) {
		calls++
		cancel()
		return &oauth.TokenResponse{AccessToken: "new", RefreshToken: "rotated", ExpiresIn: 3600}, nil
	}})
	t.Cleanup(svc.Stop)
	account := &Account{ID: 947, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Credentials: map[string]any{"refresh_token": "old"}}
	first, err := svc.RefreshAccountToken(firstCtx, account)
	require.NoError(t, err)
	second, err := svc.RefreshAccountToken(context.Background(), account)
	require.NoError(t, err)
	require.Equal(t, 1, calls)
	require.Equal(t, first.RefreshToken, second.RefreshToken)
}

func TestClaudePersistedRefreshWithoutRotationCanRefreshAgain(t *testing.T) {
	calls := 0
	svc := NewOAuthService(nil, &mockClaudeOAuthClient{refreshTokenFunc: func(context.Context, string, string) (*oauth.TokenResponse, error) {
		calls++
		return &oauth.TokenResponse{AccessToken: "new", ExpiresIn: 3600}, nil
	}})
	t.Cleanup(svc.Stop)
	account := &Account{ID: 948, Credentials: map[string]any{"access_token": "old", "refresh_token": "unchanged"}}
	_, err := svc.RefreshAccountToken(context.Background(), account)
	require.NoError(t, err)
	account.Credentials["access_token"] = "new"
	_, err = svc.RefreshAccountToken(context.Background(), account)
	require.NoError(t, err)
	require.Equal(t, 2, calls)
}

type claudeLateRefresh struct {
	tokenRefresherStub
	cancel context.CancelFunc
}

func (r *claudeLateRefresh) Refresh(context.Context, *Account) (map[string]any, error) {
	r.calls++
	r.cancel()
	return r.credentials, nil
}

func TestClaudeLateSuccessfulRotationPersistsBeforeReturning(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	account := &Account{ID: 950, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Status: StatusActive, Credentials: map[string]any{"access_token": "old", "refresh_token": "old-refresh"}}
	repo := &tokenRefreshAccountRepo{}
	repo.accountsByID = map[int64]*Account{account.ID: account}
	executor := &claudeLateRefresh{tokenRefresherStub: tokenRefresherStub{credentials: map[string]any{"access_token": "new", "refresh_token": "rotated"}}, cancel: cancel}
	result, err := NewOAuthRefreshAPI(repo, nil).RefreshIfNeeded(ctx, account, executor, time.Hour)
	require.NoError(t, err)
	require.True(t, result.Refreshed)
	require.Equal(t, 1, repo.updateCredentialsCalls)
	require.Equal(t, "rotated", repo.accountsByID[account.ID].GetCredential("refresh_token"))
}

func TestClaudeFailedRotationSaveDoesNotUseOldAccessToken(t *testing.T) {
	account := &Account{ID: 951, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Status: StatusActive, Credentials: map[string]any{"access_token": "old", "refresh_token": "old-refresh"}}
	repo := &tokenRefreshAccountRepo{updateErr: errors.New("storage unavailable")}
	repo.accountsByID = map[int64]*Account{account.ID: account}
	executor := &tokenRefresherStub{credentials: map[string]any{"access_token": "new", "refresh_token": "rotated"}}
	provider := NewClaudeTokenProvider(repo, nil, nil)
	provider.SetRefreshAPI(NewOAuthRefreshAPI(repo, nil), executor)
	_, err := provider.GetAccessToken(context.Background(), account)
	require.ErrorIs(t, err, errOAuthRefreshCredentialPersist)
	require.Equal(t, 1, executor.calls)
}

func TestClaudePendingRefreshDoesNotReplaceReauthorizedAccess(t *testing.T) {
	calls := 0
	svc := NewOAuthService(nil, &mockClaudeOAuthClient{refreshTokenFunc: func(context.Context, string, string) (*oauth.TokenResponse, error) {
		calls++
		return &oauth.TokenResponse{AccessToken: "rotated", RefreshToken: "new-refresh", ExpiresIn: 3600}, nil
	}})
	t.Cleanup(svc.Stop)
	account := &Account{ID: 956, Credentials: map[string]any{"access_token": "old", "refresh_token": "same-refresh"}}
	_, err := svc.RefreshAccountToken(context.Background(), account)
	require.NoError(t, err)
	account.Credentials["access_token"] = "reauthorized"
	_, err = svc.RefreshAccountToken(context.Background(), account)
	require.NoError(t, err)
	require.Equal(t, 2, calls)
}

func TestClaudeSetupToken401UsesRefreshRecovery(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	invalidator := &tokenCacheInvalidatorRecorder{}
	svc := &RateLimitService{accountRepo: repo, cfg: &config.Config{}, tokenCacheInvalidator: invalidator}
	account := &Account{ID: 944, Platform: PlatformAnthropic, Type: AccountTypeSetupToken, Credentials: map[string]any{"access_token": "synthetic", "refresh_token": "synthetic-refresh"}}
	require.True(t, svc.HandleUpstreamError(context.Background(), account, http.StatusUnauthorized, http.Header{}, []byte("expired token")))
	require.Zero(t, repo.setErrorCalls)
	require.Equal(t, 1, repo.tempCalls)
	require.Len(t, invalidator.accounts, 1)
}
