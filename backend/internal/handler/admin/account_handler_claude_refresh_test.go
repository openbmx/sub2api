//go:build unit

package admin

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/oauth"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type claudeRefreshClient struct{ service.ClaudeOAuthClient }

func (claudeRefreshClient) RefreshToken(context.Context, string, string) (*oauth.TokenResponse, error) {
	return &oauth.TokenResponse{AccessToken: "new", RefreshToken: "rotated", ExpiresIn: 3600}, nil
}

type claudeRefreshAdmin struct {
	*stubAdminService
	input *service.UpdateAccountInput
}

func (s *claudeRefreshAdmin) UpdateAccount(_ context.Context, id int64, input *service.UpdateAccountInput) (*service.Account, error) {
	s.input = input
	return &service.Account{ID: id, Platform: service.PlatformAnthropic, Type: service.AccountTypeOAuth, Status: input.Status, Credentials: input.Credentials}, nil
}

func TestClaudeVerifiedManualRefreshReactivatesItsAuthenticationError(t *testing.T) {
	for _, message := range []string{"Token refresh failed (non-retryable): invalid_grant", "Access forbidden (403): policy denial"} {
		admin := &claudeRefreshAdmin{stubAdminService: &stubAdminService{}}
		oauthService := service.NewOAuthService(nil, claudeRefreshClient{})
		t.Cleanup(oauthService.Stop)
		h := &AccountHandler{adminService: admin, oauthService: oauthService}
		account := &service.Account{ID: 946, Platform: service.PlatformAnthropic, Type: service.AccountTypeOAuth, Status: service.StatusError, ErrorMessage: message, Credentials: map[string]any{"refresh_token": "old"}}
		_, _, err := h.refreshSingleAccount(context.Background(), account)
		require.NoError(t, err)
		if message == "Token refresh failed (non-retryable): invalid_grant" {
			require.Equal(t, service.StatusActive, admin.input.Status)
		} else {
			require.Empty(t, admin.input.Status)
		}
	}
}
