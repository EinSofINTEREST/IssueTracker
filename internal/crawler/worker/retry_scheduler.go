package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"issuetracker/internal/crawler/core"
	"issuetracker/pkg/logger"
	"issuetracker/pkg/queue"
	pkgredis "issuetracker/pkg/redis"
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

// ─────────────────────────────────────────────────────────────────────────────
// Redis-backed delayed retry scheduler (이슈 #82 본 PR 의 핵심)
// ─────────────────────────────────────────────────────────────────────────────

// retryQueueClient 는 RedisDelayedRetryScheduler 가 사용하는 Redis 연산을 추상화합니다.
// pkg/redis.Client 가 구조적으로 만족하며, 테스트는 mock 으로 교체합니다.
type retryQueueClient interface {
	EnqueueRetry(ctx context.Context, jobID string, payload []byte, scheduledAt time.Time) error
	PopDueRetries(ctx context.Context, now time.Time, limit int) ([]pkgredis.DueRetry, error)
}

// RedisRetrySchedulerConfig 는 RedisDelayedRetryScheduler 의 polling 동작을 제어합니다.
type RedisRetrySchedulerConfig struct {
	// PollInterval: 폴링 주기 (default: 1s). 너무 짧으면 Redis 부하, 너무 길면 retry latency 상승
	PollInterval time.Duration

	// BatchSize: 한 폴 사이클에서 가져오는 최대 due 항목 수 (default: 50)
	BatchSize int

	// RepublishFailureBackoff: Kafka publish 실패 시 동일 항목을 재 enqueue 할 지연
	// (default: 1s). 일시적 Kafka 장애 흡수용 — Redis enqueue 까지 실패하면 데이터 손실
	RepublishFailureBackoff time.Duration
}

// DefaultRedisRetrySchedulerConfig 는 운영 기본값을 반환합니다.
func DefaultRedisRetrySchedulerConfig() RedisRetrySchedulerConfig {
	return RedisRetrySchedulerConfig{
		PollInterval:            1 * time.Second,
		BatchSize:               50,
		RepublishFailureBackoff: 1 * time.Second,
	}
}

// RedisDelayedRetryScheduler 는 Redis ZSET 에 retry 를 보관하고 별도 goroutine 이
// ScheduledAt 도달 항목을 Kafka 에 발행하는 구현체입니다 (이슈 #82).
//
// 핵심 효과: requeue 는 Redis 에만 저장되므로 worker 가 메시지를 소비한 뒤 sleep 하지
// 않고 즉시 다음 정상 job 처리로 넘어갑니다. 워커 슬롯 점유 문제 해소.
//
// 영속성: Redis ZSET 보관 + entry STRING 24h TTL. 프로세스 crash 시에도 다른 인스턴스가
// 이어 받아 처리 가능 (다중 인스턴스 race 는 다운스트림 JobLocker 가 흡수).
//
// 라이프사이클: New → Run(ctx) (별도 goroutine) → ctx cancel → goroutine 정리 → Stop 대기.
type RedisDelayedRetryScheduler struct {
	client   retryQueueClient
	producer queue.Producer
	cfg      RedisRetrySchedulerConfig
	log      *logger.Logger
	wg       sync.WaitGroup
}

// NewRedisDelayedRetryScheduler 는 RedisDelayedRetryScheduler 를 생성합니다.
// cfg 의 0 값 필드는 DefaultRedisRetrySchedulerConfig 로 보정합니다.
func NewRedisDelayedRetryScheduler(client retryQueueClient, producer queue.Producer, cfg RedisRetrySchedulerConfig, log *logger.Logger) *RedisDelayedRetryScheduler {
	def := DefaultRedisRetrySchedulerConfig()
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = def.PollInterval
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = def.BatchSize
	}
	if cfg.RepublishFailureBackoff <= 0 {
		cfg.RepublishFailureBackoff = def.RepublishFailureBackoff
	}
	return &RedisDelayedRetryScheduler{
		client:   client,
		producer: producer,
		cfg:      cfg,
		log:      log,
	}
}

// Enqueue 는 job 을 Redis ZSET 에 등록합니다. job.ScheduledAt 이 도달하면 Run 루프가
// Kafka 에 발행합니다.
//
// payload 는 {job: <marshaled>, last_err: <string>} JSON — Run 시 동일 헤더 재구성을 위해
// 두 정보를 모두 보존.
func (s *RedisDelayedRetryScheduler) Enqueue(ctx context.Context, job *core.CrawlJob, lastErr error) error {
	jobBytes, err := job.Marshal()
	if err != nil {
		return fmt.Errorf("marshal job for redis retry: %w", err)
	}

	entry := redisRetryEntry{
		JobBytes: jobBytes,
		LastErr:  lastErrString(lastErr),
	}
	payload, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal retry entry %s: %w", job.ID, err)
	}

	if err := s.client.EnqueueRetry(ctx, job.ID, payload, job.ScheduledAt); err != nil {
		return fmt.Errorf("enqueue retry %s: %w", job.ID, err)
	}
	return nil
}

// Run 은 ctx 가 cancel 될 때까지 polling 을 반복합니다. Stop 이 wg 를 join 하므로
// 별도 goroutine 으로 호출하세요.
func (s *RedisDelayedRetryScheduler) Run(ctx context.Context) {
	s.wg.Add(1)
	defer s.wg.Done()

	ticker := time.NewTicker(s.cfg.PollInterval)
	defer ticker.Stop()

	s.log.WithFields(map[string]interface{}{
		"poll_interval_ms": s.cfg.PollInterval.Milliseconds(),
		"batch_size":       s.cfg.BatchSize,
	}).Info("redis delayed retry scheduler started")

	for {
		select {
		case <-ctx.Done():
			s.log.Info("redis delayed retry scheduler stopped")
			return
		case <-ticker.C:
			s.pollOnce(ctx)
		}
	}
}

// Stop 은 Run goroutine 이 종료될 때까지 대기합니다 (ctx cancel 후 호출).
func (s *RedisDelayedRetryScheduler) Stop() {
	s.wg.Wait()
}

// pollOnce 는 due 항목을 한 batch 만큼 가져와 Kafka 에 발행합니다.
func (s *RedisDelayedRetryScheduler) pollOnce(ctx context.Context) {
	due, err := s.client.PopDueRetries(ctx, time.Now(), s.cfg.BatchSize)
	if err != nil {
		// ctx cancel 로 인한 에러는 정상 종료 흐름 — DEBUG 로 강등
		if ctx.Err() != nil {
			s.log.WithError(err).Debug("retry pop interrupted by shutdown")
			return
		}
		s.log.WithError(err).Warn("failed to pop due retries from redis")
		return
	}

	for _, item := range due {
		s.republish(ctx, item)
	}
}

// republish 는 단일 due 항목을 Kafka 에 발행합니다. 실패 시 item 을 짧은 backoff 로
// 재 enqueue — Kafka 일시 장애 흡수.
func (s *RedisDelayedRetryScheduler) republish(ctx context.Context, item pkgredis.DueRetry) {
	var entry redisRetryEntry
	if err := json.Unmarshal(item.Payload, &entry); err != nil {
		// payload 손상 — 복구 불가, drop + ERROR (운영 이상 신호)
		s.log.WithFields(map[string]interface{}{
			"job_id": item.JobID,
		}).WithError(err).Error("malformed retry payload, dropping")
		return
	}

	var job core.CrawlJob
	if err := json.Unmarshal(entry.JobBytes, &job); err != nil {
		s.log.WithFields(map[string]interface{}{
			"job_id": item.JobID,
		}).WithError(err).Error("malformed retry job bytes, dropping")
		return
	}

	var lastErr error
	if entry.LastErr != "" {
		lastErr = errors.New(entry.LastErr)
	}

	msg := queue.Message{
		Topic:   topicForPriority(job.Priority),
		Key:     []byte(item.JobID),
		Value:   entry.JobBytes,
		Headers: retryHeaders(&job, lastErr),
	}

	if err := s.producer.Publish(ctx, msg); err != nil {
		// Kafka publish 실패 — Redis 에 짧은 backoff 로 재 enqueue 하여 다음 폴 사이클에 재시도
		s.log.WithFields(map[string]interface{}{
			"job_id":     item.JobID,
			"crawler":    job.CrawlerName,
			"backoff_ms": s.cfg.RepublishFailureBackoff.Milliseconds(),
		}).WithError(err).Warn("retry republish failed, re-enqueuing to redis")

		retryAt := time.Now().Add(s.cfg.RepublishFailureBackoff)
		if reErr := s.client.EnqueueRetry(ctx, item.JobID, item.Payload, retryAt); reErr != nil {
			// Redis 도 안 되는 상황 — 데이터 손실. ERROR 로 운영자가 인지하도록.
			s.log.WithFields(map[string]interface{}{
				"job_id":  item.JobID,
				"crawler": job.CrawlerName,
			}).WithError(reErr).Error("redis re-enqueue failed after publish error, retry job lost")
		}
		return
	}

	s.log.WithFields(map[string]interface{}{
		"job_id":  item.JobID,
		"crawler": job.CrawlerName,
		"topic":   msg.Topic,
	}).Debug("retry job republished from redis to kafka")
}

// redisRetryEntry 는 Redis 에 저장되는 retry payload 의 JSON 스키마입니다.
type redisRetryEntry struct {
	JobBytes []byte `json:"job"`
	LastErr  string `json:"last_err"`
}

// lastErrString 은 nil 안전하게 에러 문자열을 추출합니다.
func lastErrString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
