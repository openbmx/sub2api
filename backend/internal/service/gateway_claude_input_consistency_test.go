//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestClaudeAutomaticCacheReservesOnlyEffectiveBreakpoints(t *testing.T) {
	for _, lastMarked := range []bool{false, true} {
		t.Run(map[bool]string{false: "automatic_new", true: "automatic_reuses_last"}[lastMarked], func(t *testing.T) {
			last := `{"type":"text","text":"last"}`
			if lastMarked {
				last = `{"type":"text","text":"last","cache_control":{"type":"ephemeral"}}`
			}
			body := []byte(`{"cache_control":{"type":"ephemeral"},"tools":[{"name":"lookup","input_schema":{"type":"object"},"cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":[{"type":"text","text":"a","cache_control":{"type":"ephemeral"}},{"type":"text","text":"b","cache_control":{"type":"ephemeral"}},{"type":"text","text":"c","cache_control":{"type":"ephemeral"}},` + last + `]}]}`)
			out := enforceCacheControlLimit(body)
			_, messages, tools, system := collectCacheControlPaths(out)
			count := len(messages) + len(tools) + len(system)
			if !gjson.GetBytes(out, "messages.0.content.3.cache_control").Exists() {
				count++
			}
			require.LessOrEqual(t, count, 4)
			if lastMarked {
				require.Len(t, messages, 4)
			}
			require.True(t, gjson.GetBytes(out, "cache_control").Exists())
		})
	}
}

func TestClaudeAutomaticCacheReusesLastTTL(t *testing.T) {
	body := []byte(`{"cache_control":{"type":"ephemeral"},"messages":[{"role":"user","content":[{"type":"text","text":"last","cache_control":{"type":"ephemeral","ttl":"1h"}}]}]}`)
	out := enforceCacheControlLimit(body)
	require.Equal(t, "1h", gjson.GetBytes(out, "cache_control.ttl").String())
	require.Equal(t, "1h", gjson.GetBytes(out, "messages.0.content.0.cache_control.ttl").String())
}

func TestClaudeCacheLimitKeepsAutomaticTargetsExplicitTTL(t *testing.T) {
	body := []byte(`{"cache_control":{"type":"ephemeral"},"tools":[{"name":"one","cache_control":{"type":"ephemeral"}},{"name":"two","cache_control":{"type":"ephemeral"}},{"name":"three","cache_control":{"type":"ephemeral"}},{"name":"four","cache_control":{"type":"ephemeral"}},{"name":"five","cache_control":{"type":"ephemeral","ttl":"1h"}}]}`)
	out := enforceCacheControlLimit(body)
	require.Equal(t, "1h", gjson.GetBytes(out, "tools.4.cache_control.ttl").String())
	require.Equal(t, "1h", gjson.GetBytes(out, "cache_control.ttl").String())
}

func TestClaudeTokenCountUsesTheGeneratedPrompt(t *testing.T) {
	upstream := &anthropicHTTPUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"input_tokens":1}`))}}
	svc := claudeRepairOAuthService(t, upstream, true)
	account := newAnthropicOAuthAccountForPartialUsageTest()
	body := []byte(`{"model":"claude-sonnet-4-6","max_tokens":1024,"system":"Always return JSON","messages":[{"role":"user","content":"hello"}]} `)
	c, _ := claudeRepairContext(body, "test-client/1.0")
	require.NoError(t, svc.ForwardCountTokens(context.Background(), c, account, claudeRepairParse(t, body)))
	counted := append([]byte(nil), upstream.lastBody...)
	upstream.resp = claudeRepairResponse("ok")
	c, _ = claudeRepairContext(body, "test-client/1.0")
	_, err := svc.Forward(context.Background(), c, account, claudeRepairParse(t, body))
	require.NoError(t, err)
	for _, path := range []string{"system", "messages", "tools"} {
		require.JSONEq(t, gjson.GetBytes(upstream.lastBody, path).Raw, gjson.GetBytes(counted, path).Raw, path)
	}
	require.False(t, gjson.GetBytes(counted, "max_tokens").Exists())
	require.False(t, gjson.GetBytes(counted, "metadata").Exists())
}

func TestClaudeSafeguardsRequireTheirFinalBeta(t *testing.T) {
	const beta = "dangerous-tool-use-2026-09-03"
	for _, drop := range []bool{false, true} {
		t.Run(map[bool]string{false: "preserve", true: "policy_drop"}[drop], func(t *testing.T) {
			body := []byte(`{"model":"claude-sonnet-4-6","max_tokens":1024,"messages":[{"role":"user","content":"hello"}],"safeguards":{"test_marker":"synthetic"}}`)
			c, _ := claudeRepairContext(body, "test-client/1.0")
			c.Request.Header.Set("anthropic-beta", beta)
			if drop {
				c.Set(betaPolicyFilterSetKey, map[string]struct{}{beta: {}})
			}
			svc := newForwardPartialUsageServiceForTest(&anthropicHTTPUpstreamRecorder{})
			req, out, err := svc.buildUpstreamRequest(context.Background(), c, newAnthropicOAuthAccountForPartialUsageTest(), body, "synthetic", "oauth", "claude-sonnet-4-6", false, true)
			if drop {
				require.Error(t, err)
				require.Nil(t, req)
				return
			}
			require.NoError(t, err)
			require.True(t, containsBetaToken(getHeaderRaw(req.Header, "anthropic-beta"), beta))
			require.JSONEq(t, gjson.GetBytes(body, "safeguards").Raw, gjson.GetBytes(out, "safeguards").Raw)
		})
	}
}

func TestClaudeCountDropsGenerationOnlySafetyAndMetadataFields(t *testing.T) {
	account := newAnthropicAPIKeyAccountForTest()
	body := []byte(`{"model":"claude-sonnet-4-6","max_tokens":1024,"metadata":{"user_id":"synthetic"},"service_tier":"auto","messages":[{"role":"user","content":"hello"}]}`)
	upstream := &anthropicHTTPUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"input_tokens":1}`))}}
	svc := newForwardPartialUsageServiceForTest(upstream)
	c, _ := claudeRepairContext(body, "test-client/1.0")
	require.NoError(t, svc.ForwardCountTokens(context.Background(), c, account, claudeRepairParse(t, body)))
	for _, field := range []string{"metadata", "service_tier", "max_tokens"} {
		require.False(t, gjson.GetBytes(upstream.lastBody, field).Exists(), field)
	}
	body = []byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hello"}],"safeguards":{"test_marker":"synthetic"}}`)
	c, rec := claudeRepairContext(body, "test-client/1.0")
	upstream.resp = &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"input_tokens":1}`))}
	require.NoError(t, svc.ForwardCountTokens(context.Background(), c, account, claudeRepairParse(t, body)))
	require.Equal(t, 200, rec.Code)
	require.False(t, gjson.GetBytes(upstream.lastBody, "safeguards").Exists())
}

func TestThirdPartyNativeSafetyFieldsKeepTheirOwnProtocol(t *testing.T) {
	body := []byte(`{"model":"kimi","messages":[{"role":"user","content":"hello"}],"safeguards":{"test_marker":"synthetic"}}`)
	c, _ := claudeRepairContext(body, "test-client/1.0")
	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	account := &Account{ID: 952, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "synthetic"}}
	request, out, err := svc.buildNativeAnthropicUpstreamRequest(context.Background(), c, account, body, "synthetic", "https://gateway.example/v1/messages")
	require.NoError(t, err)
	require.NotNil(t, request)
	require.True(t, gjson.GetBytes(out, "safeguards").Exists())
}
