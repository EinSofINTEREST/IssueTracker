// call_budget.go 는 Redis sorted set 기반 sliding window 호출 예산 구현체입니다 (이슈 #169).
//
// LLM rule generator 는 룰이 없는 host 를 만날 때마다 LLM 을 호출합니다. 신규 사이트가
// 몰리거나 룰 생성이 반복 실패하면 호출이 급증해 무료 한도(예: Gemini 1000/day)를
// 넘길 수 있습니다. 본 카운터가 일/시간 단위 상한을 강제합니다.
//
// failure_counter.go 와 동일한 sliding window 기법을 씁니다 — 고정 윈도(예: 자정 리셋)는
// 경계 직전에 두 배가 몰리는 문제가 있어 sliding 을 택했습니다.
//
// 멀티 인스턴스에서도 Redis 를 공유하므로 **한도가 인스턴스 수만큼 늘어나지 않습니다** —
// in-memory fallback 과의 핵심 차이입니다.

package redisstore

import (
	"context"
	"fmt"
	"strconv"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"issuetracker/pkg/logger"
)

// callBudget 는 Redis sorted set 기반 sliding window 예산 카운터입니다.
type callBudget struct {
	client    *goredis.Client
	keyPrefix string
	log       *logger.Logger

	// now 는 테스트 seam. nil 이면 time.Now.
	now func() time.Time
}

// NewCallBudget 는 Redis 기반 호출 예산 카운터를 생성합니다.
//
// keyPrefix 가 빈 문자열이면 "llmgen:budget".
func NewCallBudget(client *goredis.Client, keyPrefix string, log *logger.Logger) (*callBudget, error) {
	if client == nil {
		return nil, fmt.Errorf("redisstore: call budget requires non-nil redis client")
	}
	if keyPrefix == "" {
		keyPrefix = "llmgen:budget"
	}
	return &callBudget{client: client, keyPrefix: keyPrefix, log: log}, nil
}

func (b *callBudget) nowFn() time.Time {
	if b.now != nil {
		return b.now()
	}
	return time.Now()
}

func (b *callBudget) keyFor(window string) string {
	return b.keyPrefix + ":" + window
}

// Count 는 window 안의 현재 호출 수를 반환합니다 (기록 없이 조회만).
//
// 잔여 한도 metric 노출에 씁니다.
func (b *callBudget) Count(ctx context.Context, window string, span time.Duration) (int, error) {
	key := b.keyFor(window)
	cutoff := b.nowFn().Add(-span)

	pipe := b.client.Pipeline()
	pipe.ZRemRangeByScore(ctx, key, "-inf", strconv.FormatInt(cutoff.UnixNano(), 10))
	zcard := pipe.ZCard(ctx, key)
	if _, err := pipe.Exec(ctx); err != nil {
		return 0, fmt.Errorf("redis call budget count (%s): %w", window, err)
	}
	return int(zcard.Val()), nil
}

// Record 는 호출 1건을 기록하고 window 안의 총 수를 반환합니다.
//
// member 는 timestamp + nonce — 같은 나노초에 두 호출이 겹쳐도 ZADD 가 중복으로
// 흡수해 카운트가 누락되는 일이 없도록 합니다 (failure_counter 와 동일 대응).
func (b *callBudget) Record(ctx context.Context, window string, span time.Duration) (int, error) {
	now := b.nowFn()
	key := b.keyFor(window)
	cutoff := now.Add(-span)

	member := strconv.FormatInt(now.UnixNano(), 10) + ":" + randNonce()

	pipe := b.client.Pipeline()
	pipe.ZAdd(ctx, key, goredis.Z{Score: float64(now.UnixNano()), Member: member})
	pipe.ZRemRangeByScore(ctx, key, "-inf", strconv.FormatInt(cutoff.UnixNano(), 10))
	// EXPIRE: window 의 2배 — 장기 미사용 시 key 자연 정리.
	pipe.Expire(ctx, key, 2*span)
	zcard := pipe.ZCard(ctx, key)

	if _, err := pipe.Exec(ctx); err != nil {
		return 0, fmt.Errorf("redis call budget record (%s): %w", window, err)
	}
	return int(zcard.Val()), nil
}
