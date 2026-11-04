//go:build unit

package service

import (
	"context"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAnthropicFeatureHeadersReachTheirConfiguredOrigin(t *testing.T) {
	for _, count := range []bool{false, true} {
		body := []byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hello"}]}`)
		c, _ := claudeRepairContext(body, "test-client/1.0")
		c.Request.Header.Set("anthropic-workspace-id", "synthetic-workspace")
		c.Request.Header.Set("x-claude-code-agent-id", "synthetic-agent")
		c.Request.Header.Set("Authorization", "Bearer downstream-key")
		account := newAnthropicAPIKeyAccountForTest()
		svc := newForwardPartialUsageServiceForTest(&anthropicHTTPUpstreamRecorder{})
		if count {
			req, err := svc.buildCountTokensRequestAnthropicAPIKeyPassthrough(context.Background(), c, account, body, "upstream-key")
			require.NoError(t, err)
			require.Equal(t, "synthetic-workspace", getHeaderRaw(req.Header, "anthropic-workspace-id"))
			require.Equal(t, "synthetic-agent", getHeaderRaw(req.Header, "x-claude-code-agent-id"))
			require.Empty(t, getHeaderRaw(req.Header, "authorization"))
			require.Equal(t, "upstream-key", getHeaderRaw(req.Header, "x-api-key"))
		} else {
			req, _, err := svc.buildUpstreamRequestAnthropicAPIKeyPassthrough(context.Background(), c, account, body, "upstream-key")
			require.NoError(t, err)
			require.Equal(t, "synthetic-workspace", getHeaderRaw(req.Header, "anthropic-workspace-id"))
			require.Equal(t, "synthetic-agent", getHeaderRaw(req.Header, "x-claude-code-agent-id"))
			require.Empty(t, getHeaderRaw(req.Header, "authorization"))
			require.Equal(t, "upstream-key", getHeaderRaw(req.Header, "x-api-key"))
		}
	}
}

func TestAnthropicFeatureHeadersRespectOfficialOrigins(t *testing.T) {
	for _, tc := range []struct {
		address string
		allowed bool
	}{
		{"https://api.anthropic.com/v1/messages", true},
		{"https://aws-external-anthropic.us-east-1.api.aws/v1/messages", true},
		{"https://gateway.example/v1/messages", false},
	} {
		u, err := url.Parse(tc.address)
		require.NoError(t, err)
		require.Equal(t, tc.allowed, allowAnthropicRequestHeader("anthropic-workspace-id", u), tc.address)
	}
}
