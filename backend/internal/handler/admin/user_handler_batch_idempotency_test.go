package admin

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// ctxHonoringIdempotencyRepo fails writes on a cancelled context, as the real
// repository (database/sql ExecContext) does.
type ctxHonoringIdempotencyRepo struct{ *memoryIdempotencyRepoStub }

func (r ctxHonoringIdempotencyRepo) MarkSucceeded(ctx context.Context, id int64, status int, body string, expiresAt time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return r.memoryIdempotencyRepoStub.MarkSucceeded(ctx, id, status, body, expiresAt)
}

func (r ctxHonoringIdempotencyRepo) MarkFailedRetryable(ctx context.Context, id int64, reason string, lockedUntil, expiresAt time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return r.memoryIdempotencyRepoStub.MarkFailedRetryable(ctx, id, reason, lockedUntil, expiresAt)
}

// ledgerBatchAdminService credits users one at a time and fails a credit on a
// cancelled context, the way userRepo.AdjustBalance does.
type ledgerBatchAdminService struct {
	*stubAdminService
	mu       sync.Mutex
	credits  map[int64]float64
	writes   int
	onCredit func(n int)
}

func (s *ledgerBatchAdminService) BatchUpdateUserBalance(ctx context.Context, userIDs []int64, amount float64, _ string, _ string) (*service.BatchUserOperationResult, error) {
	result := &service.BatchUserOperationResult{}
	for _, id := range userIDs {
		if err := ctx.Err(); err != nil {
			result.Skipped = append(result.Skipped, service.BatchUserOperationSkipped{ID: id, Reason: err.Error()})
			continue
		}
		s.mu.Lock()
		s.credits[id] += amount
		s.writes++
		n := s.writes
		s.mu.Unlock()
		result.Affected++
		result.SuccessIDs = append(result.SuccessIDs, id)
		if s.onCredit != nil {
			s.onCredit(n)
		}
	}
	return result, nil
}

func postBatchBalance(router *gin.Engine, ctx context.Context, key string, body []byte) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/users/batch-balance", bytes.NewReader(body)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key)
	router.ServeHTTP(rec, req)
	return rec
}

// The batch used to run on the request context: a client, proxy or browser
// timeout cancelled it halfway, the result was never stored, the key stayed
// "processing" (409) for 24h, and a fresh key credited the finished users again.
func TestBatchUpdateBalanceSurvivesClientDisconnectAndReplays(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := service.DefaultIdempotencyConfig()
	cfg.ProcessingTimeout = 50 * time.Millisecond
	service.SetDefaultIdempotencyCoordinator(service.NewIdempotencyCoordinator(ctxHonoringIdempotencyRepo{newMemoryIdempotencyRepoStub()}, cfg))
	t.Cleanup(func() { service.SetDefaultIdempotencyCoordinator(nil) })

	svc := &ledgerBatchAdminService{stubAdminService: newStubAdminService(), credits: map[int64]float64{}}
	router := setupBatchUserRouter(svc)
	body := []byte(`{"user_ids":[1,2,3,4],"balance":10,"operation":"add"}`)

	ctx, cancel := context.WithCancel(context.Background())
	svc.onCredit = func(n int) {
		if n == 2 {
			cancel() // the client gives up after two users
		}
	}
	postBatchBalance(router, ctx, "k1", body)
	svc.onCredit = nil

	time.Sleep(100 * time.Millisecond) // well past the processing lock
	retry := postBatchBalance(router, context.Background(), "k1", body)
	require.Equal(t, http.StatusOK, retry.Code, retry.Body.String())
	require.Equal(t, "true", retry.Header().Get("X-Idempotency-Replayed"), "the same key must replay the stored outcome")
	for _, id := range []int64{1, 2, 3, 4} {
		require.Equal(t, 10.0, svc.credits[id], "user %d must be credited exactly once", id)
	}
}

// The hash covered user_ids in request order, so the same batch re-picked in a
// different order reused its key with a different hash: 409 on every retry,
// until a fresh key ran the batch a second time.
func TestBatchUpdateBalanceKeyIgnoresUserOrder(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service.SetDefaultIdempotencyCoordinator(service.NewIdempotencyCoordinator(ctxHonoringIdempotencyRepo{newMemoryIdempotencyRepoStub()}, service.DefaultIdempotencyConfig()))
	t.Cleanup(func() { service.SetDefaultIdempotencyCoordinator(nil) })

	svc := &ledgerBatchAdminService{stubAdminService: newStubAdminService(), credits: map[int64]float64{}}
	router := setupBatchUserRouter(svc)

	first := postBatchBalance(router, context.Background(), "k-order", []byte(`{"user_ids":[3,1,2],"balance":10,"operation":"add"}`))
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	retry := postBatchBalance(router, context.Background(), "k-order", []byte(`{"user_ids":[2,3,1,1],"balance":10,"operation":"add"}`))
	require.Equal(t, http.StatusOK, retry.Code, retry.Body.String())
	require.Equal(t, "true", retry.Header().Get("X-Idempotency-Replayed"))
	for _, id := range []int64{1, 2, 3} {
		require.Equal(t, 10.0, svc.credits[id], "user %d must be credited exactly once", id)
	}
}
