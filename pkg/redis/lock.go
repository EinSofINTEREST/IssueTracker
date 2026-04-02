package redis

import (
	"context"
	"errors"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// LockKeyPrefix는 분산 락 키의 공통 접두사입니다.
const LockKeyPrefix = "lock:crawl:"

// AcquireLock은 분산 락을 획득합니다.
//
// AcquireLock attempts to acquire a distributed lock via SET … NX PX.
// Returns true if the lock was acquired, false if it is already held.
// key 형식: "lock:crawl:{url_hash}" (LockKey 헬퍼 사용 권장)
// TODO: 향후 필요 시 고급 락 구현 (예: RedLock)으로 확장, TEST 케이스 추가
func (c *Client) AcquireLock(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	err := c.rdb.SetArgs(ctx, key, 1, goredis.SetArgs{
		Mode: "NX",
		TTL:  ttl,
	}).Err()
	if errors.Is(err, goredis.Nil) {
		// NX 조건 불충족 — 키가 이미 존재함
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("acquire lock %s: %w", key, err)
	}
	return true, nil
}

// ReleaseLock은 분산 락을 해제합니다.
//
// ReleaseLock releases a previously acquired lock by deleting the key.
func (c *Client) ReleaseLock(ctx context.Context, key string) error {
	if err := c.rdb.Del(ctx, key).Err(); err != nil {
		return fmt.Errorf("release lock %s: %w", key, err)
	}
	return nil
}

// LockKey는 URL 해시로 표준 락 키를 생성합니다.
//
// LockKey builds the canonical lock key from a URL hash.
// urlHash: SHA-256 hex string of the target URL
func LockKey(urlHash string) string {
	return LockKeyPrefix + urlHash
}
