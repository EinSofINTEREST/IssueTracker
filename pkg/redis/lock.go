package redis

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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

// ReleaseLock은 소유권 검증 없이 키를 삭제합니다.
//
// ReleaseLock releases a lock by deleting the key, without ownership verification.
//
// **용도 한정**: 운영자의 명시적 무효화 (IngestionMarker.Invalidate 등) 처럼 "누가 잡았든 지운다"
// 가 의도인 경우에만 사용합니다. 처리 중 보호 목적의 락 해제에는 ReleaseLockOwned 를
// 사용하세요 — 그렇지 않으면 TTL 만료 후 다른 인스턴스가 재획득한 락을 지울 수 있습니다 (이슈 #63).
func (c *Client) ReleaseLock(ctx context.Context, key string) error {
	if err := c.rdb.Del(ctx, key).Err(); err != nil {
		return fmt.Errorf("release lock %s: %w", key, err)
	}
	return nil
}

// luaReleaseOwned 는 소유권 확인 후 삭제하는 Lua 스크립트입니다.
//
// GET 과 DEL 사이에 다른 인스턴스가 끼어들 수 없도록 원자적으로 수행 — TTL 만료 후 다른
// 인스턴스가 재획득한 락을 지우는 사고를 막습니다.
// internal/storage/redis 의 inflight locker (이슈 #261) 에서 검증된 패턴을 일반화했습니다.
const luaReleaseOwned = `
if redis.call("GET", KEYS[1]) == ARGV[1] then
    return redis.call("DEL", KEYS[1])
else
    return 0
end`

// AcquireLockWithToken 은 소유권 토큰을 값으로 저장하며 분산 락을 획득합니다 (이슈 #63).
//
// AcquireLock 과의 차이: 값이 고정 `1` 이 아니라 호출마다 다른 난수 토큰입니다. 반환된 토큰을
// ReleaseLockOwned 에 넘기면 "내가 잡은 락" 일 때만 해제되어, 아래 경쟁 상태를 차단합니다.
//
//  1. 인스턴스 A 가 락 획득
//  2. 처리가 TTL 을 초과 → 락 자동 만료
//  3. 인스턴스 B 가 같은 키를 재획득
//  4. 뒤늦게 끝난 A 가 Release 호출 → **B 의 유효한 락이 삭제됨**
//
// 반환: (token, acquired, err). acquired=false 면 token 은 빈 문자열.
func (c *Client) AcquireLockWithToken(ctx context.Context, key string, ttl time.Duration) (string, bool, error) {
	token, err := newLockToken()
	if err != nil {
		return "", false, fmt.Errorf("generate lock token for %s: %w", key, err)
	}

	err = c.rdb.SetArgs(ctx, key, token, goredis.SetArgs{
		Mode: "NX",
		TTL:  ttl,
	}).Err()
	if errors.Is(err, goredis.Nil) {
		// NX 조건 불충족 — 키가 이미 존재함
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("acquire lock %s: %w", key, err)
	}
	return token, true, nil
}

// ReleaseLockOwned 는 토큰이 일치할 때만 락을 해제합니다 (이슈 #63).
//
// 반환 released=false 는 **에러가 아닙니다** — 이미 TTL 로 만료됐거나 다른 인스턴스가 재획득한
// 정상 상황입니다. 호출자는 이를 로그로 남겨 TTL 초과 빈도를 관측할 수 있습니다.
func (c *Client) ReleaseLockOwned(ctx context.Context, key, token string) (bool, error) {
	if token == "" {
		return false, errors.New("release lock: empty token")
	}
	res, err := c.rdb.Eval(ctx, luaReleaseOwned, []string{key}, token).Result()
	if err != nil && !errors.Is(err, goredis.Nil) {
		return false, fmt.Errorf("release lock %s: %w", key, err)
	}
	deleted, _ := res.(int64)
	return deleted > 0, nil
}

// newLockToken 은 락 소유권 식별용 난수 토큰을 생성합니다.
//
// crypto/rand 실패는 매우 드물지만 조용히 무시하면 빈 토큰으로 락을 잡아 소유권 검증이
// 무력화되므로 에러로 올립니다.
func newLockToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// LockKey는 URL 해시로 표준 락 키를 생성합니다.
//
// LockKey builds the canonical lock key from a URL hash.
// urlHash: SHA-256 hex string of the target URL
func LockKey(urlHash string) string {
	return LockKeyPrefix + urlHash
}
