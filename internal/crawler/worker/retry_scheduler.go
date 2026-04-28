package worker

import (
	"context"
	"fmt"

	"issuetracker/internal/crawler/core"
	"issuetracker/pkg/queue"
)

// RetryScheduler 는 처리 실패한 CrawlJob 의 재시도 발행 시점을 관리하는 인터페이스입니다
// (이슈 #82).
//
// 두 가지 구현 전략을 추상화합니다:
//   - KafkaImmediateRetryScheduler: 즉시 Kafka 에 재발행하고 worker 가 ScheduledAt 까지
//     대기 — 기존 동작 보존 (worker 슬롯 점유 문제 그대로)
//   - RedisDelayedRetryScheduler: Redis ZSET 에 보관하고 별도 goroutine 이 ScheduledAt
//     도달 시 Kafka 에 발행 — worker 슬롯 점유 회피
//
// 호출자 (worker pool) 는 ScheduledAt 과 RetryCount 를 미리 셋팅한 job 을 전달합니다.
// 구현체는 lastErr 의 메시지를 last-error 헤더로 보존해야 합니다.
type RetryScheduler interface {
	Enqueue(ctx context.Context, job *core.CrawlJob, lastErr error) error
}

// KafkaImmediateRetryScheduler 는 기존 worker 의 inline requeue 동작을 그대로 옮긴
// 기본/fallback 구현체입니다 (Redis 부재 시 사용).
//
// 흐름:
//  1. job 을 Marshal
//  2. priority 토픽으로 즉시 publish (ScheduledAt 은 미래 시각으로 셋팅된 상태)
//  3. worker 가 메시지 fetch 후 processJob 진입 시 ScheduledAt 까지 sleep — 워커 슬롯 점유
//
// 본 구현은 이슈 #82 가 지적한 처리량 급감을 그대로 갖지만, Redis 미설정 환경 (단일 인스턴스
// 개발/테스트, 통합 redis 장애) 에서 retry 자체는 동작하도록 보존합니다.
type KafkaImmediateRetryScheduler struct {
	producer queue.Producer
}

// NewKafkaImmediateRetryScheduler 는 KafkaImmediateRetryScheduler 를 생성합니다.
func NewKafkaImmediateRetryScheduler(producer queue.Producer) *KafkaImmediateRetryScheduler {
	return &KafkaImmediateRetryScheduler{producer: producer}
}

// Enqueue 는 job 을 priority 토픽에 즉시 publish 합니다.
func (s *KafkaImmediateRetryScheduler) Enqueue(ctx context.Context, job *core.CrawlJob, lastErr error) error {
	data, err := job.Marshal()
	if err != nil {
		return fmt.Errorf("marshal job for retry: %w", err)
	}

	msg := queue.Message{
		Topic:   topicForPriority(job.Priority),
		Key:     []byte(job.ID),
		Value:   data,
		Headers: retryHeaders(job, lastErr),
	}

	if err := s.producer.Publish(ctx, msg); err != nil {
		return fmt.Errorf("publish retry job %s: %w", job.ID, err)
	}
	return nil
}

// retryHeaders 는 retry-count / last-error 표준 헤더를 구성합니다 — 두 구현체가 공유.
func retryHeaders(job *core.CrawlJob, lastErr error) map[string]string {
	h := map[string]string{
		"retry-count": fmt.Sprintf("%d", job.RetryCount),
	}
	if lastErr != nil {
		h["last-error"] = lastErr.Error()
	}
	return h
}
