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
// ZSET 에서 score 가 이 시점을 지나도 PeekDue 가 호출되지 않으면 entry 가 자동 만료되어
// stale data 가 영구히 남지 않도록 합니다 (운영 안전망).
const retryEntryTTL = 24 * time.Hour

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
	// (완전 atomic 은 아니지만 PeekDueRetries 가 GET=nil 인 stale jobID 를 자동 정리하므로 충분.)
	pipe := c.rdb.Pipeline()
	pipe.ZAdd(ctx, RetryQueueZSetKey, goredis.Z{Score: score, Member: jobID})
	pipe.Set(ctx, retryEntryKey(jobID), payload, retryEntryTTL)
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("enqueue retry %s: %w", jobID, err)
	}
	return nil
}

// DueRetry 는 PeekDueRetries 의 단일 결과 항목입니다.
type DueRetry struct {
	JobID   string
	Payload []byte
}

// PeekDueRetries 는 score (scheduledAt) 가 now 이하인 retry job 을 최대 limit 개 조회합니다.
// **항목을 ZSET 에서 제거하지 않습니다** — at-least-once 보장: Kafka publish 성공 후
// 호출자가 AckRetry 를 호출해 명시적으로 제거해야 합니다 (peek-publish-ack 패턴).
//
// 절차 (per item):
//  1. ZRANGEBYSCORE -inf~now LIMIT 0 limit  → 후보 jobID 목록 (ZSET 유지)
//  2. 각 jobID 에 대해 GET payload
//     - GET 결과가 nil 인 stale jobID 는 ZSET 에서 자동 정리하고 결과에 포함하지 않음
//
// 호출자 책임:
//   - publish 성공 → AckRetry(ctx, jobID) 로 ZSET/entry 제거
//   - publish 실패 → 아무것도 하지 않으면 다음 폴 사이클에 자동 재peek + 재시도
//     (또는 EnqueueRetry 재호출로 ScheduledAt 을 미래로 옮겨 backoff 적용)
//
// 프로세스 crash 안전성: peek 후 ack 전에 crash 가 발생해도 항목이 ZSET 에 남아 있으므로
// 다른 인스턴스 / 재기동 시 다시 peek 됩니다 — at-least-once 보장.
//
// 다중 인스턴스 race: 동시에 같은 jobID 를 peek + publish 가능 → Kafka 에 1~2회 중복
// publish 발생 → 다운스트림 JobLocker 가 흡수 (정합성 문제 없음).
//
// limit <= 0 이면 빈 슬라이스를 반환합니다.
func (c *Client) PeekDueRetries(ctx context.Context, now time.Time, limit int) ([]DueRetry, error) {
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

	// payload 들을 1 RTT 로 GET — 항목 N 개에 대해 round-trip N 번 → 1 번으로 압축
	// (Copilot 피드백: 폴러 부하 / Redis latency 영향 완화).
	type getResult struct {
		jobID string
		cmd   *goredis.StringCmd
	}
	getPipe := c.rdb.Pipeline()
	results := make([]getResult, 0, len(jobIDs))
	for _, jobID := range jobIDs {
		results = append(results, getResult{
			jobID: jobID,
			cmd:   getPipe.Get(ctx, retryEntryKey(jobID)),
		})
	}
	// pipeline Exec 자체는 성공해도 개별 cmd 가 goredis.Nil 일 수 있으므로 그 에러는 무시.
	// (개별 결과 검사에서 errors.Is(_, goredis.Nil) 로 stale 판정)
	if _, execErr := getPipe.Exec(ctx); execErr != nil && !errors.Is(execErr, goredis.Nil) {
		return nil, fmt.Errorf("pipeline get retry entries: %w", execErr)
	}

	out := make([]DueRetry, 0, len(jobIDs))
	staleIDs := make([]string, 0)
	for _, r := range results {
		payload, getErr := r.cmd.Bytes()
		if errors.Is(getErr, goredis.Nil) {
			// payload 가 만료/삭제된 stale jobID — 마지막에 batch ZREM 으로 일괄 정리.
			staleIDs = append(staleIDs, r.jobID)
			continue
		}
		if getErr != nil {
			return out, fmt.Errorf("get retry entry %s: %w", r.jobID, getErr)
		}
		out = append(out, DueRetry{JobID: r.jobID, Payload: payload})
	}

	// stale jobID 들을 1 RTT 로 일괄 ZREM (정합성 회복).
	if len(staleIDs) > 0 {
		members := make([]interface{}, 0, len(staleIDs))
		for _, id := range staleIDs {
			members = append(members, id)
		}
		if _, remErr := c.rdb.ZRem(ctx, RetryQueueZSetKey, members...).Result(); remErr != nil {
			return out, fmt.Errorf("batch zrem stale retries: %w", remErr)
		}
	}

	return out, nil
}

// AckRetry 는 publish 성공한 retry job 을 ZSET 과 entry STRING 에서 제거합니다.
// PeekDueRetries 후 publish 성공 시 반드시 호출해야 합니다 — 미호출 시 다음 폴 사이클에
// 동일 jobID 가 재peek 되어 중복 publish 발생 (JobLocker 가 흡수).
//
// idempotent: 이미 제거된 항목에 대한 호출도 에러 없이 통과합니다.
func (c *Client) AckRetry(ctx context.Context, jobID string) error {
	if jobID == "" {
		return fmt.Errorf("ack retry: empty jobID")
	}
	pipe := c.rdb.Pipeline()
	pipe.ZRem(ctx, RetryQueueZSetKey, jobID)
	pipe.Del(ctx, retryEntryKey(jobID))
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("ack retry %s: %w", jobID, err)
	}
	return nil
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
//
// 운영 빌드에서 노출되지만 `ForTest` 접미어로 의도를 명시. 본 프로젝트 컨벤션상
// 테스트 파일이 `test/pkg/redis/` 별도 디렉토리에 위치하므로 (`package redis_test`),
// 동일 패키지 내 `_test.go` helper 는 외부 테스트에서 접근 불가 — 부득이 exported
// API 로 노출합니다.
func (c *Client) DeleteRetryEntryForTest(ctx context.Context, jobID string) error {
	if _, err := c.rdb.Del(ctx, retryEntryKey(jobID)).Result(); err != nil {
		return fmt.Errorf("delete retry entry %s: %w", jobID, err)
	}
	return nil
}
