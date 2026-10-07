//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// 在途预留的估算必须与计费同口径地叠乘分组的模型级倍率（model_rate_multipliers）。
// 只认分组/用户倍率的估算会把倍率 0.5 的模型按两倍预留、误拒低余额用户的并发请求；
// 倍率 3 的模型则只预留三分之一，放过的正是预留要防的透支。

func TestInflightEstimate_AppliesGroupModelRateMultiplier(t *testing.T) {
	groupID := int64(50)
	in, out := 1e-6, 2e-6
	group := &Group{ID: groupID, Platform: PlatformAnthropic, RateMultiplier: 2, ModelPricing: []ChannelModelPricing{
		{Models: []string{"rated-*"}, BillingMode: BillingModeToken, InputPrice: &in, OutputPrice: &out},
	}}
	svc := newInflightEstimateGateway(t, nil)
	apiKey := &APIKey{User: &User{ID: 1}, GroupID: &groupID, Group: group}
	req := InflightEstimateRequest{Model: "rated-x", BodyBytes: 4000, MaxTokens: 500}
	ctx := context.Background()

	base, priced := svc.EstimateInflightReservation(ctx, apiKey, req)
	require.True(t, priced)
	require.InDelta(t, (1000*in+500*out)*2, base, 1e-12, "precondition: group rate only")

	group.ModelRateMultipliers = map[string]float64{"rated-*": 0.5}
	est, priced := svc.EstimateInflightReservation(ctx, apiKey, req)
	require.True(t, priced)
	require.InDelta(t, base*0.5, est, 1e-12)

	group.ModelRateMultipliers = map[string]float64{"rated-x": 3, "rated-*": 0.5}
	est, priced = svc.EstimateInflightReservation(ctx, apiKey, req)
	require.True(t, priced)
	require.InDelta(t, base*3, est, 1e-12, "exact name wins over the wildcard, as in billing")
}

// 倍率 0 = 管理员把模型设为免费：估算应为「已定价、0 元」，handler 据此不预留也不拒绝。
// 若报成「无法定价」，estimate 会改按回退候选（这里是映射后的付费模型）预留，
// fail_closed_on_unpriced=true 时更会直接拒掉一个计费为 0 的请求。
func TestInflightEstimate_ZeroModelRateMultiplierIsFreeNotUnpriced(t *testing.T) {
	groupID := int64(51)
	ch := Channel{
		ID:                 51,
		Status:             StatusActive,
		GroupIDs:           []int64{groupID},
		BillingModelSource: BillingModelSourceRequested,
		ModelMapping: map[string]map[string]string{
			"anthropic": {"free-alias-51": "claude-sonnet-4-5"},
		},
	}
	cs := newTestChannelService(makeStandardRepo(ch, map[int64]string{groupID: "anthropic"}))
	svc := newInflightEstimateGateway(t, cs)
	in, out := 1e-6, 2e-6
	group := &Group{ID: groupID, Platform: PlatformAnthropic, RateMultiplier: 1, ModelPricing: []ChannelModelPricing{
		{Models: []string{"free-alias-51"}, BillingMode: BillingModeToken, InputPrice: &in, OutputPrice: &out},
	}}
	apiKey := &APIKey{User: &User{ID: 1}, GroupID: &groupID, Group: group}
	req := InflightEstimateRequest{Model: "free-alias-51", BodyBytes: 4000, MaxTokens: 500}
	ctx := context.Background()

	est, priced := svc.EstimateInflightReservation(ctx, apiKey, req)
	require.True(t, priced)
	require.InDelta(t, 1000*in+500*out, est, 1e-12, "precondition: the requested model is priced by the group")

	group.ModelRateMultipliers = map[string]float64{"free-alias-51": 0}
	est, priced = svc.EstimateInflightReservation(ctx, apiKey, req)
	require.True(t, priced, "a free model is priced at zero, not unpriced")
	require.Zero(t, est, "must not fall back to reserving the mapped model's price for a request billed at $0")
}

// 计费侧按渠道映射后的计费模型查倍率表，估算必须查同一个名字，而不是客户端传来的别名。
func TestInflightEstimate_ModelRateMultiplierKeysOnBillingModel(t *testing.T) {
	groupID := int64(53)
	ch := Channel{
		ID:       53,
		Status:   StatusActive,
		GroupIDs: []int64{groupID},
		ModelMapping: map[string]map[string]string{
			"anthropic": {"my-alias-53": "claude-sonnet-4-5"},
		},
	}
	cs := newTestChannelService(makeStandardRepo(ch, map[int64]string{groupID: "anthropic"}))
	svc := newInflightEstimateGateway(t, cs)
	group := &Group{ID: groupID, Platform: PlatformAnthropic, RateMultiplier: 1}
	apiKey := &APIKey{User: &User{ID: 1}, GroupID: &groupID, Group: group}
	req := InflightEstimateRequest{Model: "my-alias-53", BodyBytes: 4000, MaxTokens: 1000}
	ctx := context.Background()

	base, priced := svc.EstimateInflightReservation(ctx, apiKey, req)
	require.True(t, priced)
	require.Greater(t, base, 0.0)

	group.ModelRateMultipliers = map[string]float64{"claude-sonnet-4-5": 0.5}
	est, priced := svc.EstimateInflightReservation(ctx, apiKey, req)
	require.True(t, priced)
	require.InDelta(t, base*0.5, est, 1e-12)
}

// 联网搜索与音频没有模型维度，计费侧用叠乘前的 capabilityMultiplier / baseMultiplier；
// 图片按次计费则叠乘（imageMultiplier *= modelFactor）。估算逐项对齐。
func TestInflightEstimate_ModelRateMultiplierFollowsBillingPerKind(t *testing.T) {
	groupID := int64(52)
	p4k := 0.3
	search := 1000.0
	group := &Group{ID: groupID, Platform: PlatformOpenAI, RateMultiplier: 1, ImagePrice4K: &p4k, SearchPricePer1k: &search}
	svc := newInflightEstimateGateway(t, nil)
	apiKey := &APIKey{User: &User{ID: 1}, GroupID: &groupID, Group: group}
	ctx := context.Background()
	searchReq := InflightEstimateRequest{Model: "grok-web-search", Kind: InflightEstimatePerRequest, SearchCalls: 1}
	audioReq := InflightEstimateRequest{Model: "realtime", Kind: InflightEstimateAudio, AudioMode: "realtime", AudioUnits: 1}
	imageReq := InflightEstimateRequest{Model: "gpt-image-1", Kind: InflightEstimateImage, Units: 2}

	searchBase, _ := svc.EstimateInflightReservation(ctx, apiKey, searchReq)
	audioBase, _ := svc.EstimateInflightReservation(ctx, apiKey, audioReq)
	imageBase, _ := svc.EstimateInflightReservation(ctx, apiKey, imageReq)
	require.Greater(t, searchBase, 0.0)
	require.Greater(t, audioBase, 0.0)
	require.Greater(t, imageBase, 0.0)

	group.ModelRateMultipliers = map[string]float64{"*": 3}
	est, priced := svc.EstimateInflightReservation(ctx, apiKey, searchReq)
	require.True(t, priced)
	require.InDelta(t, searchBase, est, 1e-12, "search surcharge is not model-rated")
	est, priced = svc.EstimateInflightReservation(ctx, apiKey, audioReq)
	require.True(t, priced)
	require.InDelta(t, audioBase, est, 1e-12, "audio is not model-rated")
	est, priced = svc.EstimateInflightReservation(ctx, apiKey, imageReq)
	require.True(t, priced)
	require.InDelta(t, imageBase*3, est, 1e-12, "image generation is model-rated")
}
