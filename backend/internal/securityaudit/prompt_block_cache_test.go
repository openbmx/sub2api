package securityaudit

import (
	"strings"
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

	_, _, ok := cache.get(7, "hash-a")
	require.False(t, ok, "empty cache must miss")

	cache.put(7, "hash-a", DecisionBlock, blockResult())
	got, kind, ok := cache.get(7, "hash-a")
	require.True(t, ok, "a stored block must replay")
	require.Equal(t, DecisionBlock, kind)
	require.Equal(t, ActionBlock, got.Action)
	require.Equal(t, []string{"jailbreak"}, got.Categories)

	clock.now = clock.now.Add(61 * time.Second)
	_, _, ok = cache.get(7, "hash-a")
	require.False(t, ok, "entry must expire once the TTL elapses")
}

// A pass is cached only to collapse an agent's rapid repeats. Giving it the
// block window would let one mistaken allow be replayed for ten minutes, which
// is the bypass this cache exists to prevent, inverted.
func TestPassVerdictExpiresFarSoonerThanABlock(t *testing.T) {
	clock := &stubClock{now: time.Unix(1_700_000_000, 0)}
	cache := newBlockVerdictCache(10*time.Minute, 16, clock)

	pass := &NormalizedResult{Decision: EventPass, RiskLevel: RiskLow, Action: ActionAllow}
	cache.put(7, "pass-hash", DecisionAllow, pass)
	cache.put(7, "block-hash", DecisionBlock, blockResult())

	_, kind, ok := cache.get(7, "pass-hash")
	require.True(t, ok)
	require.Equal(t, DecisionAllow, kind, "a replayed pass must stay a pass, not become a block")

	clock.now = clock.now.Add(DefaultPassVerdictTTL + time.Second)
	_, _, ok = cache.get(7, "pass-hash")
	require.False(t, ok, "the pass window must be short")
	_, _, ok = cache.get(7, "block-hash")
	require.True(t, ok, "the block must outlive it")
}

// The preceding model turn is carried for context, never judged, so only its
// tail is worth paying for — that is the part a terse follow-up answers.
func TestTrimAssistantContextKeepsTheTailWithinBudget(t *testing.T) {
	long := strings.Repeat("a", 400) + "END"
	trimmed := trimAssistantContext([]promptSegment{{text: long, role: "assistant"}})
	require.Len(t, trimmed, 1)
	require.LessOrEqual(t, len([]rune(trimmed[0].text)), blockingAssistantContextRunes)
	require.True(t, strings.HasSuffix(trimmed[0].text, "END"), "the tail must survive, not the head")

	short := trimAssistantContext([]promptSegment{{text: "短回复", role: "assistant"}})
	require.Equal(t, "短回复", short[0].text, "content within budget must be untouched")
}

func TestTrimAssistantContextSpendsBudgetOnTheNewestSegments(t *testing.T) {
	segments := []promptSegment{
		{text: strings.Repeat("o", 400), role: "assistant"},
		{text: strings.Repeat("n", 400), role: "assistant"},
	}
	trimmed := trimAssistantContext(segments)

	total := 0
	for _, segment := range trimmed {
		total += len([]rune(segment.text))
	}
	require.LessOrEqual(t, total, blockingAssistantContextRunes)
	require.Equal(t, 400, len([]rune(trimmed[len(trimmed)-1].text)), "the newest segment keeps its full text")
	require.True(t, strings.HasPrefix(trimmed[len(trimmed)-1].text, "n"), "chronological order must be restored")
}

// An agent turn is not a typed prompt: a 65 000-character build log arriving as
// the latest turn is what broke the audit three ways and dominated its cost.
func TestSampleTurnForScanKeepsBothEndsOfAnOversizedTurn(t *testing.T) {
	head := strings.Repeat("H", 800)
	tail := strings.Repeat("T", 800)
	sampled := sampleTurnForScan(head + strings.Repeat("m", 60000) + tail)

	require.Less(t, len([]rune(sampled)), blockingTurnScanRunes+64, "the sample must stay near the cap")
	require.True(t, strings.HasPrefix(sampled, "H"), "an override at the top must survive")
	require.True(t, strings.HasSuffix(sampled, "T"), "an appended directive at the bottom must survive")
	require.Contains(t, sampled, "字符已省略", "the cut must be visible so a truncated sentence is not misread")
	require.NotContains(t, sampled, strings.Repeat("m", 100), "the noisy middle is what gets dropped")
}

func TestSampleTurnForScanLeavesNormalPromptsIntact(t *testing.T) {
	typed := "帮我把这个函数改成并发安全的，用 sync.Mutex 就行"
	require.Equal(t, typed, sampleTurnForScan(typed))

	exact := strings.Repeat("x", blockingTurnScanRunes)
	require.Equal(t, exact, sampleTurnForScan(exact), "content exactly at the cap must not be cut")
}

func TestBlockVerdictCacheIsScopedToConfigVersion(t *testing.T) {
	clock := &stubClock{now: time.Unix(1_700_000_000, 0)}
	cache := newBlockVerdictCache(time.Minute, 16, clock)
	cache.put(7, "hash-a", DecisionBlock, blockResult())

	_, _, ok := cache.get(8, "hash-a")
	require.False(t, ok, "editing the policy must retire cached blocks")
}

func TestBlockVerdictCacheIgnoresEmptyHash(t *testing.T) {
	cache := newBlockVerdictCache(time.Minute, 16, &stubClock{now: time.Unix(1_700_000_000, 0)})
	cache.put(7, "", DecisionBlock, blockResult())
	_, _, ok := cache.get(7, "")
	require.False(t, ok, "a prompt with no hash must not collide with every other one")
}

// A replayed verdict is handed to the guard, which stamps latency onto it. If
// the cache returned a shared pointer the second replay would inherit the
// first one's numbers.
func TestBlockVerdictCacheReturnsIndependentCopies(t *testing.T) {
	cache := newBlockVerdictCache(time.Minute, 16, &stubClock{now: time.Unix(1_700_000_000, 0)})
	cache.put(7, "hash-a", DecisionBlock, blockResult())

	first, _, ok := cache.get(7, "hash-a")
	require.True(t, ok)
	first.LatencyMS = 999
	first.Categories[0] = "mutated"
	first.ScannerScores["jailbreak"] = 0.1

	second, _, ok := cache.get(7, "hash-a")
	require.True(t, ok)
	require.Zero(t, second.LatencyMS)
	require.Equal(t, []string{"jailbreak"}, second.Categories)
	require.InDelta(t, 0.9, second.ScannerScores["jailbreak"], 0.0001)
}

func TestBlockVerdictCacheEvictsWhenFull(t *testing.T) {
	clock := &stubClock{now: time.Unix(1_700_000_000, 0)}
	cache := newBlockVerdictCache(time.Minute, 2, clock)
	cache.put(7, "a", DecisionBlock, blockResult())
	cache.put(7, "b", DecisionBlock, blockResult())
	cache.put(7, "c", DecisionBlock, blockResult())
	require.LessOrEqual(t, len(cache.entries), 2, "cache must stay bounded")
	got, _, ok := cache.get(7, "c")
	require.True(t, ok, "the newest entry must survive eviction")
	require.Equal(t, ActionBlock, got.Action)
}

// Under multi-user load the cache fills with unrelated prompts. Discarding
// blocks to make room would hand every one of them a fresh roll of a
// nondeterministic model — the retry bypass this cache exists to close.
func TestEvictionSacrificesPassesBeforeBlocks(t *testing.T) {
	clock := &stubClock{now: time.Unix(1_700_000_000, 0)}
	cache := newBlockVerdictCache(10*time.Minute, 4, clock)
	pass := &NormalizedResult{Decision: EventPass, Action: ActionAllow}

	cache.put(7, "block-1", DecisionBlock, blockResult())
	cache.put(7, "block-2", DecisionBlock, blockResult())
	cache.put(7, "pass-1", DecisionAllow, pass)
	cache.put(7, "pass-2", DecisionAllow, pass)
	// Nothing has expired, so this insert must evict from the live set.
	cache.put(7, "block-3", DecisionBlock, blockResult())

	for _, key := range []string{"block-1", "block-2", "block-3"} {
		_, kind, ok := cache.get(7, key)
		require.True(t, ok, "block %s must survive eviction", key)
		require.Equal(t, DecisionBlock, kind)
	}
	for _, key := range []string{"pass-1", "pass-2"} {
		_, _, ok := cache.get(7, key)
		require.False(t, ok, "pass %s should have been sacrificed first", key)
	}
}

// When every live entry is a block there is nothing cheap left to drop, but
// clearing the map would release them all at once. Evict one instead.
func TestEvictionDropsOnlyOneBlockWhenNothingElseIsAvailable(t *testing.T) {
	clock := &stubClock{now: time.Unix(1_700_000_000, 0)}
	cache := newBlockVerdictCache(10*time.Minute, 3, clock)

	cache.put(7, "oldest", DecisionBlock, blockResult())
	clock.now = clock.now.Add(time.Second)
	cache.put(7, "middle", DecisionBlock, blockResult())
	clock.now = clock.now.Add(time.Second)
	cache.put(7, "newest", DecisionBlock, blockResult())
	clock.now = clock.now.Add(time.Second)
	cache.put(7, "extra", DecisionBlock, blockResult())

	require.Equal(t, 3, len(cache.entries), "only one entry may be released, not the whole map")
	_, _, ok := cache.get(7, "oldest")
	require.False(t, ok, "the entry nearest expiry is the one to go")
	for _, key := range []string{"middle", "newest", "extra"} {
		_, _, ok := cache.get(7, key)
		require.True(t, ok, "block %s must remain cached", key)
	}
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
