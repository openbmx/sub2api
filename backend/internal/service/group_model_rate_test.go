package service

import (
	"context"
	"encoding/json"
	"math"
	"testing"
)

func TestGroupModelMultiplierFor(t *testing.T) {
	tests := []struct {
		name  string
		table map[string]float64
		model string
		want  float64
	}{
		{"nil table degrades to 1.0", nil, "claude-opus-4.5", 1.0},
		{"empty table degrades to 1.0", map[string]float64{}, "claude-opus-4.5", 1.0},
		{"empty model name degrades to 1.0", map[string]float64{"claude-opus-4.5": 2}, "", 1.0},
		{"no match degrades to 1.0", map[string]float64{"gpt-5.5": 2}, "claude-opus-4.5", 1.0},

		{"exact match", map[string]float64{"claude-opus-4.5": 1.5}, "claude-opus-4.5", 1.5},
		{"prefix wildcard match", map[string]float64{"claude-opus-4*": 1.5}, "claude-opus-4.5", 1.5},

		// 精确名必须压过通配，否则给某个具体版本配的特价会被家族规则吃掉。
		{"exact beats wildcard", map[string]float64{
			"claude-opus-4*":  3,
			"claude-opus-4.5": 1.5,
		}, "claude-opus-4.5", 1.5},

		// 最长前缀优先：更具体的规则赢。
		{"longest prefix wins", map[string]float64{
			"claude*":        3,
			"claude-opus-4*": 1.5,
			"claude-opus*":   2,
		}, "claude-opus-4.5", 1.5},

		{"bare star matches everything", map[string]float64{"*": 1.2}, "anything-at-all", 1.2},
		{"bare star loses to longer prefix", map[string]float64{
			"*":       3,
			"claude*": 1.5,
		}, "claude-opus-4.5", 1.5},

		// 0 是合法配置（该模型免费），不能被当成"未配置"降级成 1.0。
		{"zero is a valid free config", map[string]float64{"claude-opus-4.5": 0}, "claude-opus-4.5", 0},

		// 脏数据一律降级为 1.0（不叠乘），而不是 0（静默免单）或原值（放大计费）。
		{"negative degrades to 1.0", map[string]float64{"claude-opus-4.5": -2}, "claude-opus-4.5", 1.0},
		{"NaN degrades to 1.0", map[string]float64{"claude-opus-4.5": math.NaN()}, "claude-opus-4.5", 1.0},
		{"Inf degrades to 1.0", map[string]float64{"claude-opus-4.5": math.Inf(1)}, "claude-opus-4.5", 1.0},

		{"whitespace in key is tolerated", map[string]float64{" claude-opus-4* ": 1.5}, "claude-opus-4.5", 1.5},
		// 写路径都会 Normalize，但直接改库能绕过去；精确名带空白也必须命中，
		// 否则倍率会静默失效（与通配条目的容忍度保持一致）。
		{"whitespace in exact key is tolerated", map[string]float64{" claude-opus-4.5 ": 1.5}, "claude-opus-4.5", 1.5},
		{"untrimmed exact still beats wildcard", map[string]float64{
			"claude-opus-4*":    3,
			" claude-opus-4.5 ": 1.5,
		}, "claude-opus-4.5", 1.5},
		{"wildcard is case sensitive like model_mapping", map[string]float64{"claude*": 2}, "CLAUDE-opus-4.5", 1.0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := &Group{ModelRateMultipliers: tc.table}
			if got := g.ModelMultiplierFor(tc.model); got != tc.want {
				t.Fatalf("ModelMultiplierFor(%q) = %v, want %v", tc.model, got, tc.want)
			}
		})
	}
}

func TestGroupModelMultiplierFor_NilGroup(t *testing.T) {
	var g *Group
	if got := g.ModelMultiplierFor("claude-opus-4.5"); got != 1.0 {
		t.Fatalf("nil group: got %v, want 1.0", got)
	}
}

// map 迭代顺序随机，等长前缀必须有确定的胜出规则，否则同一配置会在不同请求上算出不同的钱。
func TestGroupModelMultiplierFor_EqualLengthPrefixesAreDeterministic(t *testing.T) {
	g := &Group{ModelRateMultipliers: map[string]float64{
		"claude-a*": 2,
		"claude-b*": 3,
	}}
	for i := 0; i < 100; i++ {
		if got := g.ModelMultiplierFor("claude-a-model"); got != 2 {
			t.Fatalf("iteration %d: got %v, want 2", i, got)
		}
	}
}

func TestValidateModelRateMultipliers(t *testing.T) {
	valid := []struct {
		name  string
		table map[string]float64
	}{
		{"nil", nil},
		{"empty", map[string]float64{}},
		{"exact name", map[string]float64{"claude-opus-4.5": 1.5}},
		{"trailing wildcard", map[string]float64{"claude-opus-4*": 1.5}},
		{"bare star", map[string]float64{"*": 1.2}},
		{"zero allowed", map[string]float64{"claude-opus-4.5": 0}},
	}
	for _, tc := range valid {
		t.Run("valid/"+tc.name, func(t *testing.T) {
			if err := ValidateModelRateMultipliers(tc.table); err != nil {
				t.Fatalf("expected valid, got error: %v", err)
			}
		})
	}

	invalid := []struct {
		name  string
		table map[string]float64
	}{
		{"empty key", map[string]float64{"   ": 1.5}},
		{"leading wildcard", map[string]float64{"*-opus": 1.5}},
		{"middle wildcard", map[string]float64{"claude-*-4.5": 1.5}},
		{"multiple wildcards", map[string]float64{"claude*opus*": 1.5}},
		{"negative", map[string]float64{"claude-opus-4.5": -1}},
		{"NaN", map[string]float64{"claude-opus-4.5": math.NaN()}},
		{"Inf", map[string]float64{"claude-opus-4.5": math.Inf(1)}},
	}
	for _, tc := range invalid {
		t.Run("invalid/"+tc.name, func(t *testing.T) {
			if err := ValidateModelRateMultipliers(tc.table); err == nil {
				t.Fatalf("expected error for %v, got nil", tc.table)
			}
		})
	}
}

func TestValidateModelRateMultipliers_EntryCap(t *testing.T) {
	table := make(map[string]float64, maxModelRateMultiplierEntries+1)
	for i := 0; i <= maxModelRateMultiplierEntries; i++ {
		table[string(rune('a'+i%26))+"-"+itoaTest(i)] = 1.0
	}
	if err := ValidateModelRateMultipliers(table); err == nil {
		t.Fatalf("expected entry-cap error for %d entries, got nil", len(table))
	}
}

func TestNormalizeModelRateMultipliers(t *testing.T) {
	if got := NormalizeModelRateMultipliers(nil); got != nil {
		t.Fatalf("nil should normalize to nil, got %v", got)
	}
	if got := NormalizeModelRateMultipliers(map[string]float64{}); got != nil {
		t.Fatalf("empty should normalize to nil, got %v", got)
	}
	if got := NormalizeModelRateMultipliers(map[string]float64{"  ": 1.5}); got != nil {
		t.Fatalf("all-blank keys should normalize to nil, got %v", got)
	}

	got := NormalizeModelRateMultipliers(map[string]float64{" claude-opus-4* ": 1.5, "": 2})
	if len(got) != 1 {
		t.Fatalf("expected 1 entry after dropping blank key, got %v", got)
	}
	if got["claude-opus-4*"] != 1.5 {
		t.Fatalf("expected trimmed key with value 1.5, got %v", got)
	}
}

// TestModelRateMultipliers_SnapshotRoundTrip 防回归：认证缓存快照必须携带 ModelRateMultipliers。
// 这是本特性最容易悄悄失效的一环——漏掉该字段不会报任何错，只是扣费路径拿到的
// apiKey.Group 缺表、ModelMultiplierFor 恒返回 1.0，模型倍率静默不生效。
// 走真实链路 snapshotFromAPIKey → snapshotToAPIKey，与 TestPeakMultiplier_SnapshotRoundTrip 同构。
func TestModelRateMultipliers_SnapshotRoundTrip(t *testing.T) {
	apiKey := &APIKey{
		User: &User{ID: 1, Status: StatusActive, Role: RoleUser},
		Group: &Group{
			SubscriptionType: "subscription",
			RateMultiplier:   1.0,
			ModelRateMultipliers: map[string]float64{
				"claude-opus-4*": 1.5,
				"gpt-5.5":        0.8,
			},
		},
	}
	svc := &APIKeyService{}

	snapshot := svc.snapshotFromAPIKey(context.Background(), apiKey)
	if snapshot == nil || snapshot.Group == nil {
		t.Fatalf("snapshot or snapshot.Group must not be nil")
	}
	if len(snapshot.Group.ModelRateMultipliers) != 2 {
		t.Fatalf("snapshot dropped model rate multipliers: %+v", snapshot.Group.ModelRateMultipliers)
	}

	restored := svc.snapshotToAPIKey("k", snapshot)
	if restored.Group == nil {
		t.Fatalf("restored.Group must not be nil")
	}
	if got := restored.Group.ModelMultiplierFor("claude-opus-4.5"); got != 1.5 {
		t.Fatalf("wildcard multiplier lost in round-trip: got %v, want 1.5", got)
	}
	if got := restored.Group.ModelMultiplierFor("gpt-5.5"); got != 0.8 {
		t.Fatalf("exact multiplier lost in round-trip: got %v, want 0.8", got)
	}
	if got := restored.Group.ModelMultiplierFor("gemini-3-pro"); got != 1.0 {
		t.Fatalf("unconfigured model must not be multiplied: got %v, want 1.0", got)
	}
}

// 快照以 JSON 存进 Redis。发版后的存量快照没有 model_rate_multipliers 键，
// 必须解成 nil 并安全降级为 1.0——既不能解码失败，也不能被当成 0 倍静默免单。
func TestModelRateMultipliers_LegacySnapshotJSONDegradesToOne(t *testing.T) {
	const legacy = `{"id":1,"name":"g","platform":"anthropic","rate_multiplier":1,"peak_rate_multiplier":1}`

	var snap APIKeyAuthGroupSnapshot
	if err := json.Unmarshal([]byte(legacy), &snap); err != nil {
		t.Fatalf("legacy snapshot must still decode: %v", err)
	}
	if snap.ModelRateMultipliers != nil {
		t.Fatalf("legacy snapshot should decode to a nil map, got %+v", snap.ModelRateMultipliers)
	}

	g := &Group{ModelRateMultipliers: snap.ModelRateMultipliers}
	if got := g.ModelMultiplierFor("claude-opus-4.5"); got != 1.0 {
		t.Fatalf("legacy snapshot must degrade to 1.0, got %v", got)
	}
}

// 快照 JSON 往返后倍率必须逐值保真——float64 走 JSON 不能丢精度或丢键。
func TestModelRateMultipliers_SnapshotJSONRoundTrip(t *testing.T) {
	original := APIKeyAuthGroupSnapshot{
		ModelRateMultipliers: map[string]float64{"claude-opus-4*": 1.5, "free-model": 0},
	}
	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded APIKeyAuthGroupSnapshot
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	g := &Group{ModelRateMultipliers: decoded.ModelRateMultipliers}
	if got := g.ModelMultiplierFor("claude-opus-4.9"); got != 1.5 {
		t.Fatalf("after JSON round-trip: got %v, want 1.5", got)
	}
	// 0 必须活下来：它是"该模型免费"的合法配置，不能被 omitempty 之类的规则抹掉。
	if got := g.ModelMultiplierFor("free-model"); got != 0 {
		t.Fatalf("zero multiplier lost in JSON round-trip: got %v, want 0", got)
	}
}

func itoaTest(i int) string {
	if i == 0 {
		return "0"
	}
	var buf []byte
	for i > 0 {
		buf = append([]byte{byte('0' + i%10)}, buf...)
		i /= 10
	}
	return string(buf)
}
