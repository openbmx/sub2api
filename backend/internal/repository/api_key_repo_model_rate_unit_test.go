package repository

import (
	"testing"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// groupEntityToService 是 ent 行 → service.Group 的唯一映射点，认证热路径的分组对象由它产出。
// 漏掉 ModelRateMultipliers 不会报错，只会让扣费路径拿到空表、模型倍率静默按 1.0 生效。
// 与 GetByKeyForAuth 的字段投影（api_key_repo.go 中的 group.FieldModelRateMultipliers）互为一对，
// 两者缺任一都会导致同样的静默失效。
func TestGroupEntityToService_PreservesModelRateMultipliers(t *testing.T) {
	group := &dbent.Group{
		ID:               1,
		Name:             "anthropic-tiered",
		Platform:         service.PlatformAnthropic,
		Status:           service.StatusActive,
		SubscriptionType: service.SubscriptionTypeStandard,
		RateMultiplier:   1,
		ModelRateMultipliers: map[string]float64{
			"claude-opus-4*": 1.5,
			"free-model":     0,
		},
	}

	got := groupEntityToService(group)
	require.NotNil(t, got)
	require.Equal(t, group.ModelRateMultipliers, got.ModelRateMultipliers)

	// 映射出来的对象必须能直接参与计费判定，而不只是字段相等。
	require.Equal(t, 1.5, got.ModelMultiplierFor("claude-opus-4.5"))
	require.Equal(t, 0.0, got.ModelMultiplierFor("free-model"))
	require.Equal(t, 1.0, got.ModelMultiplierFor("gemini-3-pro"))
}

// 存量分组该列为 NULL，映射后必须是 nil 且安全降级，不能让计费路径拿到半初始化的表。
func TestGroupEntityToService_NilModelRateMultipliersDegradesToOne(t *testing.T) {
	group := &dbent.Group{
		ID:               2,
		Name:             "legacy",
		Platform:         service.PlatformAnthropic,
		Status:           service.StatusActive,
		SubscriptionType: service.SubscriptionTypeStandard,
		RateMultiplier:   1,
	}

	got := groupEntityToService(group)
	require.NotNil(t, got)
	require.Nil(t, got.ModelRateMultipliers)
	require.Equal(t, 1.0, got.ModelMultiplierFor("claude-opus-4.5"))
}
