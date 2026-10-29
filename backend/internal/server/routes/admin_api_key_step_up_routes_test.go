package routes

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	adminhandler "github.com/Wei-Shaw/sub2api/internal/handler/admin"
	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// The admin API key is a full admin credential that outlives the session and
// password changes, so minting or removing it goes through step-up. Without
// the gate a stolen session could mint one and keep admin access indefinitely.
func TestAdminAPIKeyChangesRequireStepUp(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handlers := &handler.Handlers{Admin: &handler.AdminHandlers{Setting: &adminhandler.SettingHandler{}}}
	adminAuth := servermiddleware.AdminAuthMiddleware(func(c *gin.Context) { c.Next() })
	auditLog := servermiddleware.AuditLogMiddleware(func(c *gin.Context) { c.Next() })
	stepUp := servermiddleware.StepUpAuthMiddleware(func(c *gin.Context) {
		servermiddleware.AbortWithError(c, http.StatusForbidden, "STEP_UP_REQUIRED", "step-up required")
	})
	RegisterAdminRoutes(router.Group("/api/v1"), handlers, adminAuth, auditLog, stepUp, nil, nil)

	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/admin/settings/admin-api-key/regenerate"},
		{http.MethodDelete, "/api/v1/admin/settings/admin-api-key"},
	} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(tc.method, tc.path, nil))
		require.Equal(t, http.StatusForbidden, recorder.Code, "%s %s", tc.method, tc.path)
		require.Contains(t, recorder.Body.String(), "STEP_UP_REQUIRED", "%s %s", tc.method, tc.path)
	}
}
