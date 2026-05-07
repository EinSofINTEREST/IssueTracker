package redisstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"issuetracker/internal/storage"
)

// DefaultPendingTTL 은 pending LIST 키의 기본 TTL 입니다.
// rule 생성이 계속 실패해도 Redis 메모리가 무기한 증가하지 않도록 제한.
const DefaultPendingTTL = 24 * time.Hour

// DefaultPendingMaxLen 은 (host, targetType) 당 pending LIST 의 최대 항목 수입니다.
// noisy host 하나가 Redis 메모리를 독점하지 않도록 상한.
const DefaultPendingMaxLen = 1000

const pendingKeyPrefix = "llmgen:pending:"

// luaFlush 는 LRANGE + DEL 을 원자적으로 수행하는 Lua 스크립트입니다.
// 두 명령 사이에 다른 RPUSH 가 끼어들어도 안전하게 스냅샷을 꺼냅니다.
const luaFlush = `
local items = redis.call("LRANGE", KEYS[1], 0, -1)
redis.call("DEL", KEYS[1])
return items`

// pendingQueue 는 Redis LIST 기반 PendingQueue 구현입니다.
//
// 운영 정책:
//   - TTL: 기본 24h — rule 생성이 무한히 실패해도 Redis 메모리 증가 제한
//   - 최대 길이: 기본 1000 — 단일 noisy host 가 메모리를 독점하지 않도록
//   - Push 시 길이 초과면 error 반환 (drop) — 호출자가 graceful 분기
type pendingQueue struct {
	rdb    *goredis.Client
	ttl    time.Duration
	maxLen int64
}

// NewPendingQueue 는 기본 TTL(24h) + 최대 길이(1000) 로 PendingQueue 를 생성합니다.
//
// rdb 가 nil 이면 error — wiring 시 panic-on-nil 정책 (다른 redisstore 생성자와 일관).
func NewPendingQueue(rdb *goredis.Client) (storage.PendingQueue, error) {
	if rdb == nil {
		return nil, errors.New("redisstore: NewPendingQueue requires non-nil redis client")
	}
	return &pendingQueue{
		rdb:    rdb,
		ttl:    DefaultPendingTTL,
		maxLen: DefaultPendingMaxLen,
	}, nil
}

func (r *pendingQueue) Push(ctx context.Context, host string, targetType storage.TargetType, payload []byte) error {
	key := r.key(host, targetType)

	// 길이 상한 초과 시 skip — noisy host 가 Redis 메모리를 독점하지 않도록.
	llen, err := r.rdb.LLen(ctx, key).Result()
	if err != nil && !errors.Is(err, goredis.Nil) {
		return fmt.Errorf("redis pending llen %s: %w", key, err)
	}
	if llen >= r.maxLen {
		return fmt.Errorf("redis pending queue full (%s, len=%d): item dropped", key, llen)
	}

	// RPUSH + EXPIRE 를 pipeline 으로 묶어 TTL 갱신 보장.
	pipe := r.rdb.Pipeline()
	pipe.RPush(ctx, key, payload)
	pipe.Expire(ctx, key, r.ttl)
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("redis pending push %s: %w", key, err)
	}
	return nil
}

func (r *pendingQueue) Flush(ctx context.Context, host string, targetType storage.TargetType) ([][]byte, error) {
	key := r.key(host, targetType)
	result, err := r.rdb.Eval(ctx, luaFlush, []string{key}).StringSlice()
	if err != nil && !errors.Is(err, goredis.Nil) {
		return nil, fmt.Errorf("redis pending flush %s: %w", key, err)
	}

	out := make([][]byte, 0, len(result))
	for _, raw := range result {
		out = append(out, []byte(raw))
	}
	return out, nil
}

func (r *pendingQueue) key(host string, targetType storage.TargetType) string {
	return pendingKeyPrefix + host + ":" + string(targetType)
}
