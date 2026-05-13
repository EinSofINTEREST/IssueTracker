package worker

import (
	"context"
	"fmt"
	"sync"

	"issuetracker/internal/locks"
	"issuetracker/internal/processor/fetcher/core"
	"issuetracker/internal/storage/service"
	bus "issuetracker/internal/worker"
	"issuetracker/pkg/config"
	"issuetracker/pkg/logger"
	"issuetracker/pkg/queue"
	"issuetracker/pkg/resilience"
)

// buildStageCap 은 fetcher pool 의 Semaphore capacity 를 계산합니다.
// pkg/config.CapPerStage 의 정책을 본 패키지에서도 사용 — 동일 규칙 (worker_count/2 floor, min cap).
func buildStageCap(workerCount, configured int) int {
	return config.CapPerStage(workerCount, configured)
}

// ─────────────────────────────────────────────────────────────────────────
// 설정 구조체
// ─────────────────────────────────────────────────────────────────────────

// PoolConfig는 단일 우선순위 Pool의 설정입니다.
//
// PoolConfig holds the Kafka consumer and worker goroutine count for one priority pool.
type PoolConfig struct {
	Consumer    queue.Consumer
	WorkerCount int
}

// ManagerConfig는 PoolManager 생성에 필요한 설정입니다.
//
// ManagerConfig aggregates the three per-priority pool configs.
// ProcessingLock 이 nil 이면 NoopProcessingLock 이 사용되어 중복 처리 방지가 비활성화됩니다.
// RetryScheduler가 nil이면 각 Pool 이 lazy 로 KafkaImmediateRetryScheduler 를 생성하여
// 기존 동작 (즉시 Kafka publish + worker sleep) 을 유지합니다.
//
// URL dedup 은 Ingestion Lock (Publisher 단) 으로 단일화 — worker 측 별도 cache 없음.
// 단계별 worker 간 동시처리 차단은 ProcessingLock 으로 일원화 (구 JobLocker 의 명칭 변경).
//
// Chromedp 필드는 chromedp 전용 worker pool 의 PoolConfig + ChromedpHandler.
// nil 이면 chromedp pool 미기동 (fetcher pool split 비활성 — 기존 동작 유지). Consumer 와
// Handler 둘 다 non-nil 이어야 wiring 됨.
type ManagerConfig struct {
	High           PoolConfig
	Normal         PoolConfig
	Low            PoolConfig
	ProcessingLock locks.ProcessingLock
	RetryScheduler bus.RetryScheduler

	// MaxConcurrentPerStage: fetcher stage 의 Semaphore capacity 설정값 (이슈 #356).
	// 0 이하 → 각 pool 의 WorkerCount/2 (floor) 자동.
	// 양수 → min(value, WorkerCount/2). pool 별 (high/normal/low/chromedp) 동일 cap 정책 적용.
	MaxConcurrentPerStage int

	// Chromedp 는 chromedp 전용 pool 의 Consumer + WorkerCount.
	// Consumer.nil 이면 chromedp pool 비활성.
	Chromedp PoolConfig
	// ChromedpHandler 는 chromedp pool 의 JobHandler. nil 이면 chromedp pool 비활성.
	ChromedpHandler JobHandler
}

// ─────────────────────────────────────────────────────────────────────────
// PoolManager
// ─────────────────────────────────────────────────────────────────────────

// PoolManager는 우선순위별(high/normal/low) KafkaConsumerPool을 통합 관리합니다.
//
// PoolManager orchestrates three KafkaConsumerPool instances and provides
// a Publish method that routes CrawlJobs to the correct priority Kafka topic
// using the configured PriorityResolver.
//
// 사용 흐름:
//  1. NewPoolManager로 생성
//  2. Start(ctx)로 모든 Pool 구동
//  3. Publish(ctx, job)으로 Job을 적절한 우선순위 큐에 삽입
//  4. 종료 시 Stop(ctx) 호출
type PoolManager struct {
	pools    map[core.Priority]*KafkaConsumerPool
	pub      *bus.Publisher
	resolver bus.PriorityResolver
	log      *logger.Logger

	// chromedpPool 은 chromedp 전용 worker pool. nil 이면 비활성.
	chromedpPool *KafkaConsumerPool
}

// NewPoolManager는 설정에 따라 우선순위별 KafkaConsumerPool을 생성하고 PoolManager를 반환합니다.
//
// NewPoolManager creates one KafkaConsumerPool per priority level (high/normal/low).
//
// 이슈 #390 — 구 queue.Producer 직접 주입 → publisher facade 주입으로 변경. fetcher/worker
// 는 더 이상 queue.Producer 를 직접 보유하지 않음 (Kafka I/O 단일 출처 = publisher).
func NewPoolManager(
	cfg ManagerConfig,
	pub *bus.Publisher,
	handler JobHandler,
	contentSvc service.ContentService,
	resolver bus.PriorityResolver,
	log *logger.Logger,
) *PoolManager {
	procLock := cfg.ProcessingLock
	// 세 개 Pool이 동일한 CircuitBreakerRegistry를 공유하여
	// 소스별 실패 카운팅이 우선순위 경계 없이 누적됩니다.
	// log 주입 — CB state 전이마다 INFO/WARN 로그.
	cbRegistry := resilience.NewCircuitBreakerRegistry(resilience.DefaultCircuitBreakerConfig, log)

	// per-pool StageGate 합성 (이슈 #356) — pool 별 WorkerCount/2 cap.
	// procLock nil 시 BuildStageGate 가 NoopStageGate 반환 → dedup+cap 자동 비활성.
	buildGate := func(workerCount int) locks.StageGate {
		capacity := buildStageCap(workerCount, cfg.MaxConcurrentPerStage)
		return locks.BuildStageGate(locks.StageFetcher, capacity, procLock, log)
	}

	newPool := func(pc PoolConfig, priorityName string) *KafkaConsumerPool {
		pool := NewKafkaConsumerPoolWithOptions(
			pc.Consumer, pub, handler, contentSvc, pc.WorkerCount,
			cbRegistry, buildGate(pc.WorkerCount),
		)
		// heartbeat 식별자 주입 (DEBUG 레벨에서 worker pool status 출력 활성)
		pool.SetPriority(priorityName)
		// 단일 RetryScheduler 인스턴스를 세 우선순위 Pool 이 공유합니다 —
		// Redis 기반 구현에서 ZSET/연결 풀이 통일되도록.
		if cfg.RetryScheduler != nil {
			pool.SetRetryScheduler(cfg.RetryScheduler)
		}
		return pool
	}

	mgr := &PoolManager{
		pools: map[core.Priority]*KafkaConsumerPool{
			core.PriorityHigh:   newPool(cfg.High, "high"),
			core.PriorityNormal: newPool(cfg.Normal, "normal"),
			core.PriorityLow:    newPool(cfg.Low, "low"),
		},
		pub:      pub,
		resolver: resolver,
		log:      log,
	}

	// chromedp 전용 pool wiring (Consumer + Handler 둘 다 있을 때만 활성).
	if cfg.Chromedp.Consumer != nil && cfg.ChromedpHandler != nil {
		chromedpPool := NewKafkaConsumerPoolWithOptions(
			cfg.Chromedp.Consumer, pub, cfg.ChromedpHandler, contentSvc, cfg.Chromedp.WorkerCount,
			cbRegistry, buildGate(cfg.Chromedp.WorkerCount),
		)
		chromedpPool.SetPriority("chromedp")
		if cfg.RetryScheduler != nil {
			chromedpPool.SetRetryScheduler(cfg.RetryScheduler)
		}
		mgr.chromedpPool = chromedpPool
	}

	return mgr
}

// Publish는 CrawlJob을 publisher facade 로 발행합니다. priority 결정은 publisher 내부의
// resolver chain 이 buildMessage 에서 일괄 처리합니다 (이슈 #391 — 메타 #385 Sub 6).
//
// Publish delegates to bus.PublishJob. Priority resolution is handled inside the
// publisher's resolver chain via buildMessage — single source of truth across all
// PublishX paths (seed / chained / job / retry).
//
// 로깅을 위해 동일 resolver 를 한 번 더 평가 — ExplicitPriorityResolver 가 1순위라
// 이후 buildMessage 의 평가와 같은 결과를 보장 (resolver 는 stateless · idempotent).
//
// m.resolver 가 nil 인 경우 (테스트 wiring) 는 job.Priority 를 그대로 사용 — coderabbit
// PR #409 피드백. bus.buildMessage 가 nil resolver 를 fail-safe 로 허용하는 정책과 일관.
func (m *PoolManager) Publish(ctx context.Context, job *core.CrawlJob) error {
	priority := job.Priority
	if m.resolver != nil {
		priority = m.resolver.Resolve(job)
	}

	m.log.WithFields(map[string]interface{}{
		"job_id":   job.ID,
		"crawler":  job.CrawlerName,
		"priority": priority,
		"topic":    bus.CrawlTopic(priority),
	}).Info("publishing crawl job")

	return m.pub.PublishJob(ctx, job)
}

// Start는 high/normal/low 모든 Pool의 goroutine을 시작합니다.
// chromedp pool 이 wiring 되어 있으면 함께 시작.
func (m *PoolManager) Start(ctx context.Context) {
	// 우선순위 순서대로 시작 (로그 가독성)
	for _, p := range []core.Priority{core.PriorityHigh, core.PriorityNormal, core.PriorityLow} {
		m.log.WithFields(map[string]interface{}{
			"priority":     p,
			"worker_count": m.pools[p].workerCount,
		}).Info("starting worker pool")
		m.pools[p].Start(ctx)
	}
	if m.chromedpPool != nil {
		m.log.WithFields(map[string]interface{}{
			"priority":     "chromedp",
			"worker_count": m.chromedpPool.workerCount,
		}).Info("starting worker pool")
		m.chromedpPool.Start(ctx)
	}
}

// Stop은 모든 Pool을 동시에 graceful shutdown합니다.
//
// Stop stops all pools concurrently so they drain in parallel within the
// shared context timeout. 첫 번째로 발생한 에러를 반환하며 나머지 Pool 종료도 계속 시도합니다.
// chromedp pool 이 wiring 되어 있으면 함께 종료.
func (m *PoolManager) Stop(ctx context.Context) error {
	var (
		mu       sync.Mutex
		firstErr error
		wg       sync.WaitGroup
	)

	stopPool := func(name string, pool *KafkaConsumerPool) {
		defer wg.Done()
		if err := pool.Stop(ctx); err != nil {
			m.log.WithField("pool", name).WithError(err).Error("pool stop error")
			mu.Lock()
			if firstErr == nil {
				firstErr = err
			}
			mu.Unlock()
		}
	}

	for p, pool := range m.pools {
		wg.Add(1)
		go stopPool(fmt.Sprintf("priority_%d", p), pool)
	}
	if m.chromedpPool != nil {
		wg.Add(1)
		go stopPool("chromedp", m.chromedpPool)
	}

	wg.Wait()
	return firstErr
}
