//go:build unit

package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/oauth"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
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

type claudeReauthorizationAdmin struct {
	*stubAdminService
	broadRecoveryCalls int
}

func (s *claudeReauthorizationAdmin) ClearAccountError(context.Context, int64) (*service.Account, error) {
	s.broadRecoveryCalls++
	return s.getAccountResult, nil
}

func TestClaudeReauthorizationDoesNotInvokeBroadStateReset(t *testing.T) {
	gin.SetMode(gin.TestMode)
	until := time.Now().Add(time.Hour)
	base := newStubAdminService()
	base.getAccountResult = &service.Account{ID: 1, Platform: service.PlatformAnthropic, Type: service.AccountTypeOAuth, Status: service.StatusActive, RateLimitResetAt: &until, TempUnschedulableUntil: &until, TempUnschedulableReason: "operator pause", Credentials: map[string]any{"access_token": "old", "refresh_token": "old-refresh"}}
	admin := &claudeReauthorizationAdmin{stubAdminService: base}
	h := NewAccountHandler(admin, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	router := gin.New()
	router.POST("/accounts/:id/apply-oauth-credentials", h.ApplyOAuthCredentials)
	rec := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/accounts/1/apply-oauth-credentials", strings.NewReader(`{"type":"oauth","credentials":{"access_token":"new","refresh_token":"new-refresh"}}`))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, request)
	require.Equal(t, 200, rec.Code)
	require.Zero(t, admin.broadRecoveryCalls)
}
