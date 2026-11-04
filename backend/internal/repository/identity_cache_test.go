//go:build unit

package repository

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

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

func TestIdentityCacheMaskingConcurrentInitializationAndRenewal(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	cache := NewIdentityCache(rdb)
	svc := service.NewIdentityService(cache)
	account := &service.Account{ID: 941, Platform: service.PlatformAnthropic, Type: service.AccountTypeOAuth, Extra: map[string]any{"session_id_masking_enabled": true}}
	uid := service.FormatMetadataUserID(strings.Repeat("c", 64), "", "7578cf37-aaca-46e4-a45c-71285d9dbb83", "2.9.0")
	body := []byte(`{"metadata":{"user_id":` + strconv.Quote(uid) + `}}`)
	type result struct {
		body []byte
		err  error
	}
	start := make(chan struct{})
	results := make(chan result, 30)
	for range 30 {
		go func() {
			<-start
			out, err := svc.RewriteUserIDWithMasking(context.Background(), body, account, "target", strings.Repeat("a", 64), "claude-cli/2.9.0 (external, cli)")
			results <- result{out, err}
		}()
	}
	close(start)
	var first string
	for range 30 {
		got := <-results
		require.NoError(t, got.err)
		var value struct {
			Metadata struct {
				UserID string `json:"user_id"`
			} `json:"metadata"`
		}
		require.NoError(t, json.Unmarshal(got.body, &value))
		meta := service.ParseMetadataUserID(value.Metadata.UserID)
		require.NotNil(t, meta)
		if first == "" {
			first = meta.SessionID
		}
		require.Equal(t, first, meta.SessionID)
	}
	mr.FastForward(10 * time.Minute)
	_, err := svc.RewriteUserIDWithMasking(context.Background(), body, account, "target", strings.Repeat("a", 64), "claude-cli/2.9.0 (external, cli)")
	require.NoError(t, err)
	require.Equal(t, maskedSessionTTL, mr.TTL(maskedSessionKey(account.ID)))
}
