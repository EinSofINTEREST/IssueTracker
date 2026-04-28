package redis

import (
	"context"
	"errors"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// RetryQueueZSetKey 는 retry job ID 를 ScheduledAt 기준으로 정렬해 보관하는 ZSET 키입니다.
const RetryQueueZSetKey = "retry:queue"

// retryEntryKeyPrefix 는 개별 retry job 의 직렬화 payload (job + lastError) 를 저장하는
// STRING 키의 공통 접두사입니다. 실제 키는 retry:entry:<jobID> 형식.
const retryEntryKeyPrefix = "retry:entry:"

// retryEntryTTL 은 retry payload 의 안전 보관 TTL 입니다.
// ZSET 에서 score 가 이 시점을 지나도 PopDue 가 호출되지 않으면 entry 가 자동 만료되어
// stale data 가 영구히 남지 않도록 합니다 (운영 안전망).
const retryEntryTTL = 24 * time.Hour

// ErrRetryEntryGone 은 ZSET 에는 jobID 가 있지만 entry payload 가 만료/삭제되어
// 더 이상 존재하지 않는 상태를 나타냅니다. 폴러는 이 케이스를 silent skip + WARN 으로
// 처리하고 ZSET 에서도 ZREM 하여 일관성을 회복해야 합니다.
var ErrRetryEntryGone = errors.New("retry entry payload missing")

// retryEntryKey 는 jobID 에 대응하는 entry 키를 반환합니다.
func retryEntryKey(jobID string) string {
	return retryEntryKeyPrefix + jobID
}

// EnqueueRetry 는 jobID 와 payload 를 retry 큐에 등록합니다.
//
//   - ZADD retry:queue scheduledAt jobID
//   - SET  retry:entry:<jobID> payload EX retryEntryTTL
//
// 동일 jobID 로 재호출 시 ZSET score 와 payload 모두 덮어씁니다 — 같은 job 이 여러 번
// 재시도 큐에 들어가면 가장 최근 호출 기준으로 정렬됩니다.
//
// scheduledAt 은 절대 시각 (time.Time) — UnixMilli 로 ZSET score 에 저장됩니다.
// (Redis ZSET score 는 float64. UnixNano (≈1.78e18, 2026 기준) 는 float64 mantissa 한계
// 2^53 (≈9e15) 를 초과해 정밀도 손실. UnixMilli (≈1.78e12) 는 안전 범위이며 retry 큐의
// ms 단위 정렬은 실무 충분.)
func (c *Client) EnqueueRetry(ctx context.Context, jobID string, payload []byte, scheduledAt time.Time) error {
	if jobID == "" {
		return fmt.Errorf("enqueue retry: empty jobID")
	}
	if len(payload) == 0 {
		return fmt.Errorf("enqueue retry %s: empty payload", jobID)
	}

	score := float64(scheduledAt.UnixMilli())

	// pipeline 으로 ZADD + SET 을 1 RTT 에 전송 — 두 명령 사이의 race window 를 최소화.
	// (완전 atomic 은 아니지만 폴러가 ErrRetryEntryGone 으로 자체 복구하므로 충분.)
	pipe := c.rdb.Pipeline()
	pipe.ZAdd(ctx, RetryQueueZSetKey, goredis.Z{Score: score, Member: jobID})
	pipe.Set(ctx, retryEntryKey(jobID), payload, retryEntryTTL)
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("enqueue retry %s: %w", jobID, err)
	}
	return nil
}

// DueRetry 는 PopDueRetries 의 단일 결과 항목입니다.
type DueRetry struct {
	JobID   string
	Payload []byte
}

// PopDueRetries 는 score (scheduledAt) 가 now 이하인 retry job 을 최대 limit 개 꺼내고
// ZSET / entry STRING 모두에서 제거합니다.
//
// 절차 (per item):
//  1. ZRANGEBYSCORE -inf~now LIMIT 0 limit  → 후보 jobID 목록
//  2. 각 jobID 에 대해: GET payload → ZREM + DEL (pipeline)
//     - GET 결과가 nil 이면 ErrRetryEntryGone 으로 분류, ZREM 만 수행하여 정합성 회복
//
// 다중 인스턴스 환경에서 동일 항목을 두 instance 가 동시에 ZRANGEBYSCORE 하는 race 가
// 가능하지만, 다운스트림 worker 의 JobLocker 가 중복 처리를 차단하므로 정합성에는 문제 없음.
// (Kafka publish 1~2회 발생 — 중복 처리는 JobLocker 가 silent skip)
//
// limit <= 0 이면 빈 슬라이스를 반환합니다.
func (c *Client) PopDueRetries(ctx context.Context, now time.Time, limit int) ([]DueRetry, error) {
	if limit <= 0 {
		return nil, nil
	}

	maxScore := fmt.Sprintf("%d", now.UnixMilli()) // EnqueueRetry 의 score 단위와 일치
	jobIDs, err := c.rdb.ZRangeArgs(ctx, goredis.ZRangeArgs{
		Key:     RetryQueueZSetKey,
		Start:   "-inf",
		Stop:    maxScore,
		ByScore: true,
		Offset:  0,
		Count:   int64(limit),
	}).Result()
	if err != nil {
		return nil, fmt.Errorf("zrange byscore retry queue: %w", err)
	}
	if len(jobIDs) == 0 {
		return nil, nil
	}

	out := make([]DueRetry, 0, len(jobIDs))
	for _, jobID := range jobIDs {
		key := retryEntryKey(jobID)
		payload, getErr := c.rdb.Get(ctx, key).Bytes()
		if errors.Is(getErr, goredis.Nil) {
			// payload 가 만료/삭제된 stale jobID 는 ZSET 에서도 정리.
			// 호출자에게는 silent skip — 별도 시그널 없이 다음 jobID 로 진행.
			if _, remErr := c.rdb.ZRem(ctx, RetryQueueZSetKey, jobID).Result(); remErr != nil {
				return out, fmt.Errorf("zrem stale retry %s: %w", jobID, remErr)
			}
			continue
		}
		if getErr != nil {
			return out, fmt.Errorf("get retry entry %s: %w", jobID, getErr)
		}

		// 정상 케이스: payload 확보 후 ZREM + DEL 을 pipeline 으로 한번에.
		pipe := c.rdb.Pipeline()
		pipe.ZRem(ctx, RetryQueueZSetKey, jobID)
		pipe.Del(ctx, key)
		if _, execErr := pipe.Exec(ctx); execErr != nil {
			return out, fmt.Errorf("zrem+del retry %s: %w", jobID, execErr)
		}

		out = append(out, DueRetry{JobID: jobID, Payload: payload})
	}

	return out, nil
}

// PendingRetryCount 는 retry 큐에 보관된 항목 수를 반환합니다 (모니터링/디버깅용).
func (c *Client) PendingRetryCount(ctx context.Context) (int64, error) {
	n, err := c.rdb.ZCard(ctx, RetryQueueZSetKey).Result()
	if err != nil {
		return 0, fmt.Errorf("zcard retry queue: %w", err)
	}
	return n, nil
}

// DeleteRetryEntryForTest 는 entry STRING 만 직접 삭제하여 stale 시나리오 (entry 만료 +
// ZSET 잔존) 를 인위적으로 재현하기 위한 테스트 전용 헬퍼입니다. 운영 코드 사용 금지.
func (c *Client) DeleteRetryEntryForTest(ctx context.Context, jobID string) error {
	if _, err := c.rdb.Del(ctx, retryEntryKey(jobID)).Result(); err != nil {
		return fmt.Errorf("delete retry entry %s: %w", jobID, err)
	}
	return nil
}
