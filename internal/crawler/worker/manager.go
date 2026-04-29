package worker

import (
	"context"
	"fmt"
	"sync"

	"issuetracker/internal/crawler/core"
	"issuetracker/internal/storage/service"
	"issuetracker/pkg/logger"
	"issuetracker/pkg/queue"
)

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
// JobLocker가 nil이면 NoopJobLocker가 사용되어 중복 처리 방지가 비활성화됩니다.
// URLCache가 nil이면 NoopURLCache가 사용되어 URL 캐싱이 비활성화됩니다.
// RetryScheduler가 nil이면 각 Pool 이 lazy 로 KafkaImmediateRetryScheduler 를 생성하여
// 기존 동작 (즉시 Kafka publish + worker sleep) 을 유지합니다 (이슈 #82).
type ManagerConfig struct {
	High           PoolConfig
	Normal         PoolConfig
	Low            PoolConfig
	JobLocker      JobLocker
	URLCache       URLCache
	RetryScheduler RetryScheduler
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
	producer queue.Producer
	resolver PriorityResolver
	log      *logger.Logger
}

// NewPoolManager는 설정에 따라 우선순위별 KafkaConsumerPool을 생성하고 PoolManager를 반환합니다.
//
// NewPoolManager creates one KafkaConsumerPool per priority level (high/normal/low).
func NewPoolManager(
	cfg ManagerConfig,
	producer queue.Producer,
	handler JobHandler,
	contentSvc service.ContentService,
	resolver PriorityResolver,
	log *logger.Logger,
) *PoolManager {
	jobLocker := cfg.JobLocker
	if jobLocker == nil {
		jobLocker = NoopJobLocker{}
	}

	urlCache := cfg.URLCache
	if urlCache == nil {
		urlCache = NoopURLCache{}
	}

	// 세 개 Pool이 동일한 CircuitBreakerRegistry를 공유하여
	// 소스별 실패 카운팅이 우선순위 경계 없이 누적됩니다.
	// log 주입 (이슈 #137) — CB state 전이마다 INFO/WARN 로그.
	cbRegistry := NewCircuitBreakerRegistry(DefaultCircuitBreakerConfig, log)

	newPool := func(pc PoolConfig, priorityName string) *KafkaConsumerPool {
		pool := NewKafkaConsumerPoolWithOptions(
			pc.Consumer, producer, handler, contentSvc, pc.WorkerCount,
			cbRegistry, jobLocker, urlCache,
		)
		// 이슈 #137 — heartbeat 식별자 주입 (DEBUG 레벨에서 worker pool status 출력 활성)
		pool.SetPriority(priorityName)
		// 단일 RetryScheduler 인스턴스를 세 우선순위 Pool 이 공유합니다 (이슈 #82) —
		// Redis 기반 구현에서 ZSET/연결 풀이 통일되도록.
		if cfg.RetryScheduler != nil {
			pool.SetRetryScheduler(cfg.RetryScheduler)
		}
		return pool
	}

	return &PoolManager{
		pools: map[core.Priority]*KafkaConsumerPool{
			core.PriorityHigh:   newPool(cfg.High, "high"),
			core.PriorityNormal: newPool(cfg.Normal, "normal"),
			core.PriorityLow:    newPool(cfg.Low, "low"),
		},
		producer: producer,
		resolver: resolver,
		log:      log,
	}
}

// Publish는 CrawlJob을 PriorityResolver로 우선순위를 결정한 후 해당 Kafka crawl 토픽에 발행합니다.
//
// Publish resolves the job's priority via the configured PriorityResolver,
// updates job.Priority in-place, and publishes to the correct crawl topic
// (crawl.high / crawl.normal / crawl.low).
func (m *PoolManager) Publish(ctx context.Context, job *core.CrawlJob) error {
	priority := m.resolver.Resolve(job)
	job.Priority = priority

	data, err := job.Marshal()
	if err != nil {
		return fmt.Errorf("marshal job %s: %w", job.ID, err)
	}

	topic := topicForPriority(priority)

	msg := queue.Message{
		Topic: topic,
		Key:   []byte(job.ID),
		Value: data,
		Headers: map[string]string{
			"crawler":  job.CrawlerName,
			"priority": fmt.Sprintf("%d", int(priority)),
		},
	}

	m.log.WithFields(map[string]interface{}{
		"job_id":   job.ID,
		"crawler":  job.CrawlerName,
		"priority": priority,
		"topic":    topic,
	}).Info("publishing crawl job")

	return m.producer.Publish(ctx, msg)
}

// Start는 high/normal/low 모든 Pool의 goroutine을 시작합니다.
func (m *PoolManager) Start(ctx context.Context) {
	// 우선순위 순서대로 시작 (로그 가독성)
	for _, p := range []core.Priority{core.PriorityHigh, core.PriorityNormal, core.PriorityLow} {
		m.log.WithFields(map[string]interface{}{
			"priority":     p,
			"worker_count": m.pools[p].workerCount,
		}).Info("starting worker pool")
		m.pools[p].Start(ctx)
	}
}

// Stop은 모든 Pool을 동시에 graceful shutdown합니다.
//
// Stop stops all pools concurrently so they drain in parallel within the
// shared context timeout. 첫 번째로 발생한 에러를 반환하며 나머지 Pool 종료도 계속 시도합니다.
func (m *PoolManager) Stop(ctx context.Context) error {
	var (
		mu       sync.Mutex
		firstErr error
		wg       sync.WaitGroup
	)

	for p, pool := range m.pools {
		wg.Add(1)
		go func(priority core.Priority, pool *KafkaConsumerPool) {
			defer wg.Done()
			if err := pool.Stop(ctx); err != nil {
				m.log.WithFields(map[string]interface{}{
					"priority": priority,
				}).WithError(err).Error("pool stop error")

				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
			}
		}(p, pool)
	}

	wg.Wait()
	return firstErr
}
