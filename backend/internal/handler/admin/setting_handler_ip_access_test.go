//go:build unit

package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// The blacklist and IPv6 block are only as good as the client IP they match
// against. In the forwarded-header compatibility mode any client can choose that
// IP, so the risk-control page is told and can say so.
func TestIPAccessControlResponseReportsWhetherClientIPIsSpoofable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, trustForwarded := range []bool{true, false} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/risk-control/ip-access-control", nil)
		ip.SetForwardedIPSettings(c, trustForwarded, nil)

		resp := newIPAccessControlResponse(c, service.IPAccessControlSettings{IPv6BlockEnabled: true})
		require.Equal(t, trustForwarded, resp.ClientIPSpoofable)

		// Settings stay at the top level of the JSON the frontend already reads.
		encoded, err := json.Marshal(resp)
		require.NoError(t, err)
		var fields map[string]any
		require.NoError(t, json.Unmarshal(encoded, &fields))
		require.Equal(t, true, fields["ipv6_block_enabled"])
		require.Equal(t, trustForwarded, fields["client_ip_spoofable"])
		require.Contains(t, fields, "detected_client_ip")
	}
}
