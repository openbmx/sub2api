package securityaudit

import (
	"strconv"
	"strings"
	"sync"
	"time"
)

// blockVerdictCache replays a block for an identical prompt within a TTL.
//
// Audit models are not deterministic. The same prompt sent eight times to
// deepseek-v4-flash at temperature 0 scored between 0.1 and 0.9, straddling a
// 0.7 block threshold, so a client that simply retries gets through in one or
// two attempts and the block is decorative. Caching the block makes a rejection
// stick for the window.
//
// Only blocks are cached, deliberately. Caching a pass would take one lucky
// false negative and freeze it in place for the whole TTL, which turns a
// transient miss into a reliable bypass — the exact failure this cache exists
// to prevent, inverted.
//
// The cache is per-process rather than shared through Redis. It needs no new
// dependency in the guard's constructor, and for the goal — stopping a client
// from retrying past a rejection — a process-local entry is enough: retries
// land on whichever instance served the block, and at worst a multi-instance
// deployment grants one extra attempt per instance instead of unlimited ones.
type blockVerdictCache struct {
	mu      sync.Mutex
	entries map[string]blockVerdictEntry
	ttl     time.Duration
	max     int
	clock   Clock
}

type blockVerdictEntry struct {
	result  NormalizedResult
	expires time.Time
}

func newBlockVerdictCache(ttl time.Duration, max int, clock Clock) *blockVerdictCache {
	if ttl <= 0 {
		ttl = DefaultBlockVerdictTTL
	}
	if max < 1 {
		max = maxBlockVerdictEntries
	}
	if clock == nil {
		clock = realClock{}
	}
	return &blockVerdictCache{entries: make(map[string]blockVerdictEntry), ttl: ttl, max: max, clock: clock}
}

// blockVerdictKey scopes an entry to the config that produced it, so editing
// the prompt, the thresholds or the endpoint pool retires every cached block
// instead of letting a stale policy keep rejecting traffic.
func blockVerdictKey(configVersion int64, promptHash string) string {
	hash := strings.TrimSpace(promptHash)
	if hash == "" {
		return ""
	}
	return strconv.FormatInt(configVersion, 10) + ":" + hash
}

func (c *blockVerdictCache) get(configVersion int64, promptHash string) (NormalizedResult, bool) {
	if c == nil {
		return NormalizedResult{}, false
	}
	key := blockVerdictKey(configVersion, promptHash)
	if key == "" {
		return NormalizedResult{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		return NormalizedResult{}, false
	}
	if !c.clock.Now().Before(entry.expires) {
		delete(c.entries, key)
		return NormalizedResult{}, false
	}
	return cloneNormalizedResult(entry.result), true
}

func (c *blockVerdictCache) put(configVersion int64, promptHash string, result *NormalizedResult) {
	if c == nil || result == nil {
		return
	}
	key := blockVerdictKey(configVersion, promptHash)
	if key == "" {
		return
	}
	now := c.clock.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= c.max {
		c.evictLocked(now)
	}
	c.entries[key] = blockVerdictEntry{result: cloneNormalizedResult(*result), expires: now.Add(c.ttl)}
}

// evictLocked drops expired entries first. If every entry is still live the
// map is cleared outright: entries are cheap to rebuild (one audit call) and a
// full reset is preferable to tracking access order for a cache this small.
func (c *blockVerdictCache) evictLocked(now time.Time) {
	for key, entry := range c.entries {
		if !now.Before(entry.expires) {
			delete(c.entries, key)
		}
	}
	if len(c.entries) >= c.max {
		c.entries = make(map[string]blockVerdictEntry, c.max)
	}
}

// cloneNormalizedResult deep-copies the mutable fields so a cached verdict
// cannot be altered by whoever consumed a previous replay: the guard stamps
// ChunkTotal and LatencyMS onto the result it returns.
func cloneNormalizedResult(in NormalizedResult) NormalizedResult {
	out := in
	out.Categories = append([]string(nil), in.Categories...)
	out.MatchedScanners = append([]string(nil), in.MatchedScanners...)
	out.UnknownCategories = append([]string(nil), in.UnknownCategories...)
	if in.ScannerScores != nil {
		out.ScannerScores = make(map[string]float64, len(in.ScannerScores))
		for k, v := range in.ScannerScores {
			out.ScannerScores[k] = v
		}
	}
	if in.ScannerEvidence != nil {
		out.ScannerEvidence = make(map[string]string, len(in.ScannerEvidence))
		for k, v := range in.ScannerEvidence {
			out.ScannerEvidence[k] = v
		}
	}
	return out
}
