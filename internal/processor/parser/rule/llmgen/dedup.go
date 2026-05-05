package llmgen

import (
	"context"
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

// inflightKey 는 (host, target_type) 튜플입니다 — DB FindActiveCandidates 인자와 1:1 매칭.
type inflightKey struct {
	host       string
	targetType storage.TargetType
}

// InflightLocker 는 (host, targetType) 단위 중복 실행 방지 인터페이스입니다 (이슈 #261).
//
// 구현체:
//   - memInflightLocker: in-process map 기반 (기본값, 단일 인스턴스 환경)
//   - RedisInflightLocker: Redis SETNX+TTL 기반 (다중 인스턴스 환경)
type InflightLocker interface {
	// TryAcquire 는 슬롯 획득을 시도합니다.
	// acquired=true: 호출자가 작업 진행 + 완료 후 Release 책임.
	// acquired=false: 다른 goroutine/인스턴스가 이미 처리 중 — skip.
	TryAcquire(ctx context.Context, host string, targetType storage.TargetType) (acquired bool, err error)
	// Release 는 획득한 슬롯을 해제합니다.
	Release(ctx context.Context, host string, targetType storage.TargetType) error
}

// ─────────────────────────────────────────────────────────────────────────────
// memInflightLocker — in-process 기본 구현
// ─────────────────────────────────────────────────────────────────────────────

type memInflightLocker struct {
	mu      sync.Mutex
	pending map[inflightKey]struct{}
}

func newMemInflightLocker() *memInflightLocker {
	return &memInflightLocker{pending: make(map[inflightKey]struct{})}
}

func (m *memInflightLocker) TryAcquire(_ context.Context, host string, targetType storage.TargetType) (bool, error) {
	key := inflightKey{host: host, targetType: targetType}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.pending[key]; exists {
		return false, nil
	}
	m.pending[key] = struct{}{}
	return true, nil
}

func (m *memInflightLocker) Release(_ context.Context, host string, targetType storage.TargetType) error {
	key := inflightKey{host: host, targetType: targetType}
	m.mu.Lock()
	delete(m.pending, key)
	m.mu.Unlock()
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// RedisInflightLocker — 분산 Lock 구현 (이슈 #261)
// ─────────────────────────────────────────────────────────────────────────────

// RedisInflightLocker 는 Redis SETNX+TTL 기반 분산 Lock 구현입니다.
//
// 다중 인스턴스 환경에서 동일 (host, targetType) 에 대한 LLM 호출을 1회로 제한합니다.
// TTL 은 프로세스 크래시 후 stuck 슬롯 자동 해제를 보장합니다.
type RedisInflightLocker struct {
	rdb *goredis.Client
	ttl time.Duration
}

// NewRedisInflightLocker 는 RedisInflightLocker 를 생성합니다.
func NewRedisInflightLocker(rdb *goredis.Client, ttl time.Duration) *RedisInflightLocker {
	return &RedisInflightLocker{rdb: rdb, ttl: ttl}
}

func (r *RedisInflightLocker) TryAcquire(ctx context.Context, host string, targetType storage.TargetType) (bool, error) {
	key := r.key(host, targetType)
	ok, err := r.rdb.SetNX(ctx, key, 1, r.ttl).Result()
	if err != nil {
		return false, fmt.Errorf("redis inflight acquire %s: %w", key, err)
	}
	return ok, nil
}

func (r *RedisInflightLocker) Release(ctx context.Context, host string, targetType storage.TargetType) error {
	key := r.key(host, targetType)
	if err := r.rdb.Del(ctx, key).Err(); err != nil {
		return fmt.Errorf("redis inflight release %s: %w", key, err)
	}
	return nil
}

func (r *RedisInflightLocker) key(host string, targetType storage.TargetType) string {
	return inflightKeyPrefix + host + ":" + string(targetType)
}
