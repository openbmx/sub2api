//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// schedulingThresholdFallbackRepo mirrors the production settingRepository contract,
// where a missing row surfaces as ErrSettingNotFound. The shared mockSettingRepo
// returns ("", nil) instead, so the hot-path fallback branch was never covered.
type schedulingThresholdFallbackRepo struct {
	*mockSettingRepo
	err error
}

func (r *schedulingThresholdFallbackRepo) GetValue(_ context.Context, _ string) (string, error) {
	return "", r.err
}

func newSettingServiceForThresholdFallback(err error) *SettingService {
	accountSchedulingThresholdsSF.Forget(SettingKeyAccountSchedulingThresholds)
	accountSchedulingThresholdsCache.Store(&cachedAccountSchedulingThresholds{})
	repo := &schedulingThresholdFallbackRepo{mockSettingRepo: newMockSettingRepo(), err: err}
	return NewSettingService(repo, &config.Config{})
}

func loadAccountSchedulingThresholdsCacheTTL(t *testing.T) time.Duration {
	t.Helper()
	cached, ok := accountSchedulingThresholdsCache.Load().(*cachedAccountSchedulingThresholds)
	require.True(t, ok)
	require.NotNil(t, cached)
	return time.Until(time.Unix(0, cached.expiresAt))
}

// An unconfigured key is expected state, not a failure: the gateway hot path must
// cache the defaults for the full TTL instead of re-querying every 5s.
func TestGetAccountSchedulingThresholds_MissingKeyUsesNormalCacheTTL(t *testing.T) {
	svc := newSettingServiceForThresholdFallback(ErrSettingNotFound)

	got := svc.GetAccountSchedulingThresholds(context.Background())
	require.Equal(t, defaultAccountSchedulingThresholds(), got)
	require.Greater(t, loadAccountSchedulingThresholdsCacheTTL(t), accountSchedulingThresholdsErrorTTL)
}

// A genuine DB failure keeps the short TTL so the next read retries quickly.
func TestGetAccountSchedulingThresholds_RepoFailureUsesErrorTTL(t *testing.T) {
	svc := newSettingServiceForThresholdFallback(errors.New("dial tcp: connection refused"))

	got := svc.GetAccountSchedulingThresholds(context.Background())
	require.Equal(t, defaultAccountSchedulingThresholds(), got)
	require.LessOrEqual(t, loadAccountSchedulingThresholdsCacheTTL(t), accountSchedulingThresholdsErrorTTL)
}
