package service

import (
	"net/http"
	"strings"
	"sync"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

// OpenCode picks the upstream contract per model, not per client. Every other
// provider in the IsCNProvider family serves all of its models on all of its
// endpoints, which is what `adaptive` was built for: follow the inbound shape.
// Applying that rule to OpenCode silently narrows an account to whichever third
// of the catalog happens to match the caller's protocol — a Claude Code user
// gets the Anthropic models and a 400 for everything else.
//
// The mapping is by family prefix rather than by model id on purpose. The live
// catalog already carries nine models the docs never mentioned (grok-4.5,
// qwen3.5-plus, deepseek-flash, kimi-k2.5, glm-5, mimo-v2-pro, mimo-v2-omni,
// hy3-preview, omen-alpha), so an id table would have shipped stale. A family
// table makes a future kimi-k4 work on day one.

const credKeyOpenCodeModelProtocols = "opencode_model_protocols"

// MaxOpenCodeModelProtocolOverrides bounds the operator-supplied table. The
// limit only exists so a config cannot grow without end.
const MaxOpenCodeModelProtocolOverrides = 32

type openCodeModelFamily struct {
	prefix   string
	protocol string
	// digitSuffix requires a digit right after the prefix. "hy" alone would
	// swallow any future hy-prefixed family name; "hy" + digit matches hy3 and
	// hy4-preview and nothing else.
	digitSuffix bool
}

// openCodeModelFamilies is consulted in order. No entry may be a prefix of an
// earlier one, so ordering is documentation rather than precedence.
//
//nolint:gochecknoglobals // 只读静态表，初始化后不变更。
var openCodeModelFamilies = []openCodeModelFamily{
	// Anthropic Messages
	{prefix: "minimax-", protocol: APIProtocolAnthropic},
	{prefix: "qwen", protocol: APIProtocolAnthropic},
	// OpenAI Responses
	{prefix: "grok-", protocol: APIProtocolResponses},
	{prefix: "gpt-", protocol: APIProtocolResponses},
	{prefix: "muse-spark-", protocol: APIProtocolResponses},
	// OpenAI Chat Completions
	{prefix: "glm-", protocol: APIProtocolChatCompletions},
	{prefix: "kimi-", protocol: APIProtocolChatCompletions},
	{prefix: "deepseek-", protocol: APIProtocolChatCompletions},
	{prefix: "mimo-", protocol: APIProtocolChatCompletions},
	{prefix: "longcat-", protocol: APIProtocolChatCompletions},
	{prefix: "hy", protocol: APIProtocolChatCompletions, digitSuffix: true},
}

// unknownOpenCodeModelsLogged dedupes the warn below. Without it a single
// unmapped model would log on every request.
//
//nolint:gochecknoglobals // 进程内去重集合，仅用于日志降噪。
var unknownOpenCodeModelsLogged sync.Map

// resolveOpenCodeModelProtocol maps a model to the upstream contract OpenCode
// serves it on. Operator overrides win over the built-in table so a brand-new
// family can be handled without waiting for a release, and so a wrong built-in
// guess can be corrected in place.
//
// An unrecognized model falls to Chat Completions: it is the largest of the
// three groups, so a new model is most likely to belong there. The miss is
// logged once per model name — that log is the operator's cue to add an
// override.
func resolveOpenCodeModelProtocol(account *Account, model string) string {
	normalized := normalizeOpenCodeModelID(model)
	if normalized == "" {
		return APIProtocolChatCompletions
	}

	for prefix, protocol := range openCodeModelProtocolOverrides(account) {
		if strings.HasPrefix(normalized, prefix) {
			return protocol
		}
	}

	for _, family := range openCodeModelFamilies {
		if !strings.HasPrefix(normalized, family.prefix) {
			continue
		}
		if family.digitSuffix && !startsWithDigit(normalized[len(family.prefix):]) {
			continue
		}
		return family.protocol
	}

	if _, seen := unknownOpenCodeModelsLogged.LoadOrStore(normalized, struct{}{}); !seen {
		logger.L().Warn("opencode model family not recognized, defaulting to chat completions",
			zap.String("model", normalized),
			zap.String("hint", "add a prefix to the account's opencode_model_protocols override if this is wrong"),
		)
	}
	return APIProtocolChatCompletions
}

// resolveOpenCodeRequestAccount returns the account this request should be
// forwarded with. For an OpenCode account left on `adaptive` it pins the
// protocol and endpoint to whatever the requested model actually needs; for
// everything else it returns the caller's pointer untouched.
//
// Returning the same pointer matters: it is the guarantee that no other
// platform pays a copy, a lookup, or a behavior change for this feature.
func (s *OpenAIGatewayService) resolveOpenCodeRequestAccount(account *Account, body []byte, fallbackModel string) *Account {
	if account == nil || account.Platform != PlatformOpenCode {
		return account
	}
	// An explicitly pinned protocol is the operator overriding this resolution
	// on purpose — for instance to force every model through one contract.
	if !account.IsAdaptiveAPIProtocol() {
		return account
	}

	model := strings.TrimSpace(gjson.GetBytes(body, "model").String())
	if model == "" {
		model = strings.TrimSpace(fallbackModel)
	}
	if model == "" {
		return account
	}

	protocol := resolveOpenCodeModelProtocol(account, account.GetMappedModel(model))
	// GetCNProtocolBaseURL is read before the override is attached, so it still
	// takes the adaptive path and honours any per-protocol endpoint the operator
	// configured under api_base_urls.
	return account.withResolvedUpstreamProtocol(protocol, account.GetCNProtocolBaseURL(protocol))
}

// normalizeOpenCodeModelID strips the composite routing prefix and lowercases,
// so "OpenCode/Kimi-K3" and "kimi-k3" resolve identically.
func normalizeOpenCodeModelID(model string) string {
	normalized := strings.ToLower(strings.TrimSpace(model))
	for _, prefix := range []string{"opencode-go/", "opencode/"} {
		if strings.HasPrefix(normalized, prefix) {
			normalized = strings.TrimPrefix(normalized, prefix)
			break
		}
	}
	return strings.TrimSpace(normalized)
}

func startsWithDigit(s string) bool {
	return s != "" && s[0] >= '0' && s[0] <= '9'
}

// openCodeModelProtocolOverrides reads the operator's prefix→protocol table.
// Invalid entries are dropped rather than failing the request: this runs on the
// forward path, where rejecting traffic over a malformed config entry would be
// a worse outcome than ignoring it. The save path validates and reports.
func openCodeModelProtocolOverrides(account *Account) map[string]string {
	if account == nil {
		return nil
	}
	raw, ok := account.Credentials[credKeyOpenCodeModelProtocols].(map[string]any)
	if !ok || len(raw) == 0 {
		return nil
	}
	result := make(map[string]string, len(raw))
	for prefix, value := range raw {
		normalizedPrefix := normalizeOpenCodeModelID(prefix)
		protocol, _ := value.(string)
		protocol = strings.TrimSpace(strings.ToLower(protocol))
		if normalizedPrefix == "" || !isOpenCodeNativeProtocol(protocol) {
			continue
		}
		result[normalizedPrefix] = protocol
	}
	return result
}

// isOpenCodeNativeProtocol accepts only the three contracts OpenCode actually
// serves. "adaptive" is excluded on purpose — it is a routing policy, not a
// destination, and accepting it here would produce an infinite indirection.
func isOpenCodeNativeProtocol(protocol string) bool {
	switch protocol {
	case APIProtocolChatCompletions, APIProtocolAnthropic, APIProtocolResponses:
		return true
	default:
		return false
	}
}

// NormalizeOpenCodeModelProtocolOverrides validates an operator-supplied table
// for the save path. Unlike the forward-path reader above, this reports what is
// wrong instead of silently dropping it.
func NormalizeOpenCodeModelProtocolOverrides(raw map[string]string) (map[string]string, error) {
	if raw == nil {
		return nil, nil
	}
	if len(raw) > MaxOpenCodeModelProtocolOverrides {
		return nil, infraerrors.Newf(http.StatusBadRequest, "INVALID_OPENCODE_MODEL_PROTOCOLS",
			"opencode_model_protocols supports at most %d entries", MaxOpenCodeModelProtocolOverrides)
	}
	result := make(map[string]string, len(raw))
	for prefix, protocol := range raw {
		normalizedPrefix := normalizeOpenCodeModelID(prefix)
		// A blank row is what an admin form leaves behind after clearing a
		// field, not something to reject.
		if normalizedPrefix == "" {
			continue
		}
		normalizedProtocol := strings.TrimSpace(strings.ToLower(protocol))
		if !isOpenCodeNativeProtocol(normalizedProtocol) {
			return nil, infraerrors.Newf(http.StatusBadRequest, "INVALID_OPENCODE_MODEL_PROTOCOLS",
				"model prefix %q maps to unsupported protocol %q", normalizedPrefix, protocol)
		}
		result[normalizedPrefix] = normalizedProtocol
	}
	return result, nil
}
