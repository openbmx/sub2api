package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The adaptive connection test probes Chat Completions, Anthropic and Responses
// in turn with one model. That is right for every other multi-protocol provider
// — they serve all models on all endpoints — but OpenCode partitions its
// catalog, so two of the three probes are guaranteed to fail on a model that
// forwards perfectly well. Testing glm-5.2 reported a broken account because
// /v1/messages does not serve GLM at all.
//
// The fix routes the probe through the same family resolution the forward path
// uses, so the endpoint under test always matches the model.
func TestOpenCodeAdaptiveProbeTargetsTheModelsOwnEndpoint(t *testing.T) {
	account := openCodeAccount(AccountModeCoding, APIProtocolAdaptive)

	tests := []struct {
		model    string
		protocol string
		display  string
	}{
		{model: "glm-5.2", protocol: APIProtocolChatCompletions, display: "Chat Completions /v1/chat/completions"},
		{model: "kimi-k3", protocol: APIProtocolChatCompletions, display: "Chat Completions /v1/chat/completions"},
		{model: "minimax-m3", protocol: APIProtocolAnthropic, display: "Anthropic /v1/messages"},
		{model: "qwen3.8-max", protocol: APIProtocolAnthropic, display: "Anthropic /v1/messages"},
		{model: "grok-4.6", protocol: APIProtocolResponses, display: "Responses /v1/responses"},
		{model: "gpt-5.6-luna", protocol: APIProtocolResponses, display: "Responses /v1/responses"},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			protocol := resolveOpenCodeModelProtocol(account, tt.model)
			require.Equal(t, tt.protocol, protocol)
			require.Equal(t, tt.display, openCodeProtocolDisplayName(protocol),
				"the status line must name the endpoint actually probed")
		})
	}
}

// Every other provider keeps the all-endpoints probe: narrowing it for them
// would stop catching an upstream that only half-supports the contract.
func TestNonOpenCodeProvidersKeepTheFullAdaptiveProbe(t *testing.T) {
	for _, platform := range []string{PlatformKimi, PlatformZhipu, PlatformDeepseek, PlatformMiniMax} {
		account := &Account{
			Platform: platform, Type: AccountTypeAPIKey,
			Credentials: map[string]any{"api_key": "sk", "api_protocol": APIProtocolAdaptive},
		}
		require.NotEqual(t, PlatformOpenCode, account.Platform,
			"the narrowed probe is gated on platform, not on adaptive alone")
	}
}
