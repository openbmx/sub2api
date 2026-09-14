package securityaudit

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
	"unicode/utf8"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const (
	// MaxCustomHeaders bounds what one node may add. The limit only exists so a
	// config cannot grow without end; no real upstream needs more than a couple.
	MaxCustomHeaders = 16
	// MaxCustomHeaderNameBytes and MaxCustomHeaderValueRunes keep a single header
	// from dominating the stored config.
	MaxCustomHeaderNameBytes  = 128
	MaxCustomHeaderValueRunes = 1024
)

// openCodeSessionHeaderName is the routing header OpenCode requires. Announced
// 2026-09-03 and enforced from 2026-09-06: requests without it are rejected with
// a 400 MissingSessionID, which the scanner surfaces as prompt_guard_unavailable.
// It is unrelated to the gateway's forwarding of a caller-owned session value —
// an audit call has no caller to forward from, so the value is derived below.
const openCodeSessionHeaderName = "X-Opencode-Session"

// reservedCustomHeaders may not be set per node. These either frame the request
// (Host, Content-Length, Transfer-Encoding) or are managed by the transport
// (Connection and friends); overriding any of them breaks the call rather than
// customizing it. Content-Type is reserved because the audit body is always
// JSON. Authorization is deliberately absent: a gateway that wants a different
// auth scheme is exactly what custom headers are for.
var reservedCustomHeaders = map[string]struct{}{
	"Host": {}, "Content-Length": {}, "Content-Type": {}, "Transfer-Encoding": {},
	"Connection": {}, "Keep-Alive": {}, "Proxy-Connection": {}, "Proxy-Authorization": {},
	"Upgrade": {}, "Te": {}, "Trailer": {}, "Expect": {},
}

// applyEndpointHeaders stamps the outbound headers for one audit call: the node
// credential first, then the operator's custom headers, then any header the
// destination itself requires. Content-Type is left to the caller because the
// probe is a bodyless GET.
//
// Ordering is deliberate. Custom headers run after the credential so an operator
// who sets Authorization by hand actually wins, and the upstream-specific fill
// runs last but only for keys nobody set, so an explicit per-node value is never
// overwritten by a derived one.
func applyEndpointHeaders(header http.Header, endpoint ActiveEndpoint) {
	if header == nil {
		return
	}
	if endpoint.Token != "" {
		header.Set("Authorization", "Bearer "+endpoint.Token)
	}
	for name, value := range endpoint.Headers {
		canonical := textproto.CanonicalMIMEHeaderKey(strings.TrimSpace(name))
		if canonical == "" || !isValidHeaderName(canonical) {
			continue
		}
		if _, reserved := reservedCustomHeaders[canonical]; reserved {
			continue
		}
		header.Set(canonical, value)
	}
	if requiresOpenCodeSession(endpoint.BaseURL) && header.Get(openCodeSessionHeaderName) == "" {
		header.Set(openCodeSessionHeaderName, auditSessionID(endpoint.ID))
	}
}

// requiresOpenCodeSession reports whether the destination is OpenCode. The gate
// is on the host alone, so every other audit upstream — local vLLM, SiliconFlow,
// DashScope, OpenRouter, anything OpenAI-compatible — sees byte-identical
// requests to what it saw before this existed.
func requiresOpenCodeSession(baseURL string) bool {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	return host == "opencode.ai" || strings.HasSuffix(host, ".opencode.ai")
}

// auditSessionID derives the "stable per-conversation ID" OpenCode asks for. An
// audit call has no conversation, so the node is the unit: every call to one
// node shares an ID, which keeps that node's traffic on one upstream route and
// lets the unchanging guard prompt stay cached. It is a pure function of the
// node ID — no clock, no randomness — so it survives restarts and is identical
// across replicas of the same deployment. The ID is hashed rather than passed
// through so an operator-chosen node name never reaches the upstream.
func auditSessionID(endpointID string) string {
	digest := sha256.Sum256([]byte("sub2api/prompt-audit/session/" + strings.TrimSpace(endpointID)))
	return "sub2api-audit-" + hex.EncodeToString(digest[:12])
}

// normalizeCustomHeaders validates and canonicalizes an operator's header map.
// A nil map is returned as nil so callers can tell "field omitted, keep what is
// stored" from an empty map, which is an explicit "remove them all".
func normalizeCustomHeaders(raw map[string]string) (map[string]string, error) {
	if raw == nil {
		return nil, nil
	}
	if len(raw) > MaxCustomHeaders {
		return nil, infraerrors.BadRequest(ErrorCodeTooManyHeaders, "审计节点自定义请求头数量超出上限")
	}
	result := make(map[string]string, len(raw))
	for name, value := range raw {
		trimmed := strings.TrimSpace(name)
		// A blank row is what an admin form leaves behind after clearing a field,
		// not something to reject.
		if trimmed == "" {
			continue
		}
		if !isValidHeaderName(trimmed) {
			return nil, infraerrors.BadRequest(ErrorCodeInvalidHeader, "审计节点自定义请求头名称无效："+trimmed)
		}
		canonical := textproto.CanonicalMIMEHeaderKey(trimmed)
		if _, reserved := reservedCustomHeaders[canonical]; reserved {
			return nil, infraerrors.BadRequest(ErrorCodeInvalidHeader, "该请求头由系统管理，不能自定义："+canonical)
		}
		if _, duplicate := result[canonical]; duplicate {
			return nil, infraerrors.BadRequest(ErrorCodeInvalidHeader, "审计节点自定义请求头名称重复："+canonical)
		}
		value = strings.TrimSpace(value)
		// CR, LF and NUL in a value are header injection, not configuration.
		if strings.ContainsAny(value, "\r\n\x00") {
			return nil, infraerrors.BadRequest(ErrorCodeInvalidHeader, "审计节点自定义请求头值不能包含换行："+canonical)
		}
		if utf8.RuneCountInString(value) > MaxCustomHeaderValueRunes {
			return nil, infraerrors.BadRequest(ErrorCodeInvalidHeader, "审计节点自定义请求头值过长："+canonical)
		}
		result[canonical] = value
	}
	return result, nil
}

// sanitizeCustomHeaders is the non-failing counterpart used when loading stored
// config. Every save goes through normalizeCustomHeaders, so in practice there
// is nothing to drop; this exists so a config that somehow contains a bad header
// — hand-edited storage, a downgrade — loses that one header instead of failing
// to load and taking the whole audit system down with it.
func sanitizeCustomHeaders(raw map[string]string) map[string]string {
	if raw == nil {
		return nil
	}
	result := make(map[string]string, len(raw))
	for name, value := range raw {
		trimmed := strings.TrimSpace(name)
		if !isValidHeaderName(trimmed) {
			continue
		}
		canonical := textproto.CanonicalMIMEHeaderKey(trimmed)
		if _, reserved := reservedCustomHeaders[canonical]; reserved {
			continue
		}
		value = strings.TrimSpace(value)
		if strings.ContainsAny(value, "\r\n\x00") || utf8.RuneCountInString(value) > MaxCustomHeaderValueRunes {
			continue
		}
		if len(result) >= MaxCustomHeaders {
			break
		}
		result[canonical] = value
	}
	return result
}

// isValidHeaderName checks the RFC 7230 token production. http.Header.Set would
// happily store a name with a space or colon in it and produce a request the
// upstream rejects for reasons that have nothing to do with the audit.
func isValidHeaderName(name string) bool {
	if name == "" || len(name) > MaxCustomHeaderNameBytes {
		return false
	}
	for i := 0; i < len(name); i++ {
		if !isHeaderTokenChar(name[i]) {
			return false
		}
	}
	return true
}

func isHeaderTokenChar(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	}
	return strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0
}

// cloneCustomHeaders copies a header map so runtime snapshots and stored config
// never share one. Nil stays nil, preserving the omitted/cleared distinction.
func cloneCustomHeaders(headers map[string]string) map[string]string {
	if headers == nil {
		return nil
	}
	result := make(map[string]string, len(headers))
	for name, value := range headers {
		result[name] = value
	}
	return result
}
