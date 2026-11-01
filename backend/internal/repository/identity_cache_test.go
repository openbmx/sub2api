//go:build unit

package repository

import (
	"context"
	"math"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestFingerprintKey(t *testing.T) {
	tests := []struct {
		name      string
		accountID int64
		expected  string
	}{
		{
			name:      "normal_account_id",
			accountID: 123,
			expected:  "fingerprint:123",
		},
		{
			name:      "zero_account_id",
			accountID: 0,
			expected:  "fingerprint:0",
		},
		{
			name:      "negative_account_id",
			accountID: -1,
			expected:  "fingerprint:-1",
		},
		{
			name:      "max_int64",
			accountID: math.MaxInt64,
			expected:  "fingerprint:9223372036854775807",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := fingerprintKey(tc.accountID)
			require.Equal(t, tc.expected, got)
		})
	}
}

func TestIdentityCacheConcurrentInitializationKeepsOneClientID(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	cache := NewIdentityCache(rdb)
	ctx := context.Background()
	missing, err := cache.GetFingerprint(ctx, 81)
	require.NoError(t, err)
	require.Nil(t, missing)
	const count = 20
	type result struct {
		fp  *service.Fingerprint
		err error
	}
	results := make(chan result, count)
	start := make(chan struct{})
	for range count {
		go func() {
			<-start
			fp, err := service.NewIdentityService(cache).GetOrCreateFingerprint(ctx, 81, http.Header{"User-Agent": {claude.DefaultUserAgent()}})
			results <- result{fp, err}
		}()
	}
	close(start)
	var clientID string
	for range count {
		got := <-results
		require.NoError(t, got.err)
		require.NotNil(t, got.fp)
		require.NotEmpty(t, got.fp.ClientID)
		if clientID == "" {
			clientID = got.fp.ClientID
		}
		require.Equal(t, clientID, got.fp.ClientID)
	}
	require.Equal(t, fingerprintTTL, mr.TTL(fingerprintKey(81)))
}

func TestIdentityCacheFailureDoesNotCreateTemporaryIdentity(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr(), MaxRetries: -1})
	t.Cleanup(func() { _ = rdb.Close() })
	cache := NewIdentityCache(rdb)
	require.NoError(t, rdb.Close())
	fp, err := service.NewIdentityService(cache).GetOrCreateFingerprint(context.Background(), 82, http.Header{"User-Agent": {claude.DefaultUserAgent()}})
	require.Error(t, err)
	require.Nil(t, fp)
}
