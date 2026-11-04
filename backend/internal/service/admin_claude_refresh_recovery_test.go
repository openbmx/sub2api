//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type claudeReactivationRepo struct {
	AccountRepository
	account *Account
}

func (r *claudeReactivationRepo) GetByID(context.Context, int64) (*Account, error) {
	copy := *r.account
	return &copy, nil
}
func (r *claudeReactivationRepo) Update(_ context.Context, a *Account) error {
	r.account = a
	return nil
}

func TestClaudeExplicitCredentialReactivationPreservesQuotaStops(t *testing.T) {
	until := time.Now().Add(time.Hour)
	repo := &claudeReactivationRepo{account: &Account{ID: 946, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Status: StatusError, ErrorMessage: "Token refresh failed (non-retryable): invalid_grant", TempUnschedulableUntil: &until, TempUnschedulableReason: "operator pause", RateLimitResetAt: &until, Credentials: map[string]any{"access_token": "old"}}}
	svc := &adminServiceImpl{accountRepo: repo}
	updated, err := svc.UpdateAccount(context.Background(), 946, &UpdateAccountInput{Credentials: map[string]any{"access_token": "new"}, Status: StatusActive})
	require.NoError(t, err)
	require.Equal(t, StatusActive, updated.Status)
	require.Empty(t, updated.ErrorMessage)
	require.Equal(t, &until, updated.TempUnschedulableUntil)
	require.Equal(t, &until, updated.RateLimitResetAt)
	require.Equal(t, "operator pause", updated.TempUnschedulableReason)
}
