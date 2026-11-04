//go:build unit

package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestClaudeStoppedResponsePreservesStatusAndReason(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, route := range []string{"messages", "responses", "chat"} {
		t.Run(route, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			failure := &service.UpstreamFailoverError{StatusCode: http.StatusForbidden, ClientStatusCode: http.StatusForbidden, ClientMessage: "Request explicitly rejected", NextAccountAction: service.NextAccountStop, Reason: service.GatewayFailureReasonUpstreamRetryDisallowed,
				ResponseBody: []byte(`{"type":"error","error":{"type":"permission_error","message":"Request explicitly rejected"}}`), ResponseHeaders: http.Header{"X-Should-Retry": {"false"}, "X-Request-Id": {"upstream-trace"}, "Retry-After": {"60"}, "Content-Encoding": {"gzip"}}}
			h := &GatewayHandler{}
			switch route {
			case "responses":
				h.handleResponsesFailoverExhausted(c, failure, false)
			case "chat":
				h.handleCCFailoverExhausted(c, failure, false)
			default:
				h.handleFailoverExhausted(c, failure, service.PlatformAnthropic, false)
			}
			require.Equal(t, http.StatusForbidden, rec.Code)
			require.Equal(t, "false", rec.Header().Get("x-should-retry"))
			require.Equal(t, "upstream-trace", rec.Header().Get("x-request-id"))
			require.Equal(t, "60", rec.Header().Get("retry-after"))
			require.Empty(t, rec.Header().Get("content-encoding"))
			require.Contains(t, rec.Body.String(), "Request explicitly rejected")
		})
	}
}
