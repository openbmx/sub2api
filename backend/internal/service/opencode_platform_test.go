package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// openCodeAccount builds the account shape the admin dialog produces: an API-key
// account on the opencode platform, with the mode deciding Go vs Zen.
func openCodeAccount(mode, protocol string) *Account {
	return &Account{
		ID:       7,
		Platform: PlatformOpenCode,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":      "sk-test",
			"account_mode": mode,
			"api_protocol": protocol,
		},
	}
}

// The Go subscription and the Zen pay-as-you-go balance live under different
// path prefixes on the same host. Picking the wrong one is a 404 the operator
// never sees, because the URL is derived rather than typed.
func TestOpenCodeBaseURLsSplitByAccountMode(t *testing.T) {
	tests := []struct {
		name             string
		mode             string
		chatCompletions  string
		anthropic        string
		responses        string
		openAIFormatBase string
	}{
		{
			name:            "coding plan uses the Go subscription prefix",
			mode:            AccountModeCoding,
			chatCompletions: "https://opencode.ai/zen/go/v1",
			anthropic:       "https://opencode.ai/zen/go",
			responses:       "https://opencode.ai/zen/go/v1",
		},
		{
			name:            "pay as you go uses the Zen balance prefix",
			mode:            AccountModePayG,
			chatCompletions: "https://opencode.ai/zen/v1",
			anthropic:       "https://opencode.ai/zen",
			responses:       "https://opencode.ai/zen/v1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			account := openCodeAccount(tt.mode, APIProtocolAdaptive)
			require.Equal(t, tt.chatCompletions, account.GetCNProtocolBaseURL(APIProtocolChatCompletions))
			require.Equal(t, tt.anthropic, account.GetCNProtocolBaseURL(APIProtocolAnthropic))
			require.Equal(t, tt.responses, account.GetCNProtocolBaseURL(APIProtocolResponses))
		})
	}
}

// The Anthropic base intentionally stops before the version segment because the
// forwarder appends /v1/messages. Getting this wrong yields
// .../zen/go/v1/v1/messages, so assert the joined URL rather than the constant.
func TestOpenCodeAnthropicBaseLeavesRoomForTheVersionSegment(t *testing.T) {
	account := openCodeAccount(AccountModeCoding, APIProtocolAnthropic)
	require.Equal(t, "https://opencode.ai/zen/go", account.GetAnthropicProtocolBaseURL())
	require.Equal(t, "https://opencode.ai/zen/go/v1/messages",
		account.GetAnthropicProtocolBaseURL()+"/v1/messages")
}

// On an anthropic-protocol account the stored base_url points at the Anthropic
// endpoint. GetOpenAIFormatBaseURL must not hand that back for OpenAI-shaped
// paths like /v1/models, or model sync silently probes the wrong endpoint.
func TestOpenCodeOpenAIFormatBaseIgnoresTheAnthropicCredential(t *testing.T) {
	account := openCodeAccount(AccountModeCoding, APIProtocolAnthropic)
	account.Credentials["base_url"] = "https://opencode.ai/zen/go"
	require.Equal(t, "https://opencode.ai/zen/go/v1", account.GetOpenAIFormatBaseURL())
}

// The native-Anthropic builder is shared by three forward paths: inbound
// /v1/messages, CC→Anthropic and Responses→Anthropic. It lives outside the
// OpenAI request builders, so the session header has to be attached here
// separately — and OpenCode routes MiniMax M3 and the Qwen3.x family through
// exactly this endpoint, so missing it takes those models out entirely.
func TestNativeAnthropicForwardCarriesTheOpenCodeSessionHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := openCodeSessionTestService()
	account := openCodeAccount(AccountModeCoding, APIProtocolAnthropic)

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	req, _, err := svc.buildNativeAnthropicUpstreamRequest(
		context.Background(), c, account,
		[]byte(`{"model":"minimax-m3","messages":[],"max_tokens":16}`),
		"sk-test", "https://opencode.ai/zen/go/v1/messages",
	)
	require.NoError(t, err)
	require.NotEmpty(t, req.Header.Get(openCodeSessionHeader),
		"an OpenCode anthropic-protocol account would 400 without this")
	require.NotEmpty(t, req.Header.Get("User-Agent"))
}

// The same builder serves every other multi-protocol provider, none of which
// want an OpenCode header on their requests.
func TestNativeAnthropicForwardLeavesOtherProvidersAlone(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := openCodeSessionTestService()
	account := &Account{
		ID: 3, Platform: PlatformMiniMax, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-test", "api_protocol": APIProtocolAnthropic},
	}

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	req, _, err := svc.buildNativeAnthropicUpstreamRequest(
		context.Background(), c, account,
		[]byte(`{"model":"MiniMax-M3","messages":[],"max_tokens":16}`),
		"sk-test", "https://api.minimaxi.com/anthropic/v1/messages",
	)
	require.NoError(t, err)
	require.Empty(t, req.Header.Get(openCodeSessionHeader))
}

func TestOpenCodeJoinsTheMultiProtocolProviderFamily(t *testing.T) {
	account := openCodeAccount(AccountModeCoding, APIProtocolAdaptive)
	require.True(t, IsCNProvider(PlatformOpenCode))
	require.True(t, account.IsCNProvider())
	require.True(t, account.IsOpenAICompatible(), "must route through the OpenAI gateway")
	require.True(t, account.SupportsNativeCNResponses(), "Grok 4.6 and GPT 5.6 Luna need /responses")
	require.True(t, account.IsHeaderOverrideEligible(), "operators need a way to pin extra headers")
	require.Equal(t, APIProtocolAdaptive, account.GetAPIProtocol())
	require.Equal(t, "sk-test", account.GetCNAPIKey())
}

// OpenCode's two plans differ in what they expose. The Go subscription serves
// usage windows at /zen/go/v1/usage under the same API key; the Zen balance is
// only readable from an authenticated browser session, so there is nothing for
// a key-holding gateway to probe. Routing a payg account into the quota path
// would send the key to an endpoint that does not exist for that plan.
func TestOpenCodeQuotaIsAvailableOnGoButNotOnZen(t *testing.T) {
	require.Equal(t, PlatformOpenCode,
		openCodeAccount(AccountModeCoding, APIProtocolAdaptive).GetCodingPlanProvider())
	require.Empty(t, openCodeAccount(AccountModePayG, APIProtocolAdaptive).GetCodingPlanProvider())
}

// Composite routing can only reach OpenCode through the explicit prefix: every
// model it resells is named after the original vendor.
func TestOpenCodeCompositeRoutingRequiresTheExplicitPrefix(t *testing.T) {
	platform, ok := DetectModelPlatform("opencode/kimi-k3")
	require.True(t, ok)
	require.Equal(t, PlatformOpenCode, platform)

	platform, ok = DetectModelPlatform("opencode-go/glm-5.3")
	require.True(t, ok)
	require.Equal(t, PlatformOpenCode, platform)

	// Bare vendor names still belong to the vendor, not to OpenCode.
	platform, ok = DetectModelPlatform("deepseek-v4-pro")
	require.True(t, ok)
	require.Equal(t, PlatformDeepseek, platform)

	require.True(t, isConcreteRequestPlatform(PlatformOpenCode))
}
