//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestClaudeOAuthModelMappingAppliesAtEveryEntry(t *testing.T) {
	for _, typ := range []string{AccountTypeOAuth, AccountTypeSetupToken} {
		for _, route := range []string{"messages", "count", "responses", "chat"} {
			t.Run(typ+"/"+route, func(t *testing.T) {
				upstream := &anthropicHTTPUpstreamRecorder{}
				svc := claudeRepairOAuthService(t, upstream, true)
				account := &Account{ID: 941, Platform: PlatformAnthropic, Type: typ, Credentials: map[string]any{"access_token": "synthetic", "model_mapping": map[string]any{"public-claude": "claude-sonnet-5-5", "claude-haiku-4-5": "claude-haiku-4-5"}}}
				require.True(t, svc.isModelSupportedByAccount(account, "public-claude"))
				require.True(t, svc.isModelSupportedByAccount(account, "claude-haiku-4-5"))
				raw := []byte(`{"model":"public-claude","max_tokens":1024,"messages":[{"role":"user","content":"hello"}]}`)
				if route == "responses" {
					raw = []byte(`{"model":"public-claude","input":"hello","reasoning":{"effort":"high"}}`)
				}
				if route == "chat" {
					raw = []byte(`{"model":"public-claude","messages":[{"role":"user","content":"hello"}],"reasoning_effort":"high"}`)
				}
				c, _ := claudeRepairContext(raw, "test-client/1.0")
				upstream.resp = &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(claudeRepairSSE([]string{"ok"})))}
				var err error
				switch route {
				case "responses":
					_, err = svc.ForwardAsResponses(context.Background(), c, account, raw, nil)
				case "chat":
					_, err = svc.ForwardAsChatCompletions(context.Background(), c, account, raw, nil)
				case "count":
					upstream.resp = &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"input_tokens":1}`))}
					err = svc.ForwardCountTokens(context.Background(), c, account, claudeRepairParse(t, raw))
				default:
					upstream.resp = claudeRepairResponse("ok")
					_, err = svc.Forward(context.Background(), c, account, claudeRepairParse(t, raw))
				}
				require.NoError(t, err)
				require.Equal(t, "claude-sonnet-5-5", gjson.GetBytes(upstream.lastBody, "model").String())
				if route == "responses" || route == "chat" {
					require.Equal(t, "adaptive", gjson.GetBytes(upstream.lastBody, "thinking.type").String())
				}
			})
		}
	}
}

func TestClaudeModelResolverPreservesOtherOAuthContracts(t *testing.T) {
	account := &Account{Platform: PlatformAntigravity, Type: AccountTypeOAuth, Credentials: map[string]any{"model_mapping": map[string]any{"alias": "other"}}}
	require.Equal(t, "alias", resolveAnthropicUpstreamModel(account, "alias"))
}

func TestClaudeShortModelNamesStillReachLongMappingKeys(t *testing.T) {
	account := &Account{Platform: PlatformAnthropic, Type: AccountTypeOAuth, Credentials: map[string]any{"model_mapping": map[string]any{"claude-sonnet-4-5-20250929": "claude-sonnet-5-5"}}}
	svc := &GatewayService{}
	require.True(t, svc.isModelSupportedByAccount(account, "claude-sonnet-4-5"))
	require.Equal(t, "claude-sonnet-5-5", resolveAnthropicUpstreamModel(account, "claude-sonnet-4-5"))
}
