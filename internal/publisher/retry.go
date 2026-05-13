package publisher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"issuetracker/internal/processor/fetcher/core"
	"issuetracker/pkg/logger"
	pkgredis "issuetracker/pkg/redis"
)

// retryDrainTimeout 은 graceful shutdown 으로 ctx 가 canceled 된 뒤에도 Redis enqueue
// 같은 cleanup 작업을 잠깐 더 허용하기 위한 별도 timeout 입니다 (이슈 #389 — 구
// fetcher/worker 의 drainTimeout 동등 값).
const retryDrainTimeout = 5 * time.Second

// RetrySchedulerHolder 는 atomic.Pointer 가 인터페이스를 직접 저장하지 못하므로
// RetryScheduler 인터페이스 값을 감싸 atomic 교체를 지원하는 wrapper 입니다.
//
// 호출자 (fetcher/worker pool) 가 atomic.Pointer[RetrySchedulerHolder] 필드로 보유 후
// SetRetryScheduler / 조회 시 사용.
type RetrySchedulerHolder struct {
	Scheduler RetryScheduler
}

// RetryScheduler 는 처리 실패한 CrawlJob 의 재시도 발행 시점을 관리하는 인터페이스입니다
// (이슈 #389 — 메타 #385 의 Kafka I/O 단일 책임 원칙에 따라 publisher 패키지에서 정의).
//
// 두 가지 구현 전략을 추상화합니다:
//   - KafkaImmediateRetryScheduler: 즉시 Kafka 에 재발행하고 worker 가 ScheduledAt 까지
//     대기 — 기존 동작 보존 (worker 슬롯 점유 문제 그대로)
//   - RedisDelayedRetryScheduler: Redis ZSET 에 보관하고 별도 goroutine 이 ScheduledAt
//     도달 시 Kafka 에 발행 — worker 슬롯 점유 회피
//
// 호출자 (fetcher/worker pool) 는 ScheduledAt 과 RetryCount 를 미리 셋팅한 job 을 전달합니다.
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
// 본 구현은 지적한 처리량 급감을 그대로 갖지만, Redis 미설정 환경 (단일 인스턴스
// 개발/테스트, 통합 redis 장애) 에서 retry 자체는 동작하도록 보존합니다.
type KafkaImmediateRetryScheduler struct {
	pub *Publisher
}

// NewKafkaImmediateRetryScheduler 는 KafkaImmediateRetryScheduler 를 생성합니다 (이슈 #390 —
// 구 queue.Producer 직접 주입 → publisher facade 주입으로 변경. Kafka I/O 단일 책임 원칙
// 일관성 보장).
func NewKafkaImmediateRetryScheduler(pub *Publisher) *KafkaImmediateRetryScheduler {
	return &KafkaImmediateRetryScheduler{pub: pub}
}

// Enqueue 는 job 을 priority 토픽에 즉시 publish 합니다.
//
// gemini PR #400 피드백 — buildMessage 를 재사용하여 crawler / priority 기본 헤더 누락 + 코드
// 중복을 한꺼번에 해소. retry 전용 헤더 (retry-count / last-error) 는 그 위에 덮어쓰기.
func (s *KafkaImmediateRetryScheduler) Enqueue(ctx context.Context, job *core.CrawlJob, lastErr error) error {
	msg, err := s.pub.buildMessage(job)
	if err != nil {
		return fmt.Errorf("build retry message: %w", err)
	}
	applyRetryHeaders(msg.Headers, job, lastErr)

	if err := s.pub.Forward(ctx, msg); err != nil {
		return fmt.Errorf("publish retry job %s: %w", job.ID, err)
	}
	return nil
}

// applyRetryHeaders 는 buildMessage 가 부착한 기본 헤더 위에 retry 전용 헤더를 덮어씁니다 —
// 두 RetryScheduler 구현체가 공유.
func applyRetryHeaders(h map[string]string, job *core.CrawlJob, lastErr error) {
	h["retry-count"] = fmt.Sprintf("%d", job.RetryCount)
	if lastErr != nil {
		h["last-error"] = lastErr.Error()
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Redis-backed delayed retry scheduler
// ─────────────────────────────────────────────────────────────────────────────

// retryQueueClient 는 RedisDelayedRetryScheduler 가 사용하는 Redis 연산을 추상화합니다.
// pkg/redis.Client 가 구조적으로 만족하며, 테스트는 mock 으로 교체합니다.
//
// peek-publish-ack 패턴:
//   - PeekDueRetries 는 due 항목을 조회만 하고 ZSET 에서 제거하지 않음
//   - publish 성공 후 AckRetry 로 명시적 제거 → at-least-once 보장
//   - publish 실패 시 backoff 적용한 재 EnqueueRetry (또는 무처리 후 다음 polling)
type retryQueueClient interface {
	EnqueueRetry(ctx context.Context, jobID string, payload []byte, scheduledAt time.Time) error
	PeekDueRetries(ctx context.Context, now time.Time, limit int) ([]pkgredis.DueRetry, error)
	AckRetry(ctx context.Context, jobID string) error
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

	// HeartbeatEveryNIdleTicks: idle (due 항목 0) tick 이 연속 N 회 발생할 때마다 1회
	// "retry pipeline idle heartbeat" DEBUG 로그를 남김 (default: 60 = 1s polling 기준 ~1분).
	// 0 이면 legacy 동작 (매 idle tick 마다 1줄 — 노이즈 많음).
	// due 항목 발견 시점 로그는 본 설정 무관하게 항상 즉시 emit.
	HeartbeatEveryNIdleTicks int
}

// DefaultRedisRetrySchedulerConfig 는 운영 기본값을 반환합니다.
func DefaultRedisRetrySchedulerConfig() RedisRetrySchedulerConfig {
	return RedisRetrySchedulerConfig{
		PollInterval:             1 * time.Second,
		BatchSize:                50,
		RepublishFailureBackoff:  1 * time.Second,
		HeartbeatEveryNIdleTicks: 60,
	}
}

// RedisDelayedRetryScheduler 는 Redis ZSET 에 retry 를 보관하고 별도 goroutine 이
// ScheduledAt 도달 항목을 Kafka 에 발행하는 구현체입니다.
//
// 핵심 효과: requeue 는 Redis 에만 저장되므로 worker 가 메시지를 소비한 뒤 sleep 하지
// 않고 즉시 다음 정상 job 처리로 넘어갑니다. 워커 슬롯 점유 문제 해소.
//
// 영속성: Redis ZSET 보관 + entry STRING 24h TTL. 프로세스 crash 시에도 다른 인스턴스가
// 이어 받아 처리 가능 (다중 인스턴스 race 는 다운스트림 ProcessingLock 가 흡수).
//
// 라이프사이클: New → Run(ctx) (별도 goroutine) → ctx cancel → goroutine 정리 → Stop 대기.
type RedisDelayedRetryScheduler struct {
	client retryQueueClient
	pub    *Publisher
	cfg    RedisRetrySchedulerConfig
	log    *logger.Logger
	wg     sync.WaitGroup

	// idleTicks: 연속 idle (due 0) tick 수. due 발견 시 0 으로 reset.
	// pollOnce 만 접근 — goroutine 단일 진입이라 race 없음.
	idleTicks int
}

// NewRedisDelayedRetryScheduler 는 RedisDelayedRetryScheduler 를 생성합니다 (이슈 #390 —
// 구 queue.Producer 직접 주입 → publisher facade 주입으로 변경).
//
// 보정 규칙:
//   - PollInterval / BatchSize / RepublishFailureBackoff: 0 또는 음수 → default 적용.
//   - HeartbeatEveryNIdleTicks: **0 은 legacy (매 tick 로깅) 로 유효한 값** 이므로
//     음수일 때만 default (60) 로 보정. 따라서 0 을 명시하면 default 가 적용되지 않음.
func NewRedisDelayedRetryScheduler(client retryQueueClient, pub *Publisher, cfg RedisRetrySchedulerConfig, log *logger.Logger) *RedisDelayedRetryScheduler {
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
	if cfg.HeartbeatEveryNIdleTicks < 0 {
		cfg.HeartbeatEveryNIdleTicks = def.HeartbeatEveryNIdleTicks
	}
	return &RedisDelayedRetryScheduler{
		client: client,
		pub:    pub,
		cfg:    cfg,
		log:    log,
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

// Start 는 polling 루프를 별도 goroutine 으로 시작합니다.
//
// WaitGroup 안전성: wg.Add(1) 을 goroutine 시작 전에 호출하여 "Add called concurrently
// with Wait" 패닉을 방지합니다 (Copilot 피드백 — 코드베이스의 internal/scheduler 와
// 동일 패턴). Stop 이 wg.Wait() 으로 join 합니다.
func (s *RedisDelayedRetryScheduler) Start(ctx context.Context) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.Run(ctx)
	}()
}

// Run 은 ctx 가 cancel 될 때까지 polling 을 반복합니다. **동기 실행 루프** 만 담당하며
// goroutine / WaitGroup 등록은 Start 또는 호출자가 책임집니다.
//
// 호출 패턴:
//   - 권장: Start(ctx) 사용 (자동 wg 등록)
//   - 직접 호출: 호출자가 wg.Add 를 미리 한 뒤 별도 goroutine 으로 호출 (Stop 이 wait)
func (s *RedisDelayedRetryScheduler) Run(ctx context.Context) {
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

// Stop 은 Start 로 시작된 goroutine 이 종료될 때까지 대기합니다 (ctx cancel 후 호출).
func (s *RedisDelayedRetryScheduler) Stop() {
	s.wg.Wait()
}

// pollOnce 는 due 항목을 한 batch 만큼 peek 하여 Kafka 에 발행합니다.
// peek 단계에서는 ZSET 에서 제거하지 않으며, publish 성공 후에만 AckRetry 로 제거 —
// at-least-once 보장 (peek-publish-ack 패턴).
func (s *RedisDelayedRetryScheduler) pollOnce(ctx context.Context) {
	peekStart := time.Now()
	due, err := s.client.PeekDueRetries(ctx, time.Now(), s.cfg.BatchSize)
	if err != nil {
		// ctx cancel 로 인한 에러는 정상 종료 흐름 — DEBUG 로 강등
		if ctx.Err() != nil {
			s.log.WithError(err).Debug("retry peek interrupted by shutdown")
			return
		}
		s.log.WithError(err).Warn("failed to peek due retries from redis")
		return
	}

	// peek 결과를 DEBUG 로 노출 — due 발견 시 즉시, idle 시 주기적 heartbeat.
	// 운영자가 retry pipeline 이 살아있고 polling 중인지 즉답 가능하면서
	// 매 tick 1줄 노이즈는 피한다 (이슈 #370).
	if len(due) > 0 {
		s.log.WithFields(map[string]interface{}{
			"due_count":           len(due),
			"peek_ms":             time.Since(peekStart).Milliseconds(),
			"batch_size":          s.cfg.BatchSize,
			"previous_idle_ticks": s.idleTicks,
		}).Debug("retry peek returned due items")
		s.idleTicks = 0
	} else {
		s.idleTicks++
		every := s.cfg.HeartbeatEveryNIdleTicks
		if every == 0 || s.idleTicks%every == 0 {
			s.log.WithFields(map[string]interface{}{
				"peek_ms":                time.Since(peekStart).Milliseconds(),
				"consecutive_idle_ticks": s.idleTicks,
			}).Debug("retry pipeline idle heartbeat")
		}
	}

	for _, item := range due {
		s.republish(ctx, item)
	}
}

// republish 는 단일 due 항목을 Kafka 에 발행합니다.
//
//   - publish 성공  → AckRetry 로 ZSET/entry 제거 (정상 종료)
//   - publish 실패  → 짧은 backoff 로 EnqueueRetry 재호출 (ScheduledAt overwrite) →
//     다음 폴 사이클에 재시도. EnqueueRetry 도 실패하면 ZSET 에 원본 ScheduledAt 으로
//     항목이 남아 즉시 재peek 되며, ProcessingLock 가 Kafka 중복을 흡수
//   - payload 손상 → AckRetry 로 제거 (drop) + ERROR (운영 이상 신호)
//
// 프로세스 crash 안전성: republish 어느 시점에 crash 가 일어나도 ZSET 에 원본 항목이
// 남아있으므로 다음 instance / 재기동 시 재peek 됩니다.
func (s *RedisDelayedRetryScheduler) republish(ctx context.Context, item pkgredis.DueRetry) {
	var entry redisRetryEntry
	if err := json.Unmarshal(item.Payload, &entry); err != nil {
		// payload 손상 — 복구 불가. ack 로 제거하여 무한 재peek 회피.
		s.log.WithFields(map[string]interface{}{
			"job_id": item.JobID,
		}).WithError(err).Error("malformed retry payload, dropping")
		s.ackOrLog(ctx, item.JobID, "drop on malformed payload")
		return
	}

	var job core.CrawlJob
	if err := json.Unmarshal(entry.JobBytes, &job); err != nil {
		s.log.WithFields(map[string]interface{}{
			"job_id": item.JobID,
		}).WithError(err).Error("malformed retry job bytes, dropping")
		s.ackOrLog(ctx, item.JobID, "drop on malformed job bytes")
		return
	}

	var lastErr error
	if entry.LastErr != "" {
		lastErr = errors.New(entry.LastErr)
	}

	// gemini PR #400 피드백 — buildMessage 재사용으로 crawler/priority 기본 헤더 부착.
	// Value 는 이미 Redis 에 보관된 entry.JobBytes 를 그대로 사용 (re-marshal 회피).
	msg, err := s.pub.buildMessage(&job)
	if err != nil {
		s.log.WithFields(map[string]interface{}{
			"job_id": item.JobID,
		}).WithError(err).Error("failed to build retry message, dropping")
		s.ackOrLog(ctx, item.JobID, "drop on build message failure")
		return
	}
	msg.Value = entry.JobBytes
	applyRetryHeaders(msg.Headers, &job, lastErr)

	if err := s.pub.Forward(ctx, msg); err != nil {
		// Kafka publish 실패 — backoff 적용한 ScheduledAt 으로 EnqueueRetry 재호출하여
		// 같은 jobID 의 score 를 미래로 overwrite (즉시 재peek 회피).
		// EnqueueRetry 가 실패해도 ZSET 의 원본 항목이 남아있어 다음 폴에 재peek + 재시도.
		s.log.WithFields(map[string]interface{}{
			"job_id":     item.JobID,
			"crawler":    job.CrawlerName,
			"backoff_ms": s.cfg.RepublishFailureBackoff.Milliseconds(),
		}).WithError(err).Warn("retry republish failed, rescheduling in redis")

		retryAt := time.Now().Add(s.cfg.RepublishFailureBackoff)
		// 셧다운 중 (ctx canceled) 에는 reschedule 까지 ctx 에러로 실패 → 다음 인스턴스가
		// 원본 score 로 즉시 재peek (정합성 보존). 그래도 의도된 backoff 를 유지하려면
		// drain context 로 한 번 더 시도 (Copilot #5 — 셧다운 윈도우 안전성).
		// 원본 ctx 가 canceled 가 아니면 그대로 사용.
		enqueueCtx := ctx
		var cancelDrain context.CancelFunc
		if ctx.Err() != nil {
			enqueueCtx, cancelDrain = context.WithTimeout(context.WithoutCancel(ctx), retryDrainTimeout)
			defer cancelDrain()
		}
		if reErr := s.client.EnqueueRetry(enqueueCtx, item.JobID, item.Payload, retryAt); reErr != nil {
			s.log.WithFields(map[string]interface{}{
				"job_id":  item.JobID,
				"crawler": job.CrawlerName,
			}).WithError(reErr).Warn("redis reschedule failed, item retains original score for immediate re-peek")
		}
		return
	}

	// publish 성공 → ZSET/entry 제거. ack 실패는 다음 폴 사이클에서 중복 publish 로 이어짐 —
	// ProcessingLock 가 흡수하므로 정합성 문제 없으나 운영 가시성을 위해 WARN.
	s.ackOrLog(ctx, item.JobID, "after successful publish")

	s.log.WithFields(map[string]interface{}{
		"job_id":  item.JobID,
		"crawler": job.CrawlerName,
		"topic":   msg.Topic,
	}).Debug("retry job republished from redis to kafka")
}

// ackOrLog 는 AckRetry 호출 후 실패 시 WARN 로그만 남깁니다 (정합성은 ProcessingLock 가 흡수).
func (s *RedisDelayedRetryScheduler) ackOrLog(ctx context.Context, jobID, reason string) {
	if err := s.client.AckRetry(ctx, jobID); err != nil {
		s.log.WithFields(map[string]interface{}{
			"job_id": jobID,
			"reason": reason,
		}).WithError(err).Warn("retry ack failed, item may be re-peeked on next poll")
	}
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
