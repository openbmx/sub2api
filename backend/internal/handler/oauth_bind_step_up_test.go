package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// stepUpGrantTotpCacheStub grants step-up only to the (user, session) pairs a
// test lists.
type stepUpGrantTotpCacheStub struct {
	*oauthPendingFlowTotpCacheStub
	grants map[string]bool
}

func (s *stepUpGrantTotpCacheStub) HasStepUpGrant(_ context.Context, userID int64, sessionKey string) (bool, error) {
	return s.grants[fmt.Sprintf("%d|%s", userID, sessionKey)], nil
}

func (s *stepUpGrantTotpCacheStub) grant(userID int64, sessionKey string) {
	s.grants[fmt.Sprintf("%d|%s", userID, sessionKey)] = true
}

func newStepUpBindTestHandler(t *testing.T, stepUpEnabled bool) (*AuthHandler, *dbent.Client, *stepUpGrantTotpCacheStub) {
	t.Helper()
	cache := &stepUpGrantTotpCacheStub{oauthPendingFlowTotpCacheStub: &oauthPendingFlowTotpCacheStub{}, grants: map[string]bool{}}
	handler, client := newOAuthPendingFlowTestHandlerWithDependencies(t, oauthPendingFlowTestHandlerOptions{
		settingValues: map[string]string{service.SettingKeyStepUpEnabled: fmt.Sprintf("%t", stepUpEnabled)},
		totpCache:     cache,
		totpEncryptor: oauthPendingFlowTotpEncryptorStub{},
	})
	t.Cleanup(func() { _ = client.Close() })
	return handler, client, cache
}

// createBindStepUpUser returns a TOTP-enrolled user and an access token for it.
func createBindStepUpUser(t *testing.T, handler *AuthHandler, client *dbent.Client, role string) (*dbent.User, string, *service.JWTClaims) {
	t.Helper()
	suffix := fmt.Sprintf("%s-%d", role, time.Now().UnixNano())
	user, err := client.User.Create().
		SetEmail("bind-step-up-" + suffix + "@example.com").
		SetUsername("bind-step-up-" + suffix).
		SetPasswordHash("hash").
		SetRole(role).
		SetStatus(service.StatusActive).
		SetTotpEnabled(true).
		Save(context.Background())
	require.NoError(t, err)
	token, err := handler.authService.GenerateToken(context.Background(), &service.User{
		ID: user.ID, Email: user.Email, Username: user.Username, PasswordHash: user.PasswordHash,
		Role: user.Role, Status: user.Status, TotpEnabled: true,
	})
	require.NoError(t, err)
	claims, err := handler.authService.ValidateToken(token)
	require.NoError(t, err)
	return user, token, claims
}

// prepareBindTokenAs calls bind-token as an authenticated request, the way
// jwtAuth leaves the context.
func prepareBindTokenAs(handler *AuthHandler, user *dbent.User, token string, claims *service.JWTClaims) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/auth/oauth/bind-token", nil)
	c.Request.Header.Set("Authorization", "Bearer "+token)
	c.Set(string(servermiddleware.ContextKeyUser), servermiddleware.AuthSubject{UserID: user.ID})
	c.Set(servermiddleware.ContextKeySessionID, claims.SessionID)
	handler.PrepareOAuthBindAccessTokenCookie(c)
	return recorder
}

// resolveBindCookieAs is what bind/start does with the bind access-token cookie.
func resolveBindCookieAs(handler *AuthHandler, token string) (*int64, error) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/auth/oauth/linuxdo/bind/start?intent=bind_current_user", nil)
	c.Request.AddCookie(&http.Cookie{Name: oauthBindAccessTokenCookieName, Value: url.QueryEscape(token), Path: oauthBindAccessTokenCookiePath})
	return handler.resolveOAuthBindTargetUserID(c)
}

// A stolen admin session used to bind its own OAuth login to the admin account
// through the self-service flow, getting a 2FA-free login without step-up.
func TestAdminSelfServiceOAuthBindRequiresStepUp(t *testing.T) {
	handler, client, _ := newStepUpBindTestHandler(t, true)
	admin, token, claims := createBindStepUpUser(t, handler, client, service.RoleAdmin)

	recorder := prepareBindTokenAs(handler, admin, token, claims)
	require.Equal(t, http.StatusForbidden, recorder.Code, recorder.Body.String())
	require.Contains(t, recorder.Body.String(), "STEP_UP_REQUIRED")
	require.Nil(t, findCookie(recorder.Result().Cookies(), oauthBindAccessTokenCookieName), "no bind cookie without step-up")

	// Setting the cookie by hand must not get around it: bind/start is public.
	_, err := resolveBindCookieAs(handler, token)
	require.Error(t, err)
}

func TestAdminSelfServiceOAuthBindPassesWithStepUpGrant(t *testing.T) {
	handler, client, cache := newStepUpBindTestHandler(t, true)
	admin, token, claims := createBindStepUpUser(t, handler, client, service.RoleAdmin)
	cache.grant(admin.ID, oauthBindStepUpSessionKey(claims))

	recorder := prepareBindTokenAs(handler, admin, token, claims)
	require.Equal(t, http.StatusNoContent, recorder.Code, recorder.Body.String())
	require.NotNil(t, findCookie(recorder.Result().Cookies(), oauthBindAccessTokenCookieName))

	userID, err := resolveBindCookieAs(handler, token)
	require.NoError(t, err)
	require.Equal(t, admin.ID, *userID)
}

// A grant belongs to one session; another session of the same admin is not
// verified by it.
func TestAdminSelfServiceOAuthBindGrantIsPerSession(t *testing.T) {
	handler, client, cache := newStepUpBindTestHandler(t, true)
	admin, token, _ := createBindStepUpUser(t, handler, client, service.RoleAdmin)
	cache.grant(admin.ID, "some-other-session")

	_, err := resolveBindCookieAs(handler, token)
	require.Error(t, err)
}

func TestRegularUserSelfServiceOAuthBindSkipsStepUp(t *testing.T) {
	handler, client, _ := newStepUpBindTestHandler(t, true)
	user, token, claims := createBindStepUpUser(t, handler, client, service.RoleUser)

	recorder := prepareBindTokenAs(handler, user, token, claims)
	require.Equal(t, http.StatusNoContent, recorder.Code, recorder.Body.String())
	userID, err := resolveBindCookieAs(handler, token)
	require.NoError(t, err)
	require.Equal(t, user.ID, *userID)
}

func TestAdminSelfServiceOAuthBindWithStepUpOffNeedsNoGrant(t *testing.T) {
	handler, client, _ := newStepUpBindTestHandler(t, false)
	admin, token, claims := createBindStepUpUser(t, handler, client, service.RoleAdmin)

	recorder := prepareBindTokenAs(handler, admin, token, claims)
	require.Equal(t, http.StatusNoContent, recorder.Code, recorder.Body.String())
	userID, err := resolveBindCookieAs(handler, token)
	require.NoError(t, err)
	require.Equal(t, admin.ID, *userID)
}

// The grant is stored under the key the middleware derives from the request;
// bind/start derives it from the cookie's token. They must agree.
func TestOAuthBindStepUpSessionKeyMatchesTheMiddleware(t *testing.T) {
	for _, sid := range []string{"a1b2c3d4e5f60718", ""} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Set(servermiddleware.ContextKeySessionID, sid)
		require.Equal(t, servermiddleware.StepUpSessionKey(c, 42),
			oauthBindStepUpSessionKey(&service.JWTClaims{UserID: 42, SessionID: sid}), "sid %q", sid)
	}
}
