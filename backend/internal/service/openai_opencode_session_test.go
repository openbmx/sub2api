package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func newOpenCodeSessionTestContext(t *testing.T, value string) *gin.Context {
	t.Helper()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	if value != "" {
		c.Request.Header.Set(openCodeSessionHeader, value)
	}
	return c
}

func openCodeSessionTestService() *OpenAIGatewayService {
	return &OpenAIGatewayService{cfg: &config.Config{
		Security: config.SecurityConfig{
			URLAllowlist: config.URLAllowlistConfig{Enabled: false},
		},
	}}
}

func openCodeSessionTestAccount(baseURL string) *Account {
	return &Account{
		ID:       1,
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"base_url":                   baseURL,
			credKeyHeaderOverrideEnabled: true,
			credKeyHeaderOverrides:       map[string]any{"x-opencode-session": "fixed-account-value"},
		},
	}
}

func requireSingleOpenCodeSessionHeader(t *testing.T, headers http.Header, want string) {
	t.Helper()
	count := 0
	for key, values := range headers {
		if strings.EqualFold(key, openCodeSessionHeader) {
			count += len(values)
			require.Equal(t, []string{want}, values)
		}
	}
	require.Equal(t, 1, count)
}

func TestApplyOpenCodeSessionHeaderTrustBoundary(t *testing.T) {
	tests := []struct {
		name      string
		account   *Account
		targetURL string
		incoming  string
		want      string
	}{
		{
			name:      "official origin",
			account:   openCodeSessionTestAccount("https://opencode.ai/zen/v1"),
			targetURL: "https://opencode.ai/zen/v1/chat/completions",
			incoming:  " conversation-123 ",
			want:      "conversation-123",
		},
		{
			name:      "lookalike origin",
			account:   openCodeSessionTestAccount("https://opencode.ai.evil.example/v1"),
			targetURL: "https://opencode.ai.evil.example/v1/responses",
			incoming:  "conversation-123",
		},
		{
			name:      "subdomain is not implicitly trusted",
			account:   openCodeSessionTestAccount("https://api.opencode.ai/v1"),
			targetURL: "https://api.opencode.ai/v1/responses",
			incoming:  "conversation-123",
		},
		{
			name:      "insecure official origin",
			account:   openCodeSessionTestAccount("http://opencode.ai/zen/v1"),
			targetURL: "http://opencode.ai/zen/v1/responses",
			incoming:  "conversation-123",
		},
		{
			name:      "oauth account",
			account:   &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth},
			targetURL: "https://opencode.ai/zen/v1/responses",
			incoming:  "conversation-123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			headers := make(http.Header)
			applyOpenCodeSessionHeader(newOpenCodeSessionTestContext(t, tt.incoming), tt.account, tt.targetURL, headers, nil)
			require.Equal(t, tt.want, headers.Get(openCodeSessionHeader))
		})
	}

	// Only the trust boundary suppresses the header. On the official origin a
	// caller that sends nothing still gets one, because OpenCode rejects the
	// request outright without it.
	t.Run("missing caller value falls back instead of omitting", func(t *testing.T) {
		headers := make(http.Header)
		applyOpenCodeSessionHeader(newOpenCodeSessionTestContext(t, ""),
			openCodeSessionTestAccount("https://opencode.ai/zen/v1"),
			"https://opencode.ai/zen/v1/responses", headers, nil)
		require.NotEmpty(t, headers.Get(openCodeSessionHeader))
		require.True(t, strings.HasPrefix(headers.Get(openCodeSessionHeader), "sub2api-apikey-"))
	})
}

// A plain OpenAI SDK client sends no session identity of any kind. Before the
// fallback existed this was a hard 400 MissingSessionID from OpenCode, which
// made the platform unusable for everything except Claude Code and Codex.
func TestOpenCodeSessionFallbackTiers(t *testing.T) {
	account := openCodeSessionTestAccount("https://opencode.ai/zen/v1")
	const targetURL = "https://opencode.ai/zen/v1/chat/completions"

	t.Run("explicit caller header is forwarded verbatim", func(t *testing.T) {
		headers := make(http.Header)
		applyOpenCodeSessionHeader(newOpenCodeSessionTestContext(t, "caller-chosen"), account, targetURL, headers, nil)
		require.Equal(t, "caller-chosen", headers.Get(openCodeSessionHeader))
	})

	t.Run("recognized conversation header is hashed, not forwarded", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		c.Request.Header.Set("X-Session-Id", "claude-code-conversation-uuid")

		headers := make(http.Header)
		applyOpenCodeSessionHeader(c, account, targetURL, headers, nil)
		got := headers.Get(openCodeSessionHeader)
		require.True(t, strings.HasPrefix(got, "sub2api-conversation-"))
		require.NotContains(t, got, "claude-code-conversation-uuid")
	})

	t.Run("prompt_cache_key in the body is used before the coarse fallback", func(t *testing.T) {
		headers := make(http.Header)
		applyOpenCodeSessionHeader(newOpenCodeSessionTestContext(t, ""), account, targetURL, headers,
			[]byte(`{"model":"kimi-k3","prompt_cache_key":"turn-42","messages":[]}`))
		require.True(t, strings.HasPrefix(headers.Get(openCodeSessionHeader), "sub2api-conversation-"))
	})

	t.Run("derivation is stable across calls", func(t *testing.T) {
		first, second := make(http.Header), make(http.Header)
		applyOpenCodeSessionHeader(newOpenCodeSessionTestContext(t, ""), account, targetURL, first, nil)
		applyOpenCodeSessionHeader(newOpenCodeSessionTestContext(t, ""), account, targetURL, second, nil)
		require.Equal(t, first.Get(openCodeSessionHeader), second.Get(openCodeSessionHeader))
	})

	// A bare Go-http-client agent is the case OpenCode's docs call out by name,
	// so an empty header must never reach them.
	t.Run("fills a missing user agent with a recognized client identity", func(t *testing.T) {
		filled := make(http.Header)
		applyOpenCodeSessionHeader(newOpenCodeSessionTestContext(t, ""), account, targetURL, filled, nil)
		got := filled.Get("User-Agent")
		require.Equal(t, claude.DefaultHeaders["User-Agent"], got)
		require.True(t, strings.HasPrefix(got, "claude-cli/"))
		require.Contains(t, got, claude.CLIVersion(),
			"must track the same version source as the Anthropic path, not a hardcoded copy")
	})

	t.Run("never clobbers an agent the client already sent", func(t *testing.T) {
		kept := make(http.Header)
		kept.Set("User-Agent", "codex_cli_rs/0.58.0")
		applyOpenCodeSessionHeader(newOpenCodeSessionTestContext(t, ""), account, targetURL, kept, nil)
		require.Equal(t, "codex_cli_rs/0.58.0", kept.Get("User-Agent"))
	})
}

func TestOpenCodeSessionForwardedByResponsesBuildersAfterAccountOverride(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := openCodeSessionTestService()
	account := openCodeSessionTestAccount("https://opencode.ai/zen/v1")
	body := []byte(`{"model":"gpt-5","input":"hello"}`)

	tests := []struct {
		name  string
		build func(*gin.Context) (*http.Request, error)
	}{
		{
			name: "normal responses",
			build: func(c *gin.Context) (*http.Request, error) {
				return svc.buildUpstreamRequest(context.Background(), c, account, body, "token", false, "", false)
			},
		},
		{
			name: "passthrough responses",
			build: func(c *gin.Context) (*http.Request, error) {
				return svc.buildUpstreamRequestOpenAIPassthrough(context.Background(), c, account, body, "token")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newOpenCodeSessionTestContext(t, "conversation-456")
			req, err := tt.build(c)
			require.NoError(t, err)
			requireSingleOpenCodeSessionHeader(t, req.Header, "conversation-456")
		})
	}
}

func TestOpenCodeSessionMissingCallerValueKeepsExistingOverrideBehavior(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := openCodeSessionTestService()
	account := openCodeSessionTestAccount("https://opencode.ai/zen/v1")
	c := newOpenCodeSessionTestContext(t, "")

	req, err := svc.buildUpstreamRequest(
		context.Background(), c, account,
		[]byte(`{"model":"gpt-5","input":"hello"}`), "token", false, "", false,
	)
	require.NoError(t, err)
	require.Equal(t, "fixed-account-value", getHeaderRaw(req.Header, "x-opencode-session"))
}

type openCodeSessionHTTPUpstream struct {
	request *http.Request
}

func (u *openCodeSessionHTTPUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.request = req
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(`{}`)),
	}, nil
}

func (u *openCodeSessionHTTPUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, concurrency)
}

func TestOpenCodeSessionForwardedByRawChatCompletionsAfterAccountOverride(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := &openCodeSessionHTTPUpstream{}
	svc := openCodeSessionTestService()
	svc.httpUpstream = upstream
	account := openCodeSessionTestAccount("https://opencode.ai/zen/v1")
	c := newOpenCodeSessionTestContext(t, "conversation-789")

	resp, err := svc.sendCCUpstreamRequest(
		context.Background(), c, account,
		"https://opencode.ai/zen/v1/chat/completions", []byte(`{"model":"gpt-5"}`),
		false, "token", "", "",
	)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.NotNil(t, upstream.request)
	requireSingleOpenCodeSessionHeader(t, upstream.request.Header, "conversation-789")
}

func TestOpenCodeSessionIsNotForwardedToOtherUpstreams(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := openCodeSessionTestService()
	body := []byte(`{"model":"gpt-5","input":"hello"}`)

	for _, baseURL := range []string{
		"https://api.openai.com/v1",
		"https://opencode.ai.evil.example/v1",
		"https://api.opencode.ai/v1",
	} {
		t.Run(baseURL, func(t *testing.T) {
			account := &Account{
				ID:          1,
				Platform:    PlatformOpenAI,
				Type:        AccountTypeAPIKey,
				Credentials: map[string]any{"base_url": baseURL},
			}
			c := newOpenCodeSessionTestContext(t, "private-conversation")
			req, err := svc.buildUpstreamRequest(context.Background(), c, account, body, "token", false, "", false)
			require.NoError(t, err)
			require.Empty(t, req.Header.Get(openCodeSessionHeader))
		})
	}
}
