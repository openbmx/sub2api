package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const (
	fingerprintKeyPrefix   = "fingerprint:"
	fingerprintTTL         = 7 * 24 * time.Hour // 7天，配合每24小时懒续期可保持活跃账号永不过期
	maskedSessionKeyPrefix = "masked_session:"
	maskedSessionTTL       = 15 * time.Minute
)

// fingerprintKey generates the Redis key for account fingerprint cache.
func fingerprintKey(accountID int64) string {
	return fmt.Sprintf("%s%d", fingerprintKeyPrefix, accountID)
}

// maskedSessionKey generates the Redis key for masked session ID cache.
func maskedSessionKey(accountID int64) string {
	return fmt.Sprintf("%s%d", maskedSessionKeyPrefix, accountID)
}

type identityCache struct {
	rdb *redis.Client
}

func NewIdentityCache(rdb *redis.Client) service.IdentityCache {
	return &identityCache{rdb: rdb}
}

func (c *identityCache) GetFingerprint(ctx context.Context, accountID int64) (*service.Fingerprint, error) {
	key := fingerprintKey(accountID)
	val, err := c.rdb.Get(ctx, key).Result()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var fp service.Fingerprint
	if err := json.Unmarshal([]byte(val), &fp); err != nil {
		return nil, err
	}
	return &fp, nil
}

func (c *identityCache) SetFingerprint(ctx context.Context, accountID int64, fp *service.Fingerprint) error {
	key := fingerprintKey(accountID)
	val, err := json.Marshal(fp)
	if err != nil {
		return err
	}
	return c.rdb.Set(ctx, key, val, fingerprintTTL).Err()
}

func (c *identityCache) GetOrCreateFingerprint(ctx context.Context, accountID int64, fp *service.Fingerprint) (*service.Fingerprint, error) {
	val, err := json.Marshal(fp)
	if err != nil {
		return nil, err
	}
	created, err := c.rdb.SetNX(ctx, fingerprintKey(accountID), val, fingerprintTTL).Result()
	if err != nil {
		return nil, err
	}
	if created {
		return fp, nil
	}
	existing, err := c.GetFingerprint(ctx, accountID)
	if err == nil && existing == nil {
		err = fmt.Errorf("fingerprint initialization lost for account %d", accountID)
	}
	return existing, err
}

func (c *identityCache) GetMaskedSessionID(ctx context.Context, accountID int64) (string, error) {
	key := maskedSessionKey(accountID)
	val, err := c.rdb.Get(ctx, key).Result()
	if err != nil {
		if err == redis.Nil {
			return "", nil
		}
		return "", err
	}
	return val, nil
}

func (c *identityCache) SetMaskedSessionID(ctx context.Context, accountID int64, sessionID string) error {
	key := maskedSessionKey(accountID)
	return c.rdb.Set(ctx, key, sessionID, maskedSessionTTL).Err()
}
