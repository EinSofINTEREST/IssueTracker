package redis

import (
	"context"
	"errors"
	"fmt"

	goredis "github.com/redis/go-redis/v9"
)

// JobBufferKeyPrefix 는 normal/low priority crawl job 의 직렬화 Kafka payload 를 임시 적재하는
// Redis LIST 키의 공통 접두사입니다 (이슈 #510). 실제 키는 publisher:buffer:<priority> 형식.
//
// 자료구조 — LIST (FIFO):
//   - LPUSH 로 head 에 enqueue
//   - RPOP COUNT N 로 tail 에서 drain (가장 오래된 항목부터)
//   - LLEN 으로 모니터링
const JobBufferKeyPrefix = "publisher:buffer:"

// JobBufferKey 는 priority label 에 대응하는 buffer LIST 키를 반환합니다.
// label 은 호출자 컨벤션 — 일반적으로 "normal" / "low".
func JobBufferKey(label string) string {
	return JobBufferKeyPrefix + label
}

// EnqueueJob 은 payload 를 priority label 버퍼의 head 에 추가합니다.
//
// MaxLen > 0 이면 LPUSH 후 LTRIM 으로 LIST 길이를 MaxLen 이하로 보정 — buffer 무한 누적을 방어.
// MaxLen <= 0 이면 길이 제한 없음 (운영 cautious 시 0 가능, 단 metric 으로 모니터링 필수).
//
// LTRIM 은 oldest 부터 제거 — buffer 가 임계 도달 시 가장 오래된 (= drain 기회를 가장 오래 못 받은)
// 항목이 소실되지만, 정책상 신규 publish 우선. 호출자는 enqueue 직전 IngestionLock 로 dedup 책임.
//
// label / payload 빈 값은 명시적 error — silent corruption 회피.
//
// 단일 payload 의 thin wrapper — 내부에서 EnqueueBatch 로 위임 (single source of truth).
func (c *Client) EnqueueJob(ctx context.Context, label string, payload []byte, maxLen int64) error {
	if len(payload) == 0 {
		return fmt.Errorf("enqueue job %s: empty payload", label)
	}
	return c.EnqueueBatch(ctx, label, [][]byte{payload}, maxLen)
}

// EnqueueBatch 는 payloads 슬라이스 전체를 단일 Redis pipeline 으로 LPUSH 합니다 (gemini PR #511 피드백).
//
// 동작:
//  1. 빈 payloads → 즉시 (nil, nil) 반환 (no-op, idempotent)
//  2. 모든 payloads 를 LPUSH 의 variadic args 로 1 RTT 전송
//  3. MaxLen > 0 이면 동일 pipeline 에 LTRIM 추가 — 2 명령 1 RTT
//
// LPUSH 의 multi-arg 의미: LPUSH key v1 v2 v3 → list head 가 [v3, v2, v1, ...prev] 순서.
// 즉 같은 호출 안의 마지막 인자가 head 최우선. 본 함수는 입력 순서 보존을 목표로 하지 않음 —
// drainer 가 batch 단위로 처리하면 충분.
//
// label 빈 값 / 개별 payload 빈 값 → 명시적 error (silent corruption 회피).
// payloads 길이 0 은 정상 (no-op).
func (c *Client) EnqueueBatch(ctx context.Context, label string, payloads [][]byte, maxLen int64) error {
	if label == "" {
		return errors.New("enqueue batch: empty label")
	}
	if len(payloads) == 0 {
		return nil
	}

	key := JobBufferKey(label)
	args := make([]interface{}, len(payloads))
	for i, p := range payloads {
		if len(p) == 0 {
			return fmt.Errorf("enqueue batch %s: empty payload at index %d", label, i)
		}
		args[i] = p
	}

	pipe := c.rdb.Pipeline()
	pipe.LPush(ctx, key, args...)
	if maxLen > 0 {
		pipe.LTrim(ctx, key, 0, maxLen-1)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("enqueue batch %s: %w", label, err)
	}
	return nil
}

// DrainJobs 는 priority label 버퍼의 tail 에서 최대 n 개의 payload 를 pop 하여 반환합니다.
//
// RPOP COUNT n (Redis 6.2+) 으로 1 RTT 에 일괄 처리 — N 회 round-trip 회피.
// 반환 순서: tail 우선 (= 가장 오래된 순) → FIFO 보장.
//
// n <= 0 이면 빈 슬라이스 반환.
// LIST 가 비어 있으면 빈 슬라이스 반환 (error 아님 — 정상 idle).
//
// **at-most-once 의미**: drain 된 항목은 즉시 Redis 에서 제거됨. drainer 가 Kafka publish 에
// 실패한 경우 호출자가 다시 EnqueueJob 으로 재적재 — peek-publish-ack 가 아닌 pop-publish 패턴
// (단순성 우선). publish 실패 시 호출자가 재적재 책임.
//
// 다중 인스턴스 race: 동일 LIST 에서 RPOP 은 atomic — 같은 항목이 두 인스턴스에 동시 반환 안 됨.
// 두 drainer 인스턴스가 동작해도 각자 다른 항목 처리.
func (c *Client) DrainJobs(ctx context.Context, label string, n int) ([][]byte, error) {
	if n <= 0 {
		return nil, nil
	}
	if label == "" {
		return nil, errors.New("drain jobs: empty label")
	}

	key := JobBufferKey(label)

	res, err := c.rdb.RPopCount(ctx, key, n).Result()
	if errors.Is(err, goredis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("drain jobs %s: %w", label, err)
	}

	out := make([][]byte, 0, len(res))
	for _, s := range res {
		out = append(out, []byte(s))
	}
	return out, nil
}

// JobBufferLen 은 priority label 버퍼의 현재 길이를 반환합니다 (모니터링/디버깅용).
// 키가 부재하면 (0, nil) 반환.
func (c *Client) JobBufferLen(ctx context.Context, label string) (int64, error) {
	if label == "" {
		return 0, errors.New("job buffer len: empty label")
	}
	n, err := c.rdb.LLen(ctx, JobBufferKey(label)).Result()
	if err != nil {
		return 0, fmt.Errorf("llen %s: %w", JobBufferKey(label), err)
	}
	return n, nil
}
