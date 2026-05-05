package llmgen

import (
	"context"
	"encoding/json"
	"fmt"

	goredis "github.com/redis/go-redis/v9"
	"issuetracker/internal/processor/fetcher/core"
	"issuetracker/internal/storage"
)

const pendingKeyPrefix = "llmgen:pending:"

// PendingItem 은 in-flight 중 대기 중인 URL 의 재투입에 필요한 정보입니다 (이슈 #262).
type PendingItem struct {
	RawRef        core.RawContentRef `json:"raw_ref"`
	CrawlerName   string             `json:"crawler_name"`
	LLMRetryCount int                `json:"llm_retry_count"`
	TargetType    storage.TargetType `json:"target_type"`
}

// RequeueFunc 는 pending 대기 URL 목록을 파서 워커에 재투입하는 콜백 타입입니다 (이슈 #262).
type RequeueFunc func(ctx context.Context, items []PendingItem)

// PendingQueue 는 (host, targetType) 단위 대기 URL 목록을 저장/조회하는 인터페이스입니다 (이슈 #262).
type PendingQueue interface {
	// Push 는 대기 항목을 큐에 적재합니다.
	Push(ctx context.Context, host string, targetType storage.TargetType, item PendingItem) error
	// Flush 는 큐의 모든 항목을 원자적으로 꺼내 반환합니다. 반환 후 큐는 비어 있습니다.
	Flush(ctx context.Context, host string, targetType storage.TargetType) ([]PendingItem, error)
}

// ─────────────────────────────────────────────────────────────────────────────
// RedisPendingQueue — Redis LIST 기반 구현 (이슈 #262)
// ─────────────────────────────────────────────────────────────────────────────

// luaFlush 는 LRANGE + DEL 을 원자적으로 수행하는 Lua 스크립트입니다.
// 두 명령 사이에 다른 RPUSH 가 끼어들어도 안전하게 스냅샷을 꺼냅니다.
const luaFlush = `
local items = redis.call("LRANGE", KEYS[1], 0, -1)
redis.call("DEL", KEYS[1])
return items`

// RedisPendingQueue 는 Redis LIST 기반 PendingQueue 구현입니다.
type RedisPendingQueue struct {
	rdb *goredis.Client
}

// NewRedisPendingQueue 는 RedisPendingQueue 를 생성합니다.
func NewRedisPendingQueue(rdb *goredis.Client) *RedisPendingQueue {
	return &RedisPendingQueue{rdb: rdb}
}

func (r *RedisPendingQueue) Push(ctx context.Context, host string, targetType storage.TargetType, item PendingItem) error {
	data, err := json.Marshal(item)
	if err != nil {
		return fmt.Errorf("marshal pending item: %w", err)
	}
	key := r.key(host, targetType)
	if err := r.rdb.RPush(ctx, key, data).Err(); err != nil {
		return fmt.Errorf("redis pending push %s: %w", key, err)
	}
	return nil
}

func (r *RedisPendingQueue) Flush(ctx context.Context, host string, targetType storage.TargetType) ([]PendingItem, error) {
	key := r.key(host, targetType)
	result, err := r.rdb.Eval(ctx, luaFlush, []string{key}).StringSlice()
	if err != nil && err != goredis.Nil {
		return nil, fmt.Errorf("redis pending flush %s: %w", key, err)
	}

	items := make([]PendingItem, 0, len(result))
	for _, raw := range result {
		var item PendingItem
		if err := json.Unmarshal([]byte(raw), &item); err != nil {
			continue // 손상된 항목은 skip — best-effort
		}
		items = append(items, item)
	}
	return items, nil
}

func (r *RedisPendingQueue) key(host string, targetType storage.TargetType) string {
	return pendingKeyPrefix + host + ":" + string(targetType)
}
