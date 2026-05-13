// Package worker provides Kafka-based crawler worker pool implementation.
//
// worker 패키지는 Kafka consumer group 기반 크롤러 워커 풀을 제공합니다.
// KafkaConsumerPool 은 fetcher stage 특화 책임 (CircuitBreaker / RetryScheduler / StageGate /
// publishNormalized 등) 을 담당하고, 공용 lifecycle (poll / dispatch / shutdown / commit) 은
// internal/workerpool harness 에 위임합니다 (이슈 #405 — 메타 #403 Sub 2).
package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"issuetracker/internal/bus"
	"issuetracker/internal/locks"
	"issuetracker/internal/processor/fetcher/core"
	"issuetracker/internal/storage/service"
	"issuetracker/internal/workerpool"
	"issuetracker/pkg/links"
	"issuetracker/pkg/logger"
	"issuetracker/pkg/queue"
	"issuetracker/pkg/resilience"
	"issuetracker/pkg/urlguard"
)

// drainTimeout 은 graceful shutdown 으로 ctx 가 canceled 된 뒤 Kafka publish / commit 을
// 한 번 더 시도할 때 사용하는 별도 context 의 타임아웃입니다.
// at-least-once 시맨틱 보장 — validate worker / workerpool harness 와 동일 값.
const drainTimeout = workerpool.DefaultDrainTimeout

// JobHandler는 CrawlJob을 처리하는 인터페이스입니다.
// 구현체는 여러 goroutine에서 동시에 호출되므로 goroutine-safe해야 합니다.
// Handle은 크롤링 및 파싱 결과를 []*core.Content로 반환합니다.
// 처리할 내용이 없으면 nil, nil을 반환합니다 — fetcher 측
// ChainHandler 는 raw_contents 저장 + RawContentRef 발행 만 수행하므로 항상 nil 반환.
type JobHandler interface {
	Handle(ctx context.Context, job *core.CrawlJob) ([]*core.Content, error)
}

// kafkaRequeuePolicy는 Kafka 재큐잉 시 적용할 backoff 정책입니다.
// HTTP 레벨 재시도(core.DefaultRetryPolicy)와 달리 Kafka 레벨 재큐잉 간격을 제어합니다.
// RetryCount=1 → 약 5s, RetryCount=2 → 약 10s, RetryCount=3 → 약 20s (±25% jitter)
var kafkaRequeuePolicy = core.RetryPolicy{
	InitialDelay: 5 * time.Second,
	MaxDelay:     5 * time.Minute,
	Multiplier:   2.0,
	Jitter:       true,
}

// KafkaConsumerPool 은 Kafka consumer group 기반 crawler worker pool 입니다.
//
// fetcher stage 특화 책임:
//   - CircuitBreaker (소스별 자동 차단)
//   - RetryScheduler (Kafka 즉시 재발행 / Redis 지연 발행)
//   - StageGate (ProcessingLock + Semaphore)
//   - URL 가드 (urlguard.Gate)
//   - URL 정규화 (links.Normalizer)
//   - publishNormalized (Content → contents DB + ContentRef Kafka 발행)
//
// 공용 lifecycle (poll / dispatch / shutdown / commit) 은 internal/workerpool harness 에 위임.
// KafkaConsumerPool 이 workerpool.Handler 인터페이스를 구현하여 메시지마다 Handle 호출됨.
type KafkaConsumerPool struct {
	consumer    bus.Consumer
	pub         *bus.Publisher
	handler     JobHandler
	contentSvc  service.ContentService
	workerCount int
	cbRegistry  *resilience.CircuitBreakerRegistry
	stageGate   locks.StageGate // nil 허용 → NoopStageGate (이슈 #356)
	// normalizer는 URL 정규화기입니다.
	// 미설정(nil) 이면 정규화가 적용되지 않으며, 기존 동작이 유지됩니다.
	// atomic.Pointer 를 사용하여 worker goroutine 의 동시 Load 와 SetNormalizer 의 Store
	// 사이에 race 가 발생하지 않도록 보장합니다.
	normalizer atomic.Pointer[links.Normalizer]
	// gate 는 URL 가드 입니다.
	// 미설정(nil) 이면 가드 비활성 — 모든 job 이 처리됩니다.
	// Handle 진입 직후 검사하여 차단된 URL 의 처리를 skip 하고 message 만 commit.
	// atomic.Pointer 로 race-safe 한 lock-free 설정/조회.
	gate atomic.Pointer[urlguard.Gate]
	// retryScheduler 는 재시도 발행 시점을 관리하는 RetryScheduler 입니다.
	// 미설정(nil) 이면 lazy 로 KafkaImmediateRetryScheduler 가 사용되어 기존 동작 유지 —
	// 즉시 priority 토픽에 publish + worker 가 ScheduledAt 까지 sleep.
	// atomic.Pointer 로 race-safe 설정 — Start 이후 SetRetryScheduler 호출에도 안전.
	retryScheduler atomic.Pointer[bus.RetrySchedulerHolder]

	// heartbeat 가시성. Handle 진입 시 inc, defer 시 dec.
	// 운영자가 "silent vs hang" 을 즉답하기 위한 단일 source of truth.
	busyCount atomic.Int32
	// priority 는 heartbeat 로그에 포함될 pool 식별자 ("high"/"normal"/"low").
	// 빈 문자열이면 heartbeat goroutine 자체가 시작되지 않음 (테스트 호환).
	priority string

	// pool 은 workerpool harness — Start 에서 lazy 생성.
	pool *workerpool.ConsumerPool
}

// NewKafkaConsumerPool은 새로운 KafkaConsumerPool을 생성합니다.
//
// consumer에서 메시지를 읽어 handler로 처리하고,
// 결과 Content를 contents DB에 저장한 뒤 ContentRef를
// issuetracker.normalized 토픽에 발행합니다.
// workerCount는 동시에 실행되는 처리 goroutine 수를 결정합니다.
func NewKafkaConsumerPool(
	consumer bus.Consumer,
	pub *bus.Publisher,
	handler JobHandler,
	contentSvc service.ContentService,
	workerCount int,
) *KafkaConsumerPool {
	// CircuitBreakerRegistry log 주입은 NewPoolManager 가 PoolManager 단위에서 처리.
	// 본 헬퍼는 단순 호출용 (테스트/예제) 이라 logger 주입 안 함 — nil 이면 state 전이 로그 skip.
	return NewKafkaConsumerPoolWithOptions(
		consumer, pub, handler, contentSvc, workerCount,
		resilience.NewCircuitBreakerRegistry(resilience.DefaultCircuitBreakerConfig, nil),
		locks.NewNoopStageGate(),
	)
}

// NewKafkaConsumerPoolWithCB는 외부에서 주입한 CircuitBreakerRegistry를 사용하는
// KafkaConsumerPool을 생성합니다. 테스트에서 circuit breaker 동작을 제어할 때 사용합니다.
func NewKafkaConsumerPoolWithCB(
	consumer bus.Consumer,
	pub *bus.Publisher,
	handler JobHandler,
	contentSvc service.ContentService,
	workerCount int,
	cbRegistry *resilience.CircuitBreakerRegistry,
) *KafkaConsumerPool {
	return NewKafkaConsumerPoolWithOptions(
		consumer, pub, handler, contentSvc, workerCount,
		cbRegistry,
		locks.NewNoopStageGate(),
	)
}

// NewKafkaConsumerPoolWithOptions는 모든 의존성을 외부에서 주입하는 생성자입니다.
// 테스트에서 circuit breaker, stage gate 를 개별 제어할 때 사용합니다.
//
// gate 는 nil 허용 — nil 이면 NoopStageGate 로 fallback (단일 인스턴스 환경에서 dedup + cap 비활성).
// 이슈 #356 — fetcher / parser / validator 가 동일 StageGate 패턴 사용.
func NewKafkaConsumerPoolWithOptions(
	consumer bus.Consumer,
	pub *bus.Publisher,
	handler JobHandler,
	contentSvc service.ContentService,
	workerCount int,
	cbRegistry *resilience.CircuitBreakerRegistry,
	gate locks.StageGate,
) *KafkaConsumerPool {
	// nil guard — NewParserWorker / validate.NewWorker 와 일관성 보장.
	if gate == nil {
		gate = locks.NewNoopStageGate()
	}
	return &KafkaConsumerPool{
		consumer:    consumer,
		pub:         pub,
		handler:     handler,
		contentSvc:  contentSvc,
		workerCount: workerCount,
		cbRegistry:  cbRegistry,
		stageGate:   gate,
	}
}

// SetRetryScheduler 는 requeueWithRetry 시 사용할 RetryScheduler 를 설정합니다.
// nil 전달 시 fallback 인 KafkaImmediateRetryScheduler (lazy 생성) 가 사용됩니다 —
// 기존 동작 보존. atomic 교체로 Start 이후에도 race-safe.
func (p *KafkaConsumerPool) SetRetryScheduler(rs bus.RetryScheduler) {
	if rs == nil {
		p.retryScheduler.Store(nil)
		return
	}
	p.retryScheduler.Store(&bus.RetrySchedulerHolder{Scheduler: rs})
}

// SetGate 는 Handle 진입 시 URL 검사에 사용할 urlguard.Gate 를 설정합니다.
// 미설정(nil) 시 가드 비활성 — 모든 job 이 정상 처리됩니다.
func (p *KafkaConsumerPool) SetGate(g *urlguard.Gate) {
	p.gate.Store(g)
}

// SetNormalizer는 URL 정규화기를 설정합니다.
// nil 이거나 미호출 시 정규화는 적용되지 않으며, 기존 동작이 유지됩니다.
func (p *KafkaConsumerPool) SetNormalizer(n *links.Normalizer) {
	p.normalizer.Store(n)
}

// normalizeURL은 normalizer가 설정된 경우 URL을 정규화하여 반환합니다.
// normalizer 미설정 / 빈 URL / 정규화 실패 시 원본을 그대로 반환.
func (p *KafkaConsumerPool) normalizeURL(rawURL string) string {
	n := p.normalizer.Load()
	if n == nil || rawURL == "" {
		return rawURL
	}
	normalized, err := n.Normalize(rawURL)
	if err != nil || normalized == "" {
		return rawURL
	}
	return normalized
}

// SetPriority 는 heartbeat 로그에 사용할 pool 식별자를 설정합니다.
// 빈 문자열이면 heartbeat goroutine 이 시작되지 않습니다.
// Start 호출 전에 설정해야 효과가 있습니다.
func (p *KafkaConsumerPool) SetPriority(name string) {
	p.priority = name
}

// Start 는 workerpool harness 를 기동합니다.
//
// harness 가 N 개 worker goroutine + poll goroutine 을 관리하며, 각 메시지마다 본 pool 의
// Handle 메소드를 호출합니다. heartbeat goroutine 은 priority 설정 시 별도 시작.
//
// 로깅 — worker_pool 필드 단일 책임 (gemini PR #414 피드백):
//   - Name 은 priority 가 설정된 경우만 "fetcher-<priority>", 빈 경우 "fetcher" (trailing
//     hyphen 회피).
//   - worker_pool 필드는 workerpool.Config.Name 으로 한 번만 설정 → harness 가 자체 logger 에
//     주입 (중복 회피).
//   - Handler / processJob / heartbeatLoop 가 FromContext(ctx) 로 로거를 얻을 때도 동일 필드를
//     보도록 ctx 에 worker_pool 필드 logger 주입 (observability 보존).
func (p *KafkaConsumerPool) Start(ctx context.Context) {
	if p.pool != nil {
		panic("fetcher worker pool: Start called more than once on the same instance")
	}
	name := "fetcher"
	if p.priority != "" {
		name = "fetcher-" + p.priority
	}
	// plain logger 를 workerpool.Config.Log 로 전달 — harness 가 Name 으로 worker_pool 필드
	// 단독 주입 (중복 회피). 동일 필드를 Handle/processJob/heartbeatLoop 의 ctx logger 에도
	// 보존하기 위해 ctx 에 별도 ToContext 로 주입.
	plainLog := logger.FromContext(ctx)
	ctx = plainLog.WithField("worker_pool", name).ToContext(ctx)

	p.pool = workerpool.New(workerpool.Config{
		Consumer:     p.consumer,
		Handler:      p,
		WorkerCount:  p.workerCount,
		DrainTimeout: drainTimeout,
		Log:          plainLog, // harness 가 자체 worker_pool 필드 주입 (Name 으로 1회)
		Name:         name,
	})
	p.pool.Start(ctx)

	// heartbeat goroutine — priority 가 설정된 경우만 (테스트 호환). ctx cancel 시 자체 종료.
	if p.priority != "" {
		go p.heartbeatLoop(ctx)
	}
}

// Stop 은 workerpool harness 를 정상 종료합니다 — graceful shutdown 순서 + drain timeout 위임.
//
// pool 이 미기동 (Start 미호출) 상태에서 Stop 호출 시 consumer.Close 만 수행 — Kafka 연결
// 자원 누수 방지 (gemini PR #415 피드백 — 동일 패턴이 parser 와 일치).
func (p *KafkaConsumerPool) Stop(ctx context.Context) error {
	if p.pool == nil {
		return p.consumer.Close()
	}
	return p.pool.Stop(ctx)
}

// heartbeatLoop 는 30초마다 worker pool 의 현재 상태 (busy/total/buffered) 를 DEBUG 로
// 출력합니다. 운영자가 LOG_LEVEL=debug 토글 시 silent vs hang 즉답.
//
// ctx cancel 시 자체 종료 — 관찰용이라 셧다운 차단 책임 없음.
func (p *KafkaConsumerPool) heartbeatLoop(ctx context.Context) {
	const interval = 30 * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	log := logger.FromContext(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			busy := int(p.busyCount.Load())
			buffered := 0
			if p.pool != nil {
				buffered = p.pool.JobsBuffered()
			}
			log.WithFields(map[string]interface{}{
				"priority":      p.priority,
				"busy_workers":  busy,
				"total_workers": p.workerCount,
				"jobs_buffered": buffered,
			}).Debug("worker pool status")
		}
	}
}

// Handle 은 workerpool.Handler 구현 — 각 메시지마다 호출됩니다.
//
// 흐름 (구 processJob):
//  1. UnmarshalCrawlJob (실패 → DLQ + commit)
//  2. URL 정규화 (적용 지점 ①)
//  3. URL 가드 검사 (차단 → commit-only)
//  4. ScheduledAt backoff 대기
//  5. StageGate Acquire (skip → no commit; err → fail-open)
//  6. CircuitBreaker Allow 검사 (open → DLQ + commit)
//  7. handler.Handle (error → requeue/DLQ + commit; success → publishNormalized + commit)
func (p *KafkaConsumerPool) Handle(ctx context.Context, msg *queue.Message) {
	log := logger.FromContext(ctx)

	job, err := core.UnmarshalCrawlJob(msg.Value)
	if err != nil {
		log.WithError(err).Error("failed to unmarshal crawl job, sending to dlq")
		if dlqErr := p.sendToDLQ(ctx, msg, err); dlqErr != nil {
			logShutdownAware(ctx, log, dlqErr, "failed to send unmarshal-failed message to dlq, skipping commit to preserve message")
			return
		}
		if commitErr := p.pool.Commit(ctx, msg); commitErr != nil {
			logShutdownAware(ctx, log, commitErr, "failed to commit message after unmarshal-failure dlq")
		}
		return
	}

	// 적용 지점 ①: Target.URL 정규화 — 다운스트림 모든 키 (Ingestion Lock / ProcessingLock /
	// Kafka 파티션) 가 동일 정규형 사용. normalizer 미설정 시 원본 fallback.
	job.Target.URL = p.normalizeURL(job.Target.URL)

	if err := p.processJob(ctx, msg, job); err != nil {
		// graceful shutdown 으로 발생한 context.Canceled 는 정상 종료 흐름이므로 DEBUG 로 강등.
		jobLog := log.WithFields(map[string]interface{}{
			"job_id":  job.ID,
			"crawler": job.CrawlerName,
		})
		logShutdownAware(ctx, jobLog, err, "job processing failed")
	}
}

func (p *KafkaConsumerPool) processJob(ctx context.Context, msg *queue.Message, job *core.CrawlJob) (err error) {
	log := logger.FromContext(ctx)

	// busyCount 증감 + duration 측정 (heartbeat 가시성). outcome 은 err 여부로 분기.
	p.busyCount.Add(1)
	start := time.Now()
	defer func() {
		p.busyCount.Add(-1)
		fields := map[string]interface{}{
			"job_id":      job.ID,
			"crawler":     job.CrawlerName,
			"duration_ms": time.Since(start).Milliseconds(),
		}
		if err != nil {
			fields["outcome"] = "error"
		} else {
			fields["outcome"] = "ok"
		}
		log.WithFields(fields).Debug("job processed")
	}()

	// URL 가드: Handle 진입 직후 차단 URL 을 즉시 거르고 commit.
	if g := p.gate.Load(); g != nil {
		if !g.Allow(job.Target.URL, map[string]interface{}{
			"crawler": job.CrawlerName,
			"job_id":  job.ID,
			"stage":   "consumer",
		}) {
			return p.pool.Commit(ctx, msg)
		}
	}

	// 재큐잉 시 설정된 ScheduledAt 이 미래면 해당 시점까지 대기.
	// ProcessingLock 획득 전에 대기 — lock 먼저 잡으면 backoff 가 TTL 소진 + 동일 URL 의 다른
	// worker 가 처리 중으로 오인 회피.
	if delay := time.Until(job.ScheduledAt); delay > 0 {
		log.WithFields(map[string]interface{}{
			"job_id":   job.ID,
			"crawler":  job.CrawlerName,
			"delay_ms": delay.Milliseconds(),
		}).Debug("waiting for backoff before processing retried job")

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}

	// fetcher 단계 StageGate (ProcessingLock + Semaphore 합성, 이슈 #356).
	// backoff 대기 이후에 Acquire 하여 슬롯 점유 시간을 실제 처리 구간으로 최소화.
	release, acquired, gateErr := p.stageGate.Acquire(ctx, job.Target.URL)
	if gateErr != nil {
		// ctx cancel / deadline → fail-open 시 취소된 ctx 로 불필요 작업 + per-stage cap 무력화 위험.
		// commit 안 하고 종료 → Kafka redeliver 보장.
		if ctx.Err() != nil {
			return fmt.Errorf("fetcher stage gate acquire aborted by ctx: %w", gateErr)
		}
		// 그 외 인프라 에러 — graceful degrade.
		log.WithFields(map[string]interface{}{
			"job_id":  job.ID,
			"crawler": job.CrawlerName,
			"url":     job.Target.URL,
		}).WithError(gateErr).Warn("failed to acquire fetcher stage gate, proceeding without gate")
	} else if !acquired {
		log.WithFields(map[string]interface{}{
			"job_id":  job.ID,
			"crawler": job.CrawlerName,
			"url":     job.Target.URL,
		}).Debug("fetcher processing lock already held by another worker, skipping")
		// 다른 워커가 처리 중 — commit 없이 종료. 처리 담당 워커의 commit 에 의존.
		return nil
	} else {
		log.WithFields(map[string]interface{}{
			"job_id":  job.ID,
			"crawler": job.CrawlerName,
			"url":     job.Target.URL,
		}).Debug("fetcher stage gate acquired")

		defer func() {
			release()
			log.WithFields(map[string]interface{}{
				"job_id":  job.ID,
				"crawler": job.CrawlerName,
				"url":     job.Target.URL,
			}).Debug("fetcher stage gate released")
		}()
	}

	log.WithFields(map[string]interface{}{
		"job_id":  job.ID,
		"crawler": job.CrawlerName,
		"url":     job.Target.URL,
	}).Info("crawl job started")

	// CircuitBreaker open → DLQ + commit. 소스 복구 후 운영자가 DLQ 재처리.
	cb := p.cbRegistry.Get(job.CrawlerName)
	if !cb.Allow() {
		cbErr := &resilience.ErrCircuitOpen{Source: job.CrawlerName}
		log.WithFields(map[string]interface{}{
			"job_id":  job.ID,
			"crawler": job.CrawlerName,
		}).Warn("circuit breaker open, sending job to dlq")

		jobLog := log.WithFields(map[string]interface{}{
			"job_id":  job.ID,
			"crawler": job.CrawlerName,
		})
		if publishErr := p.sendToDLQ(ctx, msg, cbErr); publishErr != nil {
			logShutdownAware(ctx, jobLog, publishErr, "failed to send circuit-broken job to dlq, skipping commit to preserve message")
			return fmt.Errorf("circuit open dlq for job %s: %w", job.ID, publishErr)
		}
		if commitErr := p.pool.Commit(ctx, msg); commitErr != nil {
			logShutdownAware(ctx, jobLog, commitErr, "failed to commit message after circuit breaker dlq")
		}
		return cbErr
	}

	// incoming msg.Headers 를 ctx 에 첨부 — chain_handler.publishFetchedRef 가 reparse_*
	// 등 헤더를 TopicFetched 발행 메시지로 전파 (이슈 #366).
	handleCtx := core.WithInboxHeaders(ctx, msg.Headers)
	contents, err := p.handler.Handle(handleCtx, job)
	if err != nil {
		cb.RecordFailure()

		// 재발행 (requeue / DLQ) 성공 시에만 원본 commit. 발행 실패 시 commit 안 함 → 재소비 시 재처리.
		var publishErr error
		if job.RetryCount >= job.MaxRetries {
			publishErr = p.sendToDLQ(ctx, msg, err)
		} else {
			publishErr = p.requeueWithRetry(ctx, job, err)
		}

		jobLog := log.WithFields(map[string]interface{}{
			"job_id":  job.ID,
			"crawler": job.CrawlerName,
		})
		if publishErr != nil {
			logShutdownAware(ctx, jobLog, publishErr, "failed to republish job, skipping commit to preserve message")
			return fmt.Errorf("handle job %s: republish: %w", job.ID, publishErr)
		}
		if commitErr := p.pool.Commit(ctx, msg); commitErr != nil {
			logShutdownAware(ctx, jobLog, commitErr, "failed to commit message after error handling")
		}
		return fmt.Errorf("handle job %s: %w", job.ID, err)
	}

	if len(contents) == 0 {
		// URL dedup 은 bus 의 Ingestion Lock (TTL 24h) 으로 보장 — fetch 성공 후 별도 캐시 등록 불필요.
		cb.RecordSuccess()
		return p.pool.Commit(ctx, msg)
	}

	for _, c := range contents {
		if err := p.publishNormalized(ctx, c, job); err != nil {
			return fmt.Errorf("publish normalized for job %s: %w", job.ID, err)
		}
	}

	cb.RecordSuccess()
	return p.pool.Commit(ctx, msg)
}

// publishNormalized 는 Content 를 contents DB 에 저장한 뒤 ContentRef 를
// issuetracker.normalized 토픽에 ProcessingMessage 로 발행합니다.
// 다운스트림 validator 는 ref.ID 로 DB 에서 전체 데이터를 조회합니다.
//
// URL 정규화 (적용 지점 ②, ③):
//   - CanonicalURL: 파서가 채운 값을 우선 정규화, 비어있으면 content.URL 정규화
//   - Kafka 파티션 키: CanonicalURL 사용 (동일 기사가 동일 파티션으로 라우팅)
func (p *KafkaConsumerPool) publishNormalized(ctx context.Context, content *core.Content, job *core.CrawlJob) error {
	source := content.CanonicalURL
	if source == "" {
		source = content.URL
	}
	if normalized := p.normalizeURL(source); normalized != "" {
		content.CanonicalURL = normalized
	}

	storedID, _, err := p.contentSvc.Store(ctx, content)
	if err != nil {
		return fmt.Errorf("store content for job %s: %w", job.ID, err)
	}

	ref := core.ContentRef{
		ID:      storedID,
		URL:     content.URL,
		Country: content.Country,
		SourceInfo: core.SourceInfo{
			Country:  content.Country,
			Type:     content.SourceType,
			Name:     content.SourceID,
			Language: content.Language,
		},
	}

	refData, err := json.Marshal(ref)
	if err != nil {
		return fmt.Errorf("marshal content ref: %w", err)
	}

	pm := core.ProcessingMessage{
		ID:        storedID,
		Timestamp: time.Now(),
		Country:   content.Country,
		Stage:     "normalized",
		Data:      refData,
		Metadata: map[string]interface{}{
			"job_id":  job.ID,
			"crawler": job.CrawlerName,
		},
	}

	pmBytes, err := json.Marshal(pm)
	if err != nil {
		return fmt.Errorf("marshal processing message: %w", err)
	}

	partitionKey := content.CanonicalURL
	if partitionKey == "" {
		partitionKey = content.URL
	}

	msg := queue.Message{
		Topic: queue.TopicNormalized,
		Key:   []byte(partitionKey),
		Value: pmBytes,
		Headers: map[string]string{
			"source":  content.SourceID,
			"country": content.Country,
			"job_id":  job.ID,
		},
	}

	return p.pub.Forward(ctx, msg)
}

// sendToDLQ 는 메시지를 DLQ 토픽에 발행합니다.
// graceful shutdown 으로 ctx 가 canceled 된 경우 drain context 로 한 번 더 시도 — 원본 메시지
// 보존 우선. 로그는 호출자가 logShutdownAware 로 통일 강등 정책에 따라 작성.
func (p *KafkaConsumerPool) sendToDLQ(ctx context.Context, msg *queue.Message, reason error) error {
	headers := make(map[string]string, len(msg.Headers)+2)
	for k, v := range msg.Headers {
		headers[k] = v
	}
	headers["original-topic"] = msg.Topic
	headers["error"] = reason.Error()

	dlqMsg := queue.Message{
		Topic:   queue.TopicDLQ,
		Key:     msg.Key,
		Value:   msg.Value,
		Headers: headers,
	}

	err := p.pub.Forward(ctx, dlqMsg)
	if err != nil && errors.Is(err, context.Canceled) {
		drainCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), drainTimeout)
		defer cancel()
		if retryErr := p.pub.Forward(drainCtx, dlqMsg); retryErr != nil {
			err = errors.Join(err, retryErr)
		} else {
			err = nil
		}
	}
	if err != nil {
		return fmt.Errorf("send to dlq: %w", err)
	}
	return nil
}

func (p *KafkaConsumerPool) requeueWithRetry(ctx context.Context, job *core.CrawlJob, reason error) error {
	log := logger.FromContext(ctx)

	job.RetryCount++

	// RetryCount 기반 exponential backoff 계산 후 ScheduledAt 에 저장.
	// 실제 지연 적용은 RetryScheduler 구현체가 책임 — KafkaImmediate (worker sleep) 또는
	// RedisDelayed (별 goroutine, 워커 슬롯 점유 없음).
	backoffDelay := core.CalculateBackoff(kafkaRequeuePolicy, job.RetryCount)
	job.ScheduledAt = time.Now().Add(backoffDelay)

	scheduler := p.resolveRetryScheduler()
	topic := bus.CrawlTopic(job.Priority)

	log.WithFields(map[string]interface{}{
		"job_id":   job.ID,
		"crawler":  job.CrawlerName,
		"retry":    job.RetryCount,
		"delay_ms": backoffDelay.Milliseconds(),
		"topic":    topic,
	}).Warn("requeuing job with backoff delay")

	err := scheduler.Enqueue(ctx, job, reason)
	if err != nil && errors.Is(err, context.Canceled) {
		drainCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), drainTimeout)
		defer cancel()
		if retryErr := scheduler.Enqueue(drainCtx, job, reason); retryErr != nil {
			err = errors.Join(err, retryErr)
		} else {
			err = nil
		}
	}
	if err != nil {
		return fmt.Errorf("publish retry job: %w", err)
	}
	return nil
}

// resolveRetryScheduler 는 SetRetryScheduler 로 주입된 구현체를 반환하고, 미설정 시
// KafkaImmediateRetryScheduler 를 lazy 생성합니다 (기존 동작 — 즉시 publish + worker sleep).
func (p *KafkaConsumerPool) resolveRetryScheduler() bus.RetryScheduler {
	if h := p.retryScheduler.Load(); h != nil {
		return h.Scheduler
	}
	return bus.NewKafkaImmediateRetryScheduler(p.pub)
}

// logShutdownAware 는 graceful shutdown 으로 발생한 컨텍스트성 에러를 DEBUG 로,
// 그 외 에러는 ERROR 로 기록하는 공통 헬퍼입니다.
//
// 강등 조건은 ctx.Err() != nil 단독 — 셧다운으로 인한 cancel 인지의 진실의 단일 소스 (SoT).
// errors.Is(err, context.Canceled) 검사 시 chromedp 같은 라이브러리가 정상 timeout 후
// context.Canceled 를 chain 에 포함하는 케이스를 셧다운으로 오인 (false positive).
func logShutdownAware(ctx context.Context, log *logger.Logger, err error, msg string) {
	if ctx != nil && ctx.Err() != nil {
		log.WithError(err).Debug(msg)
		return
	}
	log.WithError(err).Error(msg)
}
