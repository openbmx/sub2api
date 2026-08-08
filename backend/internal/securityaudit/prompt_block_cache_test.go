package securityaudit

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type stubClock struct{ now time.Time }

func (c *stubClock) Now() time.Time { return c.now }

func blockResult() *NormalizedResult {
	return &NormalizedResult{
		Decision: EventCritical, RiskLevel: RiskCritical, Action: ActionBlock,
		Categories: []string{"jailbreak"}, MatchedScanners: []string{"jailbreak"},
		ScannerScores: map[string]float64{"jailbreak": 0.9}, GuardEndpointID: "dn1",
	}
}

// The cache exists because an audit model can score the same prompt either side
// of the threshold on consecutive calls, so a client that retries walks past a
// block. These cover the properties that behaviour depends on.
func TestBlockVerdictCacheReplaysWithinTTL(t *testing.T) {
	clock := &stubClock{now: time.Unix(1_700_000_000, 0)}
	cache := newBlockVerdictCache(time.Minute, 16, clock)

	_, ok := cache.get(7, "hash-a")
	require.False(t, ok, "empty cache must miss")

	cache.put(7, "hash-a", blockResult())
	got, ok := cache.get(7, "hash-a")
	require.True(t, ok, "a stored block must replay")
	require.Equal(t, ActionBlock, got.Action)
	require.Equal(t, []string{"jailbreak"}, got.Categories)

	clock.now = clock.now.Add(61 * time.Second)
	_, ok = cache.get(7, "hash-a")
	require.False(t, ok, "entry must expire once the TTL elapses")
}

func TestBlockVerdictCacheIsScopedToConfigVersion(t *testing.T) {
	clock := &stubClock{now: time.Unix(1_700_000_000, 0)}
	cache := newBlockVerdictCache(time.Minute, 16, clock)
	cache.put(7, "hash-a", blockResult())

	_, ok := cache.get(8, "hash-a")
	require.False(t, ok, "editing the policy must retire cached blocks")
}

func TestBlockVerdictCacheIgnoresEmptyHash(t *testing.T) {
	cache := newBlockVerdictCache(time.Minute, 16, &stubClock{now: time.Unix(1_700_000_000, 0)})
	cache.put(7, "", blockResult())
	_, ok := cache.get(7, "")
	require.False(t, ok, "a prompt with no hash must not collide with every other one")
}

// A replayed verdict is handed to the guard, which stamps latency onto it. If
// the cache returned a shared pointer the second replay would inherit the
// first one's numbers.
func TestBlockVerdictCacheReturnsIndependentCopies(t *testing.T) {
	cache := newBlockVerdictCache(time.Minute, 16, &stubClock{now: time.Unix(1_700_000_000, 0)})
	cache.put(7, "hash-a", blockResult())

	first, ok := cache.get(7, "hash-a")
	require.True(t, ok)
	first.LatencyMS = 999
	first.Categories[0] = "mutated"
	first.ScannerScores["jailbreak"] = 0.1

	second, ok := cache.get(7, "hash-a")
	require.True(t, ok)
	require.Zero(t, second.LatencyMS)
	require.Equal(t, []string{"jailbreak"}, second.Categories)
	require.InDelta(t, 0.9, second.ScannerScores["jailbreak"], 0.0001)
}

func TestBlockVerdictCacheEvictsWhenFull(t *testing.T) {
	clock := &stubClock{now: time.Unix(1_700_000_000, 0)}
	cache := newBlockVerdictCache(time.Minute, 2, clock)
	cache.put(7, "a", blockResult())
	cache.put(7, "b", blockResult())
	cache.put(7, "c", blockResult())
	require.LessOrEqual(t, len(cache.entries), 2, "cache must stay bounded")
	got, ok := cache.get(7, "c")
	require.True(t, ok, "the newest entry must survive eviction")
	require.Equal(t, ActionBlock, got.Action)
}

func TestBlockResponseDefaultsAndOverrides(t *testing.T) {
	status, message := ActiveConfig{}.BlockResponse()
	require.Equal(t, DefaultBlockHTTPStatus, status, "a config predating these fields keeps 403")
	require.Equal(t, DefaultBlockMessage, message)

	status, message = ActiveConfig{BlockHTTPStatus: 429, BlockMessage: " 自定义文案 "}.BlockResponse()
	require.Equal(t, 429, status)
	require.Equal(t, "自定义文案", message)

	status, _ = ActiveConfig{BlockHTTPStatus: 503}.BlockResponse()
	require.Equal(t, DefaultBlockHTTPStatus, status, "an out-of-range status falls back rather than shipping a 5xx")
}

func TestValidateBlockResponse(t *testing.T) {
	require.NoError(t, validateBlockResponse(0, ""), "omission means keep stored")
	require.NoError(t, validateBlockResponse(429, "请稍后再试"))
	require.Error(t, validateBlockResponse(503, ""), "5xx tells a client to retry a rejected prompt")
	require.Error(t, validateBlockResponse(200, ""))
	require.Error(t, validateBlockResponse(0, string(make([]rune, MaxBlockMessageRunes+1))))
}

// The operator asked for the category and a request ID, not the model's own
// reason: the reason explains which wording tripped the filter, which is the
// feedback an attacker needs to iterate past it.
func TestApplyBlockResponseNamesCategoriesAndRequestIDButNotReason(t *testing.T) {
	decision := &PromptDecision{Kind: DecisionBlock, Result: &NormalizedResult{
		Categories: []string{"jailbreak", "violent"},
		Reason:     "用户试图诱导模型输出提权步骤",
	}}
	cfg := ActiveConfig{BlockHTTPStatus: 451, BlockMessage: "请求已被安全策略拒绝"}
	applyBlockResponse(decision, cfg, PromptSnapshot{RequestID: "req-42"})

	require.Equal(t, 451, decision.HTTPStatus)
	require.Contains(t, decision.ClientMessage, "请求已被安全策略拒绝")
	require.Contains(t, decision.ClientMessage, "jailbreak, violent")
	require.Contains(t, decision.ClientMessage, "req-42")
	require.NotContains(t, decision.ClientMessage, "提权步骤")
}

// The per-node cap decides whether a burst is queued or rejected outright, and
// a rejection is indistinguishable from a hard failure in the logs, so the
// fallback behaviour matters: an unset value must not silently become something
// other than the limit the evaluator was built with.
func TestEffectiveNodeConcurrencyFallsBackForUnsetOrOutOfRange(t *testing.T) {
	require.Equal(t, DefaultNodeConcurrency, ActiveConfig{}.EffectiveNodeConcurrency())
	require.Equal(t, DefaultNodeConcurrency, ActiveConfig{NodeConcurrency: 0}.EffectiveNodeConcurrency())
	require.Equal(t, DefaultNodeConcurrency, ActiveConfig{NodeConcurrency: -4}.EffectiveNodeConcurrency())
	require.Equal(t, DefaultNodeConcurrency, ActiveConfig{NodeConcurrency: MaxNodeConcurrency + 1}.EffectiveNodeConcurrency())
	require.Equal(t, 64, ActiveConfig{NodeConcurrency: 64}.EffectiveNodeConcurrency())
}

// Raising the cap must take effect without a restart, which means replacing the
// channel: capacity is fixed once a channel exists.
func TestNodeSemaphoreResizesAndHonoursConstructorDefault(t *testing.T) {
	g := newGuardEvaluator(nil, nil, nil, 64, 3)

	unset := g.nodeSemaphore("dn1", 0)
	require.Equal(t, 3, cap(unset), "an unset limit keeps the evaluator's own default")

	raised := g.nodeSemaphore("dn1", 32)
	require.Equal(t, 32, cap(raised), "a configured limit resizes the limiter")
	require.False(t, unset == raised, "resizing must replace the limiter, not mutate it")

	again := g.nodeSemaphore("dn1", 32)
	require.True(t, raised == again, "an unchanged limit must reuse the same limiter")

	require.Equal(t, 3, cap(g.nodeSemaphore("dn1", MaxNodeConcurrency+1)), "out-of-range falls back rather than allocating absurd capacity")
}

func TestApplyBlockResponseOmitsEmptyDetails(t *testing.T) {
	decision := &PromptDecision{Kind: DecisionBlock, Result: &NormalizedResult{}}
	applyBlockResponse(decision, ActiveConfig{}, PromptSnapshot{})
	require.Equal(t, DefaultBlockHTTPStatus, decision.HTTPStatus)
	require.Equal(t, DefaultBlockMessage, decision.ClientMessage, "no categories and no request ID means no trailing parenthetical")
}
