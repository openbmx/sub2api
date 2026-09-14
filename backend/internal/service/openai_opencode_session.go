package service

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/gin-gonic/gin"
)

const openCodeSessionHeader = "X-OpenCode-Session"

// openCodeFallbackUserAgent is what goes out when nothing else has set an agent
// string. OpenCode asks clients to identify with a specific agent rather than a
// bare SDK or HTTP-library default, and singles out "Go HTTP client" traffic as
// the problem case — which is exactly what net/http sends when the header is
// left unset.
//
// Claude Code's identity is used rather than a sub2api-specific token because
// OpenCode recognizes Claude Code as a first-class client (its docs list it
// among the clients whose native session headers Go already understands), so an
// unrecognized product token is the one thing likely to get filtered. Derived
// from the same CLIVersion() source as the Anthropic path so the two can never
// drift apart.
func openCodeFallbackUserAgent() string {
	return claude.DefaultHeaders["User-Agent"]
}

// applyOpenCodeSessionHeader stamps the conversation identifier OpenCode requires.
// Requests without it are rejected with 400 MissingSessionID (enforced from
// 2026-09-06), so this is not an optimization — it is what makes the upstream
// usable at all.
//
// The caller applies this after account header overrides so a per-conversation
// value cannot be replaced by a fixed account-wide override.
func applyOpenCodeSessionHeader(c *gin.Context, account *Account, targetURL string, headers http.Header, body []byte) {
	if c == nil || c.Request == nil || account == nil || account.Type != AccountTypeAPIKey || headers == nil {
		return
	}

	parsed, err := url.Parse(targetURL)
	if err != nil || !strings.EqualFold(parsed.Scheme, "https") || !strings.EqualFold(parsed.Hostname(), "opencode.ai") {
		return
	}

	sessionID, isFallback := resolveOpenCodeSessionID(c, account, body)
	if sessionID == "" {
		return
	}

	// A fixed account-level override is an explicit operator choice. It still
	// loses to a real conversation identity — that is the whole point of the
	// header — but the coarse per-key fallback must not clobber it.
	if isFallback && existingOpenCodeSessionHeader(headers) != "" {
		applyOpenCodeUserAgent(headers)
		return
	}

	for key := range headers {
		if strings.EqualFold(key, openCodeSessionHeader) {
			delete(headers, key)
		}
	}
	headers.Set(openCodeSessionHeader, sessionID)
	applyOpenCodeUserAgent(headers)
}

// existingOpenCodeSessionHeader reads the header under any casing, because an
// account override stores whatever key the operator typed.
func existingOpenCodeSessionHeader(headers http.Header) string {
	for key, values := range headers {
		if !strings.EqualFold(key, openCodeSessionHeader) {
			continue
		}
		for _, value := range values {
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}

// applyOpenCodeUserAgent only fills a gap. A real client's agent string is left
// alone, both because it is more informative upstream and because OpenCode
// recognizes some clients (Claude Code, Codex, ZCode) by their own identity.
func applyOpenCodeUserAgent(headers http.Header) {
	if strings.TrimSpace(headers.Get("User-Agent")) != "" {
		return
	}
	// Guard the lookup: setting an empty User-Agent would be worse than leaving
	// the header off, because net/http only substitutes its own default when the
	// key is absent rather than present-and-blank.
	if fallback := strings.TrimSpace(openCodeFallbackUserAgent()); fallback != "" {
		headers.Set("User-Agent", fallback)
	}
}

// resolveOpenCodeSessionID picks the most conversation-specific identifier
// available, in three tiers. The bool reports whether the result came from the
// last-resort tier, which callers treat as fill-only.
//
// Granularity is worth the trouble: OpenCode uses this value for routing
// affinity and prompt caching, and a cached read costs roughly a fiftieth of an
// uncached one. Collapsing every request onto one ID would work — and quietly
// pay full price for every turn.
func resolveOpenCodeSessionID(c *gin.Context, account *Account, body []byte) (string, bool) {
	// 1. The caller addressed OpenCode directly. Pass it through untouched: it is
	// their conversation identity and they chose the exact value.
	if sessionID := strings.TrimSpace(c.GetHeader(openCodeSessionHeader)); sessionID != "" {
		return sessionID, false
	}

	// 2. Any other session identity this gateway already recognizes — the
	// session/conversation headers Claude Code, Codex and ZCode send, or
	// prompt_cache_key in the body. Hashed rather than forwarded: the client did
	// not choose to disclose these to OpenCode, and an opaque stable token serves
	// the upstream's purpose just as well.
	if sessionID := strings.TrimSpace(explicitOpenAISessionID(c, body)); sessionID != "" {
		return derivedOpenCodeSessionID("conversation", sessionID), false
	}

	// 3. Nothing conversation-scoped at all, which is the ordinary case for a
	// plain OpenAI SDK client. Fall back to the calling key and account so the
	// request is still routable. This is the coarsest tier — every conversation
	// through one key shares it — but the alternative is a 400.
	return derivedOpenCodeSessionID("apikey",
		strconv.FormatInt(getAPIKeyIDFromContext(c), 10)+"/"+strconv.FormatInt(account.ID, 10)), true
}

// derivedOpenCodeSessionID hashes an internal identifier into a stable opaque
// token. No clock and no randomness, so the same conversation keeps the same ID
// across restarts and across replicas; hashed so internal key and account IDs
// never reach the upstream in the clear.
func derivedOpenCodeSessionID(scope, value string) string {
	digest := sha256.Sum256([]byte("sub2api/opencode/" + scope + "/" + value))
	return "sub2api-" + scope + "-" + hex.EncodeToString(digest[:12])
}
