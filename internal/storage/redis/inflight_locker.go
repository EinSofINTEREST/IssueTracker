package redisstore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"issuetracker/internal/storage"
)

// DefaultInflightLockTTL 은 Redis 분산 Lock 의 기본 TTL 입니다.
// CLAUDE_CODE_TIMEOUT 기본값(120s) 의 2.5배 — 프로세스 크래시 후 stuck 슬롯 자동 해제 보장.
const DefaultInflightLockTTL = 5 * time.Minute

const inflightKeyPrefix = "llmgen:inflight:"

// luaRelease 는 소유권 확인 후 삭제하는 Lua 스크립트입니다.
// GET 과 DEL 의 원자성 보장 — 다른 인스턴스가 재획득한 락을 삭제하지 않습니다.
const luaRelease = `
if redis.call("GET", KEYS[1]) == ARGV[1] then
    return redis.call("DEL", KEYS[1])
else
    return 0
end`

// inflightLocker 는 Redis SET NX+TTL 기반 분산 InflightLocker 구현입니다.
//
// 다중 인스턴스 환경에서 동일 (host, targetType) 에 대한 LLM 호출을 1회로 제한합니다.
// TTL 은 프로세스 크래시 후 stuck 슬롯 자동 해제를 보장.
//
// 소유권 보장: TryAcquire 시 고유 token 을 값으로 저장하고, Release 시 Lua 스크립트로
// token 일치를 확인한 후 삭제 — TTL 만료 후 다른 인스턴스가 재획득한 락을 삭제하지 않습니다.
type inflightLocker struct {
	rdb    *goredis.Client
	ttl    time.Duration
	mu     sync.Mutex
	tokens map[string]string // lockKey → token (소유권 추적)
}

// NewInflightLocker 는 Redis 기반 InflightLocker 를 생성합니다.
// ttl ≤ 0 이면 DefaultInflightLockTTL 로 보정합니다.
func NewInflightLocker(rdb *goredis.Client, ttl time.Duration) storage.InflightLocker {
	if ttl <= 0 {
		ttl = DefaultInflightLockTTL
	}
	return &inflightLocker{rdb: rdb, ttl: ttl, tokens: make(map[string]string)}
}

func (r *inflightLocker) TryAcquire(ctx context.Context, host string, targetType storage.TargetType) (bool, error) {
	key := r.key(host, targetType)
	token := newLockToken()

	// SET key token NX PX ttl — SetNX 는 deprecated, SetArgs 의 NX mode 사용.
	err := r.rdb.SetArgs(ctx, key, token, goredis.SetArgs{
		Mode: "NX",
		TTL:  r.ttl,
	}).Err()
	if errors.Is(err, goredis.Nil) {
		return false, nil // 이미 다른 소유자가 획득 중
	}
	if err != nil {
		return false, fmt.Errorf("redis inflight acquire %s: %w", key, err)
	}

	r.mu.Lock()
	r.tokens[key] = token
	r.mu.Unlock()
	return true, nil
}

func (r *inflightLocker) Release(ctx context.Context, host string, targetType storage.TargetType) error {
	key := r.key(host, targetType)

	r.mu.Lock()
	token, ok := r.tokens[key]
	if ok {
		delete(r.tokens, key)
	}
	r.mu.Unlock()

	if !ok {
		return nil // 이 인스턴스가 소유하지 않음
	}

	// Lua 스크립트로 소유권 확인 후 원자적 삭제 — TTL 만료 후 재획득된 락을 삭제하지 않음.
	if err := r.rdb.Eval(ctx, luaRelease, []string{key}, token).Err(); err != nil && !errors.Is(err, goredis.Nil) {
		return fmt.Errorf("redis inflight release %s: %w", key, err)
	}
	return nil
}

func (r *inflightLocker) key(host string, targetType storage.TargetType) string {
	return inflightKeyPrefix + host + ":" + string(targetType)
}

func newLockToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
