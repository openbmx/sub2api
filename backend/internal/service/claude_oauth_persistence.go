package service

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type ClaudeOAuthRefreshSuccessRepository interface {
	UpdateClaudeOAuthCredentialsIfUnchanged(context.Context, int64, map[string]any, map[string]any) (bool, error)
}

type ClaudeOAuthRefreshFailureRepository interface {
	SetClaudeOAuthRefreshFailureIfUnchanged(context.Context, *Account, string, *time.Time) (bool, error)
}

type ClaudeOAuthRecoveryRepository interface {
	ClearClaudeOAuthStateIfUnchanged(context.Context, *Account, bool, bool) (bool, error)
}

func recoverClaudeOAuthState(ctx context.Context, repo AccountRepository, account *Account) (*Account, bool, bool, error) {
	clearError := account.Status == StatusError && IsClaudeRefreshAuthenticationError(account.ErrorMessage)
	clearTemporary := account.TempUnschedulableUntil != nil && claudeRefreshCanClearTemporaryStop(account.TempUnschedulableReason)
	if !account.IsAnthropicOAuthOrSetupToken() || !clearError && !clearTemporary {
		return account, false, false, nil
	}
	updater, ok := repo.(ClaudeOAuthRecoveryRepository)
	if !ok {
		return nil, false, false, errors.New("claude OAuth conditional state persistence is not configured")
	}
	applied, err := updater.ClearClaudeOAuthStateIfUnchanged(ctx, snapshotOAuthRefreshAccount(account), clearError, clearTemporary)
	if err != nil {
		return nil, false, false, err
	}
	current, err := repo.GetByID(ctx, account.ID)
	if err != nil || current == nil {
		return nil, false, false, fmt.Errorf("read Claude OAuth state after recovery: %v", err)
	}
	return current, applied && clearError, applied && clearTemporary, nil
}

// Persist only authentication fields while the attempted token pair still owns the account.
func persistClaudeOAuthRefresh(ctx context.Context, repo AccountRepository, attempted *Account, credentials map[string]any) (*Account, bool, error) {
	updater, ok := repo.(ClaudeOAuthRefreshSuccessRepository)
	if !ok {
		return nil, false, errors.New("claude OAuth conditional credential persistence is not configured")
	}
	updates := make(map[string]any)
	for _, key := range []string{"access_token", "refresh_token", "token_type", "scope", "expires_in", "expires_at", "_token_version"} {
		if value, exists := credentials[key]; exists {
			updates[key] = value
		}
	}
	applied, err := updater.UpdateClaudeOAuthCredentialsIfUnchanged(ctx, attempted.ID, attempted.Credentials, updates)
	if err != nil {
		return nil, false, fmt.Errorf("%w: %v", errOAuthRefreshCredentialPersist, err)
	}
	current, err := repo.GetByID(ctx, attempted.ID)
	if err != nil || current == nil {
		return nil, false, &oauthRefreshStateUnavailableError{err: fmt.Errorf("read Claude OAuth state after persistence: %v", err)}
	}
	return current, applied, nil
}
