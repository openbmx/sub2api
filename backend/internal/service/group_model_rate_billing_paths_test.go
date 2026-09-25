//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// The per-model group multiplier applies to every image billing branch
// (migration 221). Batch images price from their own snapshot, which used to
// skip it, so the same model billed x2 synchronously and x1 as a batch.
func TestBatchImagePricingSnapshotAppliesModelRateMultiplier(t *testing.T) {
	svc, repo, _, _, _ := newTestBatchImagePublicService(true)
	groupID := int64(7)
	svc.GroupRepo = &publicBatchImageGroupRepo{groups: map[int64]*Group{
		groupID: {
			ID:                           groupID,
			Platform:                     PlatformGemini,
			RateMultiplier:               1.0,
			AllowImageGeneration:         true,
			AllowBatchImageGeneration:    true,
			BatchImageDiscountMultiplier: 0.5,
			BatchImageHoldMultiplier:     0.6,
			ModelRateMultipliers:         map[string]float64{"gemini-2.5-flash-image*": 2},
		},
	}}

	got, err := svc.Submit(context.Background(), BatchImageOwner{UserID: 11, APIKeyID: 22, GroupID: &groupID}, validBatchImageSubmitRequest(), "")
	require.NoError(t, err)

	job := repo.jobs[got.ID]
	require.InDelta(t, 2.0, job.GroupRateMultiplier, 1e-12)
	require.InDelta(t, 0.25*2*0.5, job.BillableUnitPrice, 1e-12)
}

// Web search and audio are per-call / per-duration capabilities with no model
// dimension, so the model factor must not scale them (migration 221). The OpenAI
// path always did this; the gateway path used to reuse the factored multiplier.
func TestGatewayRecordUsageCostSearchSurchargeSkipsModelFactor(t *testing.T) {
	svc := &GatewayService{billingService: NewBillingService(&config.Config{}, nil)}
	price := 10.0
	apiKey := &APIKey{Group: &Group{ID: 1, SearchPricePer1k: &price}}
	result := &ForwardResult{Model: "unpriced-model", SearchCount: 100}

	// multiplier/imageMultiplier already carry a model factor of 2; the
	// capability multiplier (group x peak, before the factor) is 1.
	cost := svc.calculateRecordUsageCost(context.Background(), result, apiKey, "unpriced-model", 2.0, 2.0, time.Time{}, 1.0)
	require.NotNil(t, cost)
	require.InDelta(t, 1.0, cost.TotalCost, 1e-9) // 100 calls at 10 per 1k
	require.InDelta(t, 1.0, cost.ActualCost, 1e-9)

	// Callers that do not pass it (upstream tests) keep the previous behaviour.
	legacy := svc.calculateRecordUsageCost(context.Background(), result, apiKey, "unpriced-model", 2.0, 2.0, time.Time{})
	require.InDelta(t, 2.0, legacy.ActualCost, 1e-9)
}
