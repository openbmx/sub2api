package admin

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// A stolen admin session could otherwise satisfy step-up by itself: reset the
// admin's password (or bind its own identity), log in again, re-enrol TOTP and
// pass the check. Changing an admin's credentials is therefore gated too.
// As in user_handler_role_stepup_test.go, a triggered gate aborts with 401
// because the test router injects no auth subject.

func TestUpdateAdminPasswordRequiresStepUp(t *testing.T) {
	router, _ := setupRoleStepUpRouter(t)

	rec := doJSON(t, router, http.MethodPut, "/api/v1/admin/users/2", map[string]any{"password": "new-pass-123"})
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestUpdateAdminEmailChangeRequiresStepUp(t *testing.T) {
	router, _ := setupRoleStepUpRouter(t)

	rec := doJSON(t, router, http.MethodPut, "/api/v1/admin/users/2", map[string]any{"email": "attacker@example.com"})
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

// The edit form always resends the current email; that alone must not prompt.
func TestUpdateAdminUnchangedEmailSkipsStepUp(t *testing.T) {
	router, _ := setupRoleStepUpRouter(t)

	rec := doJSON(t, router, http.MethodPut, "/api/v1/admin/users/2", map[string]any{"email": "ADMIN@example.com", "notes": "on call"})
	require.Equal(t, http.StatusOK, rec.Code)
}

func TestUpdateRegularUserPasswordSkipsStepUp(t *testing.T) {
	router, _ := setupRoleStepUpRouter(t)

	rec := doJSON(t, router, http.MethodPut, "/api/v1/admin/users/1", map[string]any{"password": "new-pass-123"})
	require.Equal(t, http.StatusOK, rec.Code)
}

func TestBindAuthIdentityToAdminRequiresStepUp(t *testing.T) {
	router, adminSvc := setupRoleStepUpRouter(t)
	h := NewUserHandler(adminSvc, nil, nil, nil, nil, nil, nil)
	router.POST("/api/v1/admin/users/:id/auth-identities", h.BindAuthIdentity)
	payload := map[string]any{"provider_type": "oidc", "provider_key": "https://issuer.example.com", "provider_subject": "attacker"}

	rec := doJSON(t, router, http.MethodPost, "/api/v1/admin/users/2/auth-identities", payload)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Zero(t, adminSvc.boundAuthIdentityFor, "the identity must not be bound when the gate fires")

	rec = doJSON(t, router, http.MethodPost, "/api/v1/admin/users/1/auth-identities", payload)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, int64(1), adminSvc.boundAuthIdentityFor)
}
