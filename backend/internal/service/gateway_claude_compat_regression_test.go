//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const claudeRepairModel = "claude-sonnet-4-5"
const claudeRepairMarker = "REVIEW_KEEP_THIS_CUSTOM_RULE"

func claudeRepairResponse(text string) *http.Response {
	body, _ := json.Marshal(map[string]any{
		"id": "msg_claudeRepair", "type": "message", "role": "assistant", "model": claudeRepairModel,
		"content":     []any{map[string]any{"type": "text", "text": text}},
		"stop_reason": "end_turn", "usage": map[string]any{"input_tokens": 1, "output_tokens": 1},
	})
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(string(body)))}
}

func claudeRepairContext(body []byte, ua string) (*gin.Context, *httptest.ResponseRecorder) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(string(body)))
	c.Request.Header.Set("User-Agent", ua)
	return c, rec
}

func claudeRepairParse(t *testing.T, body []byte) *ParsedRequest {
	t.Helper()
	parsed, err := ParseGatewayRequest(NewRequestBodyRef(body), PlatformAnthropic)
	if err != nil {
		t.Fatal(err)
	}
	parsed.SessionContext = &SessionContext{ClientIP: "127.0.0.1", UserAgent: "claudeRepair-client/1.0.0", APIKeyID: 123}
	return parsed
}

func claudeRepairOAuthService(t *testing.T, upstream *anthropicHTTPUpstreamRecorder, injection bool) *GatewayService {
	t.Helper()
	resetGatewayForwardingSettingsCacheForTest(t)
	svc := newForwardPartialUsageServiceForTest(upstream)
	value := "false"
	if injection {
		value = "true"
	}
	svc.settingService = NewSettingService(&gatewayTTLSettingRepo{data: map[string]string{
		SettingKeyEnableClaudeOAuthSystemPromptInjection: value,
	}}, svc.cfg)
	svc.identityService = NewIdentityService(&stubIdentityCache{fingerprint: &Fingerprint{
		UserAgent: claude.DefaultUserAgent(), ClientID: strings.Repeat("a", 64), UpdatedAt: time.Now().Unix(),
	}})
	return svc
}

func TestClaudeRepairVertexCountTokensThroughForward(t *testing.T) {
	gin.SetMode(gin.TestMode)
	account := &Account{ID: 870, Platform: PlatformAnthropic, Type: AccountTypeServiceAccount, Status: StatusActive, Schedulable: true, Credentials: map[string]any{
		"service_account_json": map[string]any{"type": "service_account", "project_id": "claudeRepair-project", "client_email": "claudeRepair@example.invalid", "private_key": "SYNTHETIC_UNUSED_PRIVATE_KEY", "private_key_id": "claudeRepair-key"},
	}}
	key, err := parseVertexServiceAccountKey(account)
	if err != nil {
		t.Fatal(err)
	}
	cache := newClaudeTokenCacheStub()
	if err := cache.SetAccessToken(context.Background(), vertexServiceAccountCacheKey(account, key), "SYNTHETIC_GOOGLE_TOKEN", time.Hour); err != nil {
		t.Fatal(err)
	}
	upstream := &anthropicHTTPUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"input_tokens":5}`))}}
	svc := newForwardPartialUsageServiceForTest(upstream)
	svc.claudeTokenProvider = NewClaudeTokenProvider(nil, cache, nil)
	body := []byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"count this"}]}`)
	c, rec := claudeRepairContext(body, "claudeRepair-client/1.0.0")
	if err := svc.ForwardCountTokens(context.Background(), c, account, claudeRepairParse(t, body)); err != nil {
		t.Fatal(err)
	}
	if upstream.lastReq != nil || rec.Code != http.StatusNotFound || cache.getCalled != 0 {
		t.Fatalf("Vertex count_tokens must return 404 before token lookup or transport; response=%d token_reads=%d request=%v", rec.Code, cache.getCalled, upstream.lastReq)
	}
}

func claudeRepairSSE(texts []string) string {
	events := []string{
		`event: message_start` + "\n" + `data: {"type":"message_start","message":{"id":"msg_claudeRepair","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
		`event: content_block_start` + "\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
	}
	for _, text := range texts {
		delta, _ := json.Marshal(map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": text}})
		events = append(events, "event: content_block_delta\ndata: "+string(delta))
	}
	events = append(events,
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}",
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}",
		"event: message_stop\ndata: {\"type\":\"message_stop\"}",
	)
	return strings.Join(events, "\n\n") + "\n\n"
}

func TestClaudeRepairResponseLiteralAcrossForwardModes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, mode := range []string{"api_normal", "api_passthrough", "native_oauth"} {
		for _, stream := range []bool{false, true} {
			label := mode + "/json"
			if stream {
				label = mode + "/sse"
			}
			t.Run(label, func(t *testing.T) {
				bodyMap := map[string]any{"model": claudeRepairModel, "stream": stream, "messages": []any{map[string]any{"role": "user", "content": "quote literal cc_sess_list"}}}
				ua := "claudeRepair-client/1.0.0"
				account := &Account{ID: 871, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "SYNTHETIC_KEY"}, Extra: map[string]any{"anthropic_passthrough": mode == "api_passthrough"}}
				if mode == "native_oauth" {
					account = newAnthropicOAuthAccountForPartialUsageTest()
					ua = claude.DefaultUserAgent()
					bodyMap["metadata"] = map[string]any{"user_id": FormatMetadataUserID(strings.Repeat("a", 64), "", "7578cf37-aaca-46e4-a45c-71285d9dbb83", ExtractCLIVersion(ua))}
				}
				body, _ := json.Marshal(bodyMap)
				response := claudeRepairResponse("literal cc_sess_list")
				if stream {
					response.Header.Set("Content-Type", "text/event-stream")
					response.Body = io.NopCloser(strings.NewReader(claudeRepairSSE([]string{"literal cc_sess_list"})))
				}
				upstream := &anthropicHTTPUpstreamRecorder{resp: response}
				svc := newForwardPartialUsageServiceForTest(upstream)
				c, rec := claudeRepairContext(body, ua)
				if _, err := svc.Forward(context.Background(), c, account, claudeRepairParse(t, body)); err != nil {
					t.Fatal(err)
				}
				got := rec.Body.String()
				if !strings.Contains(got, "literal cc_sess_list") {
					t.Fatalf("literal response was modified: %s", got)
				}
				if toolNameRewriteFromContext(c) != nil {
					t.Fatal("control setup unexpectedly generated a tool mapping")
				}
				t.Log("response literal remains unchanged without a tool mapping")
			})
		}
	}
	t.Run("sse_split_is_not_reassembled_for_rewrite", func(t *testing.T) {
		upstream := &anthropicHTTPUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(claudeRepairSSE([]string{"cc_", "sess_list"})))}}
		svc := newForwardPartialUsageServiceForTest(upstream)
		body := []byte(`{"model":"claude-sonnet-4-5","stream":true,"messages":[{"role":"user","content":"hello"}]}`)
		c, rec := claudeRepairContext(body, "claudeRepair-client/1.0.0")
		account := &Account{ID: 872, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "SYNTHETIC_KEY"}, Extra: map[string]any{"anthropic_passthrough": false}}
		if _, err := svc.Forward(context.Background(), c, account, claudeRepairParse(t, body)); err != nil {
			t.Fatal(err)
		}
		var joined strings.Builder
		for _, line := range strings.Split(rec.Body.String(), "\n") {
			if strings.HasPrefix(line, "data:") {
				data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
				if gjson.Get(data, "delta.type").String() == "text_delta" {
					joined.WriteString(gjson.Get(data, "delta.text").String())
				}
			}
		}
		if joined.String() != "cc_sess_list" {
			t.Fatalf("split control changed: %q", joined.String())
		}
		t.Log("split across SSE events: literal preserved; rewrite is event-local")
	})
}

func TestClaudeRepairSystemBannerThroughForwardAndControls(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name                        string
		native, injection, wantKept bool
	}{
		{"third_party_injection", false, true, true},
		{"third_party_injection_disabled", false, false, true},
		{"native_cli_with_metadata", true, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := &anthropicHTTPUpstreamRecorder{resp: claudeRepairResponse("ok")}
			svc := claudeRepairOAuthService(t, upstream, tc.injection)
			bodyMap := map[string]any{"model": claudeRepairModel, "messages": []any{map[string]any{"role": "user", "content": "hello"}}, "system": []any{map[string]any{"type": "text", "text": claudeCodeSystemPrompt}, map[string]any{"type": "text", "text": claudeRepairMarker}}}
			ua := "claudeRepair-client/1.0.0"
			if tc.native {
				ua = claude.DefaultUserAgent()
				bodyMap["metadata"] = map[string]any{"user_id": FormatMetadataUserID(strings.Repeat("a", 64), "", "7578cf37-aaca-46e4-a45c-71285d9dbb83", ExtractCLIVersion(ua))}
			}
			body, _ := json.Marshal(bodyMap)
			c, _ := claudeRepairContext(body, ua)
			account := newAnthropicOAuthAccountForPartialUsageTest()
			if _, err := svc.Forward(context.Background(), c, account, claudeRepairParse(t, body)); err != nil {
				t.Fatal(err)
			}
			kept := strings.Contains(string(upstream.lastBody), claudeRepairMarker)
			if kept != tc.wantKept {
				t.Fatalf("rule retained=%t want=%t; outgoing body=%s", kept, tc.wantKept, upstream.lastBody)
			}
			if count := strings.Count(string(upstream.lastBody), claudeCodeSystemPrompt); count != 1 {
				t.Fatalf("identity banner must appear once; got %d: %s", count, upstream.lastBody)
			}
			t.Logf("actual outgoing rule retained=%t", kept)
		})
	}
}

func TestClaudeRepairSessionIDsThroughForward(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, injection := range []bool{false, true} {
		name := "injection_disabled"
		if injection {
			name = "injection_enabled"
		}
		t.Run(name, func(t *testing.T) {
			upstream := &anthropicHTTPUpstreamRecorder{}
			svc := claudeRepairOAuthService(t, upstream, injection)
			account := newAnthropicOAuthAccountForPartialUsageTest()
			ids := []string{}
			for _, opener := range []string{"first conversation", "different conversation"} {
				body, _ := json.Marshal(map[string]any{"model": claudeRepairModel, "system": "same application rules", "messages": []any{map[string]any{"role": "user", "content": opener}}})
				c, _ := claudeRepairContext(body, "claudeRepair-client/1.0.0")
				upstream.resp = claudeRepairResponse("ok")
				if _, err := svc.Forward(context.Background(), c, account, claudeRepairParse(t, body)); err != nil {
					t.Fatal(err)
				}
				uid := ParseMetadataUserID(gjson.GetBytes(upstream.lastBody, "metadata.user_id").String())
				if uid == nil {
					t.Fatal("outgoing metadata user_id missing or invalid")
				}
				ids = append(ids, uid.SessionID)
			}
			equal := ids[0] == ids[1]
			if equal {
				t.Fatalf("session IDs equal=%t; injection=%t", equal, injection)
			}
			t.Logf("two different openers; same client discriminator; session IDs equal=%t", equal)
		})
	}
}

func TestClaudeRepairForeignPrimerAffectsNativeForwardIdentity(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := &anthropicHTTPUpstreamRecorder{resp: claudeRepairResponse("ok")}
	svc := claudeRepairOAuthService(t, upstream, true)
	svc.identityService = NewIdentityService(&stubIdentityCache{})
	account := newAnthropicOAuthAccountForPartialUsageTest()
	account.Extra = map[string]any{"account_uuid": "11111111-2222-4333-8444-555555555555"}
	body := []byte(`{"model":"claude-sonnet-4-5","system":"application rules","messages":[{"role":"user","content":"hello"}]}`)
	primer, _ := claudeRepairContext(body, "httpx/0.28.1")
	primer.Request.Header.Set("X-Stainless-Lang", "python")
	if _, err := svc.Forward(context.Background(), primer, account, claudeRepairParse(t, body)); err != nil {
		t.Fatal(err)
	}
	primerUA := getHeaderRaw(upstream.lastReq.Header, "User-Agent")
	primerUID := ParseMetadataUserID(gjson.GetBytes(upstream.lastBody, "metadata.user_id").String())
	if !strings.HasPrefix(primerUA, "claude-cli/") || primerUID == nil || !primerUID.IsNewFormat {
		t.Fatalf("unexpected primer wire identity UA=%s metadata=%s", primerUA, gjson.GetBytes(upstream.lastBody, "metadata.user_id").String())
	}
	nativeUA := claude.DefaultUserAgent()
	nativeBody, _ := json.Marshal(map[string]any{"model": claudeRepairModel, "system": claudeCodeSystemPrompt, "messages": []any{map[string]any{"role": "user", "content": "native question"}}, "metadata": map[string]any{"user_id": FormatMetadataUserID(strings.Repeat("b", 64), "", "7578cf37-aaca-46e4-a45c-71285d9dbb83", ExtractCLIVersion(nativeUA))}})
	native, _ := claudeRepairContext(nativeBody, nativeUA)
	native.Request.Header.Set("X-Stainless-Lang", "js")
	upstream.resp = claudeRepairResponse("ok")
	if _, err := svc.Forward(context.Background(), native, account, claudeRepairParse(t, nativeBody)); err != nil {
		t.Fatal(err)
	}
	actualUA := getHeaderRaw(upstream.lastReq.Header, "User-Agent")
	actualLang := getHeaderRaw(upstream.lastReq.Header, "X-Stainless-Lang")
	if !strings.HasPrefix(actualUA, "claude-cli/") || actualLang != "js" {
		t.Fatalf("characterization differs UA=%q lang=%q", actualUA, actualLang)
	}
	t.Logf("primer outgoing UA=%s with JSON metadata; subsequent native Forward outgoing UA=%s lang=%s", primerUA, actualUA, actualLang)
}

func TestClaudeRepairToolMappingSurvivesAccountSwitchAttempts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	bodyMap := map[string]any{"model": claudeRepairModel, "messages": []any{map[string]any{"role": "user", "content": "use the tools"}}}
	tools := []any{}
	for _, name := range []string{"read_file", "write_file", "list_files", "search_code", "run_tests", "inspect_repo"} {
		tools = append(tools, map[string]any{"name": name, "input_schema": map[string]any{"type": "object"}})
	}
	bodyMap["tools"] = tools
	body, _ := json.Marshal(bodyMap)
	c, rec := claudeRepairContext(body, "claudeRepair-client/1.0.0")
	upstream := &anthropicHTTPUpstreamRecorder{err: io.ErrUnexpectedEOF}
	svc := newForwardPartialUsageServiceForTest(upstream)
	firstAccount := newAnthropicOAuthAccountForPartialUsageTest()
	_, err := svc.Forward(context.Background(), c, firstAccount, claudeRepairParse(t, body))
	var failover *UpstreamFailoverError
	if !errors.As(err, &failover) {
		t.Fatalf("first attempt did not produce failover: %v", err)
	}
	if rec.Body.Len() != 0 {
		t.Fatal("first failed attempt committed output")
	}
	rw := toolNameRewriteFromContext(c)
	if rw == nil || len(rw.Reverse) == 0 {
		t.Fatal("first attempt did not save dynamic tool mapping")
	}
	var fake string
	for name := range rw.Reverse {
		fake = name
		break
	}
	upstream.err = nil
	upstream.resp = &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(strings.NewReader(`{"content":[{"type":"tool_use","name":"` + fake + `","input":{}}],"usage":{}}`))}
	if _, err := svc.Forward(context.Background(), c, newAnthropicAPIKeyAccountForTest(), claudeRepairParse(t, body)); err != nil {
		t.Fatal(err)
	}
	got := gjson.Get(rec.Body.String(), "content.0.name").String()
	if got != fake || toolNameRewriteFromContext(c) != nil {
		t.Fatalf("characterization differs: %q", got)
	}
	t.Log("same-context OAuth failure does not alter the second account response")
}

func TestClaudeRepairToolAliasesDoNotCollide(t *testing.T) {
	body := []byte(`{"tools":[{"name":"sessions_list","input_schema":{}},{"name":"cc_sess_list","input_schema":{}}],"tool_choice":{"type":"tool","name":"sessions_list"},"messages":[{"role":"assistant","content":[{"type":"tool_use","name":"sessions_list"},{"type":"tool_use","name":"cc_sess_list"}]}]}`)
	rw := buildToolNameRewriteFromBody(body)
	got := applyToolNameRewriteToBody(body, rw)
	seen := map[string]bool{}
	for _, tool := range gjson.GetBytes(got, "tools").Array() {
		name := tool.Get("name").String()
		if seen[name] {
			t.Fatalf("tool alias collided with another declared name: %q", name)
		}
		seen[name] = true
	}
	for _, path := range []string{"tool_choice.name", "messages.0.content.0.name"} {
		if gjson.GetBytes(got, path).String() != gjson.GetBytes(got, "tools.0.name").String() {
			t.Fatalf("tool reference no longer matches its declaration: %s", got)
		}
	}
	if gjson.GetBytes(got, "messages.0.content.1.name").String() != gjson.GetBytes(got, "tools.1.name").String() {
		t.Fatalf("second tool reference changed: %s", got)
	}
	reply := []byte(`{"content":[{"type":"tool_use","name":"cc_sess_list"}]}`)
	if string(restoreToolNamesInBytes(reply, rw)) != string(reply) {
		t.Fatal("an unchanged tool name was restored as a different tool")
	}
}

func TestClaudeRepairResponseNamesAreFieldScoped(t *testing.T) {
	rw := &ToolNameRewrite{Reverse: map[string]string{"fake_tool": "real_tool"}}
	cases := []struct{ input, path string }{
		{`{"content":[{"type":"tool_use","name":"fake_tool","input":{"name":"fake_tool","text":"fake_tool"}},{"type":"text","text":"fake_tool"}],"id":"fake_tool"}`, "content.0.name"},
		{`{"type":"content_block_start","content_block":{"type":"tool_use","name":"fake_tool","input":{"name":"fake_tool"}}}`, "content_block.name"},
		{`{"choices":[{"message":{"tool_calls":[{"type":"function","function":{"name":"fake_tool","arguments":"fake_tool"}}]}}]}`, "choices.0.message.tool_calls.0.function.name"},
		{`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"fake_tool","arguments":"fake_tool"}}]}}]}`, "choices.0.delta.tool_calls.0.function.name"},
		{`{"output":[{"type":"function_call","name":"fake_tool","arguments":"fake_tool"}]}`, "output.0.name"},
		{`{"type":"response.output_item.added","item":{"type":"function_call","name":"fake_tool","arguments":"fake_tool"}}`, "item.name"},
		{`{"type":"response.function_call_arguments.done","name":"fake_tool","arguments":"fake_tool"}`, "name"},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			got := restoreToolNamesInBytes([]byte(tc.input), rw)
			if gjson.GetBytes(got, tc.path).String() != "real_tool" {
				t.Fatalf("tool field was not restored: %s", got)
			}
			want, err := sjson.SetBytes([]byte(tc.input), tc.path, "real_tool")
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != string(want) {
				t.Fatalf("non-tool data changed: got %s want %s", got, want)
			}
			sse := []byte("event: tool\r\ndata: " + tc.input + "\r\n\r\n")
			expected := []byte("event: tool\r\ndata: " + string(want) + "\r\n\r\n")
			if actual := restoreToolNamesInBytes(sse, rw); string(actual) != string(expected) {
				t.Fatalf("SSE framing or data changed: %s", actual)
			}
		})
	}
	for _, body := range []string{`{"name":"fake_tool","text":"fake_tool"}`, `{"type":"content_block_delta","delta":{"type":"input_json_delta","partial_json":"fake_tool"}}`, `{"content":[{"type":"server_tool_use","name":"fake_tool"}]}`} {
		if got := restoreToolNamesInBytes([]byte(body), rw); string(got) != body {
			t.Fatalf("ordinary fields changed: %s", got)
		}
	}
}

type claudeRepairFailedIdentityCache struct{ stubIdentityCache }

func (c *claudeRepairFailedIdentityCache) GetFingerprint(context.Context, int64) (*Fingerprint, error) {
	return nil, errors.New("identity cache unavailable")
}

func TestClaudeRepairIdentityFailureStopsConfiguredOAuthForward(t *testing.T) {
	for _, endpoint := range []string{"messages", "count_tokens", "chat_completions", "responses"} {
		t.Run(endpoint, func(t *testing.T) {
			upstream := &anthropicHTTPUpstreamRecorder{resp: claudeRepairResponse("must not forward")}
			svc := claudeRepairOAuthService(t, upstream, true)
			svc.identityService = NewIdentityService(&claudeRepairFailedIdentityCache{})
			account := newAnthropicOAuthAccountForPartialUsageTest()
			body := []byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hello"}]}`)
			c, _ := claudeRepairContext(body, "httpx/0.28.1")
			var err error
			switch endpoint {
			case "messages":
				_, err = svc.Forward(context.Background(), c, account, claudeRepairParse(t, body))
			case "count_tokens":
				err = svc.ForwardCountTokens(context.Background(), c, account, claudeRepairParse(t, body))
			case "chat_completions":
				_, err = svc.ForwardAsChatCompletions(context.Background(), c, account, body, nil)
			case "responses":
				_, err = svc.ForwardAsResponses(context.Background(), c, account, []byte(`{"model":"claude-sonnet-4-5","input":"hello"}`), nil)
			}
			if err == nil || upstream.lastReq != nil {
				t.Fatalf("configured identity failure must stop before transport; err=%v request=%v", err, upstream.lastReq)
			}
		})
	}
}

func TestClaudeRepairSystemMigrationPreservesBlockBoundaries(t *testing.T) {
	system := []any{
		map[string]any{"type": "text", "text": "stable rules", "cache_control": map[string]any{"type": "ephemeral", "ttl": "1h"}},
		map[string]any{"type": "text", "text": "per-request context"},
	}
	body := []byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"question"}]}`)
	got := rewriteSystemForNonClaudeCodeWithPromptBlocks(body, system, "", "")
	blocks := gjson.GetBytes(got, "messages.0.content").Array()
	if len(blocks) != 2 || !strings.Contains(blocks[0].Get("text").String(), "stable rules") || blocks[0].Get("cache_control.ttl").String() != "1h" || blocks[1].Get("cache_control").Exists() || blocks[1].Get("text").String() != "per-request context" {
		t.Fatalf("migrated system cache boundary changed: %s", got)
	}
}

func TestClaudeRepairIdentityOptOutDoesNotRequireCache(t *testing.T) {
	upstream := &anthropicHTTPUpstreamRecorder{resp: claudeRepairResponse("ok")}
	svc := claudeRepairOAuthService(t, upstream, true)
	svc.settingService = NewSettingService(&gatewayTTLSettingRepo{data: map[string]string{
		SettingKeyEnableFingerprintUnification: "false", SettingKeyEnableMetadataPassthrough: "true",
	}}, svc.cfg)
	svc.identityService = NewIdentityService(&claudeRepairFailedIdentityCache{})
	body := []byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hello"}]}`)
	c, _ := claudeRepairContext(body, "client/1.0.0")
	if _, err := svc.Forward(context.Background(), c, newAnthropicOAuthAccountForPartialUsageTest(), claudeRepairParse(t, body)); err != nil {
		t.Fatal(err)
	}
	if upstream.lastReq == nil {
		t.Fatal("explicitly disabled identity rewriting must keep working without a cache")
	}
}
