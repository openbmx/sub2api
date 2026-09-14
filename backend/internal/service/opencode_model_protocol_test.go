package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The live catalog from GET /zen/go/v1/models, captured 2026-09-14. It is the
// fixture rather than the docs because the two already disagree: nine of these
// (grok-4.5, qwen3.5-plus, deepseek-flash, kimi-k2.5, glm-5, mimo-v2-pro,
// mimo-v2-omni, hy3-preview, omen-alpha) appear on no documentation page.
// A per-model table would therefore have shipped stale, which is why the
// resolver matches families instead.
func TestResolveOpenCodeModelProtocolCoversTheLiveCatalog(t *testing.T) {
	tests := map[string]string{
		// Anthropic Messages
		"minimax-m3":    APIProtocolAnthropic,
		"minimax-m2.7":  APIProtocolAnthropic,
		"minimax-m2.5":  APIProtocolAnthropic,
		"qwen3.8-max":   APIProtocolAnthropic,
		"qwen3.8-flash": APIProtocolAnthropic,
		"qwen3.7-max":   APIProtocolAnthropic,
		"qwen3.7-plus":  APIProtocolAnthropic,
		"qwen3.6-plus":  APIProtocolAnthropic,
		"qwen3.5-plus":  APIProtocolAnthropic,

		// OpenAI Responses
		"grok-4.6":                   APIProtocolResponses,
		"grok-4.5":                   APIProtocolResponses,
		"gpt-5.6-luna":               APIProtocolResponses,
		"muse-spark-1.3-contributor": APIProtocolResponses,
		"muse-spark-1.2-contributor": APIProtocolResponses,

		// OpenAI Chat Completions
		"kimi-k3":                      APIProtocolChatCompletions,
		"kimi-k2.7-code":               APIProtocolChatCompletions,
		"kimi-k2.6":                    APIProtocolChatCompletions,
		"kimi-k2.5":                    APIProtocolChatCompletions,
		"glm-5.3-flash":                APIProtocolChatCompletions,
		"glm-5.3":                      APIProtocolChatCompletions,
		"glm-5.2":                      APIProtocolChatCompletions,
		"glm-5.1":                      APIProtocolChatCompletions,
		"glm-5":                        APIProtocolChatCompletions,
		"longcat-2.0":                  APIProtocolChatCompletions,
		"deepseek-v4-pro":              APIProtocolChatCompletions,
		"deepseek-v4-flash":            APIProtocolChatCompletions,
		"deepseek-v4.1-flash":          APIProtocolChatCompletions,
		"deepseek-v4-flash-vision-exp": APIProtocolChatCompletions,
		"deepseek-flash":               APIProtocolChatCompletions,
		"mimo-v2-pro":                  APIProtocolChatCompletions,
		"mimo-v2-omni":                 APIProtocolChatCompletions,
		"mimo-v2.5-pro":                APIProtocolChatCompletions,
		"mimo-v2.5":                    APIProtocolChatCompletions,
		"hy4-preview":                  APIProtocolChatCompletions,
		"hy3-preview":                  APIProtocolChatCompletions,
		"hy3":                          APIProtocolChatCompletions,

		// Unmapped: not on any documentation page, no recognizable family.
		"omen-alpha": APIProtocolChatCompletions,
	}
	require.Len(t, tests, 37, "fixture must stay in step with the captured catalog")

	for model, want := range tests {
		require.Equal(t, want, resolveOpenCodeModelProtocol(nil, model), "model %q", model)
	}
}

// Family matching is the whole point: a model OpenCode has not released yet
// must route correctly the day it appears, without a sub2api release.
func TestResolveOpenCodeModelProtocolGeneralizesToUnreleasedModels(t *testing.T) {
	tests := map[string]string{
		"kimi-k4":            APIProtocolChatCompletions,
		"glm-6":              APIProtocolChatCompletions,
		"deepseek-v5-pro":    APIProtocolChatCompletions,
		"minimax-m4":         APIProtocolAnthropic,
		"qwen4-max":          APIProtocolAnthropic,
		"grok-5":             APIProtocolResponses,
		"gpt-6":              APIProtocolResponses,
		"muse-spark-2.0-alt": APIProtocolResponses,
		"hy5":                APIProtocolChatCompletions,
	}
	for model, want := range tests {
		require.Equal(t, want, resolveOpenCodeModelProtocol(nil, model), "model %q", model)
	}
}

func TestResolveOpenCodeModelProtocolNormalizesTheModelID(t *testing.T) {
	// Composite routing addresses models as opencode/<id>; casing varies by client.
	for _, model := range []string{
		"opencode/minimax-m3", "opencode-go/minimax-m3", "  MiniMax-M3  ", "OPENCODE/MINIMAX-M3",
	} {
		require.Equal(t, APIProtocolAnthropic, resolveOpenCodeModelProtocol(nil, model), "model %q", model)
	}
}

// "hy" without the digit guard would swallow any future hy-prefixed family.
func TestOpenCodeHyFamilyRequiresADigit(t *testing.T) {
	require.Equal(t, APIProtocolChatCompletions, resolveOpenCodeModelProtocol(nil, "hy3"))

	// Not the hy3/hy4 family; falls through to the unknown default rather than
	// being claimed by the prefix.
	account := openCodeAccount(AccountModeCoding, APIProtocolAdaptive)
	account.Credentials[credKeyOpenCodeModelProtocols] = map[string]any{"hyena-": APIProtocolAnthropic}
	require.Equal(t, APIProtocolAnthropic, resolveOpenCodeModelProtocol(account, "hyena-x"))
}

// minimax- and mimo- share a two-letter head; neither may claim the other.
func TestOpenCodeSimilarPrefixesDoNotCollide(t *testing.T) {
	require.Equal(t, APIProtocolAnthropic, resolveOpenCodeModelProtocol(nil, "minimax-m3"))
	require.Equal(t, APIProtocolChatCompletions, resolveOpenCodeModelProtocol(nil, "mimo-v2.5"))
}

func TestOpenCodeModelProtocolOverridesWinOverTheBuiltInTable(t *testing.T) {
	account := openCodeAccount(AccountModeCoding, APIProtocolAdaptive)
	account.Credentials[credKeyOpenCodeModelProtocols] = map[string]any{
		// A brand-new family the built-in table has never heard of.
		"omen-": APIProtocolResponses,
		// Correcting a built-in guess that turned out wrong.
		"kimi-": APIProtocolAnthropic,
	}
	require.Equal(t, APIProtocolResponses, resolveOpenCodeModelProtocol(account, "omen-alpha"))
	require.Equal(t, APIProtocolAnthropic, resolveOpenCodeModelProtocol(account, "kimi-k3"))
	// Untouched families keep the built-in mapping.
	require.Equal(t, APIProtocolChatCompletions, resolveOpenCodeModelProtocol(account, "glm-5.3"))
}

// The forward path must never reject traffic over a malformed config entry;
// the save path is where bad input gets reported.
func TestOpenCodeModelProtocolOverridesDropGarbageOnTheForwardPath(t *testing.T) {
	account := openCodeAccount(AccountModeCoding, APIProtocolAdaptive)
	account.Credentials[credKeyOpenCodeModelProtocols] = map[string]any{
		"omen-":  "not-a-protocol",
		"  ":     APIProtocolAnthropic,
		"other-": 42,
		// adaptive is a routing policy, not a destination — accepting it here
		// would make the resolver point at itself.
		"third-": APIProtocolAdaptive,
	}
	require.Equal(t, APIProtocolChatCompletions, resolveOpenCodeModelProtocol(account, "omen-alpha"))
	require.Equal(t, APIProtocolChatCompletions, resolveOpenCodeModelProtocol(account, "third-x"))
}

func TestNormalizeOpenCodeModelProtocolOverridesReportsBadInput(t *testing.T) {
	ok, err := NormalizeOpenCodeModelProtocolOverrides(map[string]string{
		"  Omen-  ": "ANTHROPIC",
		"":          APIProtocolResponses,
	})
	require.NoError(t, err)
	require.Equal(t, map[string]string{"omen-": APIProtocolAnthropic}, ok, "blank rows drop, values normalize")

	_, err = NormalizeOpenCodeModelProtocolOverrides(map[string]string{"omen-": "adaptive"})
	require.Error(t, err, "adaptive is not a destination protocol")

	_, err = NormalizeOpenCodeModelProtocolOverrides(map[string]string{"omen-": "nonsense"})
	require.Error(t, err)

	tooMany := make(map[string]string, MaxOpenCodeModelProtocolOverrides+1)
	for i := 0; i <= MaxOpenCodeModelProtocolOverrides; i++ {
		tooMany[string(rune('a'+i))+"-"] = APIProtocolChatCompletions
	}
	_, err = NormalizeOpenCodeModelProtocolOverrides(tooMany)
	require.Error(t, err)

	nilResult, err := NormalizeOpenCodeModelProtocolOverrides(nil)
	require.NoError(t, err)
	require.Nil(t, nilResult, "omitted means keep stored, not clear")
}
