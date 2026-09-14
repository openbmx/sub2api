package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func openCodeProtocolTestService() *OpenAIGatewayService { return &OpenAIGatewayService{} }

// The load-bearing guarantee of this feature: no other platform pays a copy, a
// lookup, or a behavior change for it. Same pointer in, same pointer out.
func TestResolveOpenCodeRequestAccountLeavesOtherAccountsUntouched(t *testing.T) {
	svc := openCodeProtocolTestService()
	body := []byte(`{"model":"minimax-m3","messages":[]}`)

	for _, platform := range []string{
		PlatformMiniMax, PlatformKimi, PlatformZhipu, PlatformDeepseek,
		PlatformOpenAI, PlatformAnthropic, PlatformGrok, PlatformGemini,
	} {
		account := &Account{
			ID: 1, Platform: platform, Type: AccountTypeAPIKey,
			Credentials: map[string]any{"api_key": "sk", "api_protocol": APIProtocolAdaptive},
		}
		require.Same(t, account, svc.resolveOpenCodeRequestAccount(account, body, ""), "platform %q", platform)
	}

	require.Nil(t, svc.resolveOpenCodeRequestAccount(nil, body, ""))
}

// An explicitly pinned protocol is the operator overriding this resolution on
// purpose; resolving anyway would silently ignore their choice.
func TestResolveOpenCodeRequestAccountRespectsAnExplicitProtocol(t *testing.T) {
	svc := openCodeProtocolTestService()
	body := []byte(`{"model":"minimax-m3","messages":[]}`)

	for _, protocol := range []string{
		APIProtocolChatCompletions, APIProtocolAnthropic, APIProtocolResponses,
	} {
		account := openCodeAccount(AccountModeCoding, protocol)
		require.Same(t, account, svc.resolveOpenCodeRequestAccount(account, body, ""), "protocol %q", protocol)
	}
}

// The point of the whole change: one adaptive account, three models, three
// different upstream contracts — regardless of which endpoint the client called.
func TestResolveOpenCodeRequestAccountPinsProtocolPerModel(t *testing.T) {
	svc := openCodeProtocolTestService()
	tests := []struct {
		model     string
		protocol  string
		baseURL   string
		anthropic bool
	}{
		{model: "kimi-k3", protocol: APIProtocolChatCompletions, baseURL: DefaultOpenCodeGoBaseURL},
		{model: "minimax-m3", protocol: APIProtocolAnthropic, baseURL: DefaultOpenCodeGoAnthropicBaseURL, anthropic: true},
		{model: "grok-4.6", protocol: APIProtocolResponses, baseURL: DefaultOpenCodeGoBaseURL},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			account := openCodeAccount(AccountModeCoding, APIProtocolAdaptive)
			resolved := svc.resolveOpenCodeRequestAccount(account,
				[]byte(`{"model":"`+tt.model+`","messages":[]}`), "")

			require.NotSame(t, account, resolved)
			require.Equal(t, tt.protocol, resolved.GetAPIProtocol())
			require.False(t, resolved.IsAdaptiveAPIProtocol(), "routing must see a concrete protocol")
			require.Equal(t, tt.anthropic, resolved.IsAnthropicProtocol())

			if tt.anthropic {
				require.Equal(t, tt.baseURL, resolved.GetAnthropicProtocolBaseURL())
			} else {
				require.Equal(t, tt.baseURL, resolved.GetOpenAIBaseURL())
			}

			// The original must be untouched: it is shared with whatever else
			// holds this account.
			require.Equal(t, APIProtocolAdaptive, account.GetAPIProtocol())
		})
	}
}

// Operators can point the three endpoints at a relay. Rewriting the protocol
// must not silently fall back to the platform defaults and drop that.
func TestResolveOpenCodeRequestAccountKeepsCustomEndpoints(t *testing.T) {
	svc := openCodeProtocolTestService()
	account := openCodeAccount(AccountModeCoding, APIProtocolAdaptive)
	account.Credentials["api_base_urls"] = map[string]any{
		APIProtocolChatCompletions: "https://relay.example.com/cc",
		APIProtocolAnthropic:       "https://relay.example.com/anthropic",
		APIProtocolResponses:       "https://relay.example.com/resp",
	}

	cc := svc.resolveOpenCodeRequestAccount(account, []byte(`{"model":"kimi-k3"}`), "")
	require.Equal(t, "https://relay.example.com/cc", cc.GetOpenAIBaseURL())

	anthropic := svc.resolveOpenCodeRequestAccount(account, []byte(`{"model":"minimax-m3"}`), "")
	require.Equal(t, "https://relay.example.com/anthropic", anthropic.GetAnthropicProtocolBaseURL())

	responses := svc.resolveOpenCodeRequestAccount(account, []byte(`{"model":"grok-4.6"}`), "")
	require.Equal(t, "https://relay.example.com/resp", responses.GetCNProtocolBaseURL(APIProtocolResponses))
}

// The copy shares the Credentials map precisely so the model_mapping and
// header_override hot-path caches stay valid. Rebuilding that map per request
// would re-parse both JSON blobs on every forward.
func TestResolveOpenCodeRequestAccountSharesCredentialsToKeepCachesWarm(t *testing.T) {
	svc := openCodeProtocolTestService()
	account := openCodeAccount(AccountModeCoding, APIProtocolAdaptive)
	account.Credentials["model_mapping"] = map[string]any{"gpt-4o": "kimi-k3"}

	// Prime the cache on the original.
	require.Equal(t, "kimi-k3", account.GetMappedModel("gpt-4o"))
	require.True(t, account.modelMappingCacheReady)

	resolved := svc.resolveOpenCodeRequestAccount(account, []byte(`{"model":"gpt-4o"}`), "")
	require.NotSame(t, account, resolved)
	require.True(t, resolved.modelMappingCacheReady, "cache must survive the copy")
	require.Equal(t, "kimi-k3", resolved.GetMappedModel("gpt-4o"))
}

// Model mapping runs before family resolution, so an operator remapping an
// inbound name onto an OpenCode model still gets the right endpoint.
func TestResolveOpenCodeRequestAccountResolvesTheMappedModel(t *testing.T) {
	svc := openCodeProtocolTestService()
	account := openCodeAccount(AccountModeCoding, APIProtocolAdaptive)
	account.Credentials["model_mapping"] = map[string]any{"claude-sonnet-4-6": "minimax-m3"}

	resolved := svc.resolveOpenCodeRequestAccount(account, []byte(`{"model":"claude-sonnet-4-6"}`), "")
	require.Equal(t, APIProtocolAnthropic, resolved.GetAPIProtocol(),
		"the upstream sees minimax-m3, so the Anthropic endpoint is the correct one")
}

// A body with no model at all (probes, malformed input) must not be rewritten
// into an arbitrary protocol.
func TestResolveOpenCodeRequestAccountFallsBackWhenNoModelIsPresent(t *testing.T) {
	svc := openCodeProtocolTestService()
	account := openCodeAccount(AccountModeCoding, APIProtocolAdaptive)

	require.Same(t, account, svc.resolveOpenCodeRequestAccount(account, []byte(`{}`), ""))
	require.Same(t, account, svc.resolveOpenCodeRequestAccount(account, nil, ""))

	// The caller's already-mapped model is used when the body has none.
	resolved := svc.resolveOpenCodeRequestAccount(account, []byte(`{}`), "minimax-m3")
	require.Equal(t, APIProtocolAnthropic, resolved.GetAPIProtocol())
}
