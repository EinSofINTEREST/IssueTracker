// Package worker provides Kafka-based crawler worker pool implementation.
//
// worker 패키지는 Kafka consumer group 기반 크롤러 워커 풀을 제공합니다.
// KafkaConsumerPool은 여러 goroutine이 병렬로 CrawlJob을 처리하도록 관리합니다.
package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"issuetracker/internal/crawler/core"
	"issuetracker/internal/storage/service"
	"issuetracker/pkg/links"
	"issuetracker/pkg/logger"
	"issuetracker/pkg/queue"
	"issuetracker/pkg/urlguard"
)

// drainTimeout 은 graceful shutdown 으로 ctx 가 canceled 된 뒤 Kafka publish/commit 을
// 한 번 더 시도할 때 사용하는 별도 context 의 타임아웃입니다.
// at-least-once 시맨틱 보장을 위해 ctx canceled 직후 in-flight 메시지의 DLQ 발행·재큐잉·
// offset 커밋을 마무리할 시간을 확보합니다.
//
// validate worker (internal/processor/validate/worker.go) 의 동일 상수와 정책을 일치시킵니다.
const drainTimeout = 5 * time.Second

// JobHandler는 CrawlJob을 처리하는 인터페이스입니다.
// 구현체는 여러 goroutine에서 동시에 호출되므로 goroutine-safe해야 합니다.
// Handle은 크롤링 및 파싱 결과를 []*core.Content로 반환합니다.
// RSS처럼 다수의 기사를 반환하는 경우 슬라이스에 여러 항목이 담깁니다.
// 처리할 내용이 없으면 nil, nil을 반환합니다.
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

// KafkaConsumerPool은 Kafka consumer group 기반 crawler worker pool입니다.
//
// Kafka 토픽에서 CrawlJob 메시지를 읽어 내부 채널을 통해 worker goroutine에 분배합니다.
// 각 job의 처리 결과(Content)는 contents DB 테이블에 저장된 뒤 ContentRef만
// issuetracker.normalized 토픽에 발행됩니다.
//
// 종료 순서 보장:
//  1. ctx cancel → pollMessages 루프 탈출 → pollDone 닫힘
//  2. Stop이 pollDone 대기 후 jobs 채널 close (send on closed channel panic 방지)
//  3. worker goroutine들이 jobs 드레인 후 종료
//  4. consumer.Close()
type KafkaConsumerPool struct {
	consumer    queue.Consumer
	producer    queue.Producer
	handler     JobHandler
	contentSvc  service.ContentService
	workerCount int
	jobs        chan jobItem
	wg          sync.WaitGroup
	cbRegistry  *CircuitBreakerRegistry
	jobLocker   JobLocker
	urlCache    URLCache
	// normalizer는 URL 정규화기입니다.
	// 미설정(nil) 이면 정규화가 적용되지 않으며, 기존 동작이 유지됩니다.
	// atomic.Pointer 를 사용하여 polling/worker goroutine 의 동시 Load 와
	// SetNormalizer 의 Store 사이에 race 가 발생하지 않도록 보장합니다.
	normalizer atomic.Pointer[links.Normalizer]
	// gate 는 URL 가드 (이슈 #119) 입니다.
	// 미설정(nil) 이면 가드 비활성 — 모든 job 이 처리됩니다.
	// processJob 진입 직후 검사하여 차단된 URL 의 처리를 skip 하고 message 만 commit.
	// atomic.Pointer 로 race-safe 한 lock-free 설정/조회.
	gate atomic.Pointer[urlguard.Gate]
	// pollDone은 pollMessages goroutine이 완전히 종료됐음을 알리는 신호입니다.
	// close(p.jobs) 전에 반드시 이 채널이 닫혔음을 확인해야 합니다.
	pollDone chan struct{}
}

type jobItem struct {
	msg *queue.Message
	job *core.CrawlJob
}

// NewKafkaConsumerPool은 새로운 KafkaConsumerPool을 생성합니다.
//
// consumer에서 메시지를 읽어 handler로 처리하고,
// 결과 Content를 contents DB에 저장한 뒤 ContentRef를
// issuetracker.normalized 토픽에 발행합니다.
// workerCount는 동시에 실행되는 처리 goroutine 수를 결정합니다.
func NewKafkaConsumerPool(
	consumer queue.Consumer,
	producer queue.Producer,
	handler JobHandler,
	contentSvc service.ContentService,
	workerCount int,
) *KafkaConsumerPool {
	return NewKafkaConsumerPoolWithOptions(
		consumer, producer, handler, contentSvc, workerCount,
		NewCircuitBreakerRegistry(DefaultCircuitBreakerConfig),
		NoopJobLocker{},
		NoopURLCache{},
	)
}

// NewKafkaConsumerPoolWithCB는 외부에서 주입한 CircuitBreakerRegistry를 사용하는
// KafkaConsumerPool을 생성합니다. 테스트에서 circuit breaker 동작을 제어할 때 사용합니다.
func NewKafkaConsumerPoolWithCB(
	consumer queue.Consumer,
	producer queue.Producer,
	handler JobHandler,
	contentSvc service.ContentService,
	workerCount int,
	cbRegistry *CircuitBreakerRegistry,
) *KafkaConsumerPool {
	return NewKafkaConsumerPoolWithOptions(
		consumer, producer, handler, contentSvc, workerCount,
		cbRegistry,
		NoopJobLocker{},
		NoopURLCache{},
	)
}

// NewKafkaConsumerPoolWithOptions는 모든 의존성을 외부에서 주입하는 생성자입니다.
// 테스트에서 circuit breaker, job locker를 개별 제어할 때 사용합니다.
func NewKafkaConsumerPoolWithOptions(
	consumer queue.Consumer,
	producer queue.Producer,
	handler JobHandler,
	contentSvc service.ContentService,
	workerCount int,
	cbRegistry *CircuitBreakerRegistry,
	jobLocker JobLocker,
	urlCache URLCache,
) *KafkaConsumerPool {
	return &KafkaConsumerPool{
		consumer:    consumer,
		producer:    producer,
		handler:     handler,
		contentSvc:  contentSvc,
		workerCount: workerCount,
		cbRegistry:  cbRegistry,
		jobLocker:   jobLocker,
		urlCache:    urlCache,
		// 버퍼 크기: worker 수의 2배로 polling과 처리 사이의 지연을 흡수
		jobs:     make(chan jobItem, workerCount*2),
		pollDone: make(chan struct{}),
	}
}

// SetGate 는 processJob 진입 시 URL 검사에 사용할 urlguard.Gate 를 설정합니다 (이슈 #119).
// 미설정(nil) 시 가드 비활성 — 모든 job 이 정상 처리됩니다.
//
// 차단 시 동작: handler.Handle 호출 없이 메시지만 commit (큐에서 제거).
// → 누적된 stale URL 메시지가 retry 사이클에 다시 발행되는 것을 차단.
//
// 동시성: atomic.Pointer 기반 lock-free — Start 이후 변경에도 race-safe.
func (p *KafkaConsumerPool) SetGate(g *urlguard.Gate) {
	p.gate.Store(g)
}

// SetNormalizer는 URL 정규화기를 설정합니다.
// nil 이거나 미호출 시 정규화는 적용되지 않으며, 기존 동작이 유지됩니다.
//
// 적용 지점:
//  1. Target.URL — pollMessages 에서 unmarshal 직후 (URLCache·JobLocker 키 일관성)
//  2. Content.CanonicalURL — publishNormalized 에서 Store 직전 (DB 중복 탐지)
//  3. Kafka 파티션 키 — publishNormalized 에서 CanonicalURL 사용 (파티션 ordering)
//
// 동시성: atomic.Pointer 기반 lock-free 설정/조회로 race-safe 합니다.
// Start 이후 변경 시에도 worker goroutine 의 Load 와 race 가 발생하지 않습니다.
func (p *KafkaConsumerPool) SetNormalizer(n *links.Normalizer) {
	p.normalizer.Store(n)
}

// normalizeURL은 normalizer가 설정된 경우 URL을 정규화하여 반환합니다.
// normalizer 미설정 / 빈 URL / 정규화 실패 시 원본을 그대로 반환하여
// 정규화 도입이 기존 fetch/저장 경로를 깨지 않도록 보장합니다.
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

// Start는 worker goroutine들과 message polling goroutine을 시작합니다.
// context가 cancel되면 polling이 중단되고 진행 중인 작업이 완료됩니다.
func (p *KafkaConsumerPool) Start(ctx context.Context) {
	for i := 0; i < p.workerCount; i++ {
		p.wg.Add(1)
		go p.worker(ctx)
	}

	// pollDone은 pollMessages가 완전히 종료된 후 닫힙니다.
	// Stop에서 close(p.jobs) 전에 이 채널을 대기하여 send on closed channel panic을 방지합니다.
	go func() {
		defer close(p.pollDone)
		p.pollMessages(ctx)
	}()
}

// Stop은 pool을 정상 종료합니다.
//
// 종료 순서:
//  1. pollMessages goroutine 종료 대기 (ctx cancel로 이미 탈출 중)
//  2. jobs 채널 close — poll이 종료된 이후이므로 send/close 경합 없음
//  3. worker goroutine 드레인 대기
//  4. consumer close
func (p *KafkaConsumerPool) Stop(ctx context.Context) error {
	log := logger.FromContext(ctx)

	// 1단계: poll goroutine이 완전히 종료될 때까지 대기
	// ctx(shutdown timeout)가 만료되면 강제 진행하되, 이 경우 worker도 곧 timeout으로 종료됨
	select {
	case <-p.pollDone:
	case <-ctx.Done():
		log.Warn("poll goroutine shutdown timeout, proceeding with force close")
	}

	// 2단계: poll이 종료된 이후에만 jobs 채널 close
	// poll goroutine이 더 이상 send하지 않으므로 send on closed channel panic 없음
	close(p.jobs)

	// 3단계: worker goroutine 드레인 대기
	workersDone := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(workersDone)
	}()

	select {
	case <-workersDone:
		log.Info("all crawler workers finished gracefully")
	case <-ctx.Done():
		log.Warn("worker pool shutdown timeout, forcing close")
	}

	return p.consumer.Close()
}

func (p *KafkaConsumerPool) pollMessages(ctx context.Context) {
	log := logger.FromContext(ctx)

	for {
		msg, err := p.consumer.FetchMessage(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}

			log.WithError(err).Error("failed to receive kafka message")
			continue
		}

		job, err := core.UnmarshalCrawlJob(msg.Value)
		if err != nil {
			log.WithError(err).Error("failed to unmarshal crawl job, sending to dlq")

			// DLQ 발행 성공 시에만 원본 offset을 commit합니다.
			// 발행 실패 시 commit을 건너뛰면 재소비 시 재시도되고,
			// 발행 성공 시 commit을 수행해야 동일 메시지의 DLQ 중복 전송 루프를 방지할 수 있습니다.
			if dlqErr := p.sendToDLQ(ctx, msg, err); dlqErr != nil {
				logShutdownAware(ctx, log, dlqErr, "failed to send unmarshal-failed message to dlq, skipping commit to preserve message")
				continue
			}
			if commitErr := p.commitMessage(ctx, msg); commitErr != nil {
				logShutdownAware(ctx, log, commitErr, "failed to commit message after unmarshal-failure dlq")
			}
			continue
		}

		// 적용 지점 ①: Target.URL 정규화
		// 다운스트림 모든 키(URLCache, JobLocker, Kafka 파티션)가 동일한 정규형을
		// 사용하도록 가장 이른 시점에 한 번만 적용합니다.
		// normalizer 미설정 시 원본이 반환되어 기존 동작이 유지됩니다.
		job.Target.URL = p.normalizeURL(job.Target.URL)

		select {
		case p.jobs <- jobItem{msg: msg, job: job}:
		case <-ctx.Done():
			return
		}
	}
}

func (p *KafkaConsumerPool) worker(ctx context.Context) {
	defer p.wg.Done()

	log := logger.FromContext(ctx)

	for item := range p.jobs {
		if err := p.processJob(ctx, item); err != nil {
			// graceful shutdown 으로 발생한 context.Canceled 는 정상 종료 흐름이므로
			// DEBUG 로 강등하여 알림·대시보드에서 오탐을 만들지 않도록 합니다.
			jobLog := log.WithFields(map[string]interface{}{
				"job_id":  item.job.ID,
				"crawler": item.job.CrawlerName,
			})
			logShutdownAware(ctx, jobLog, err, "job processing failed")
		}
	}
}

func (p *KafkaConsumerPool) processJob(ctx context.Context, item jobItem) error {
	log := logger.FromContext(ctx)

	// URL 가드 (이슈 #119): processJob 진입 직후 차단 URL 을 즉시 거르고 commit
	// → handler 호출·lock 획득·backoff 대기 등 모든 비용 회피
	// → stale URL 메시지가 큐에서 즉시 제거되어 retry 사이클 차단
	//
	// item.job 은 pollMessages 에서 unmarshal 성공 후에만 jobs 채널로 전송되므로
	// 본 시점에 nil 일 수 없음 (방어 검사 불필요).
	if g := p.gate.Load(); g != nil {
		if !g.Allow(item.job.Target.URL, map[string]interface{}{
			"crawler": item.job.CrawlerName,
			"job_id":  item.job.ID,
			"stage":   "consumer",
		}) {
			return p.commitMessage(ctx, item.msg)
		}
	}

	// 재큐잉 시 설정된 ScheduledAt이 미래면 해당 시점까지 대기합니다.
	// JobLocker 획득 전에 대기하는 이유는 다음과 같습니다:
	//  - 락을 먼저 잡으면 backoff 대기 시간(최대 5분)이 TTL(10분)을 소진시킴
	//  - 다른 worker가 동일 job을 "처리 중"으로 오인해 불필요한 중복 스킵 발생
	// time.After가 아닌 NewTimer + Stop을 사용하여 ctx 취소 시 타이머 자원을 즉시 해제합니다.
	if delay := time.Until(item.job.ScheduledAt); delay > 0 {
		log.WithFields(map[string]interface{}{
			"job_id":   item.job.ID,
			"crawler":  item.job.CrawlerName,
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

	// 동일 job_id가 여러 worker에서 동시에 처리되는 것을 방지합니다.
	// Kafka rebalance/재시작으로 동일 메시지가 중복 소비될 때 idempotency를 보장합니다.
	// backoff 대기 이후에 Acquire하여 락 점유 시간을 실제 처리 구간으로 최소화합니다.
	acquired, err := p.jobLocker.Acquire(ctx, item.job.ID)
	if err != nil {
		// 락 획득 오류 시 처리를 건너뛰지 않고 경고 후 진행합니다.
		// JobLocker 장애가 크롤링 전체를 중단시키지 않도록 합니다.
		log.WithFields(map[string]interface{}{
			"job_id":  item.job.ID,
			"crawler": item.job.CrawlerName,
		}).WithError(err).Warn("failed to acquire job lock, proceeding without lock")
	} else if !acquired {
		log.WithFields(map[string]interface{}{
			"job_id":  item.job.ID,
			"crawler": item.job.CrawlerName,
		}).Debug("job already being processed by another worker, skipping")
		// 다른 워커가 처리 중이므로 commit 없이 종료합니다.
		// 처리 담당 워커의 commit에 의존하여, 해당 워커 장애 시 재처리가 보장되도록 합니다.
		return nil
	} else {
		defer func() {
			// 셧다운 시 ctx가 취소되어도 락 해제는 반드시 수행되어야 합니다.
			// 작업 ctx를 그대로 사용하면 Redis 호출이 즉시 실패해 락이 TTL(10분) 동안 점유됩니다.
			releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if releaseErr := p.jobLocker.Release(releaseCtx, item.job.ID); releaseErr != nil {
				log.WithFields(map[string]interface{}{
					"job_id":  item.job.ID,
					"crawler": item.job.CrawlerName,
				}).WithError(releaseErr).Warn("failed to release job lock")
			}
		}()
	}

	// 카테고리 페이지는 URL 캐시 대상에서 제외합니다.
	// 카테고리 페이지는 매 주기마다 새 기사 URL을 추출해야 하므로 캐싱하면 안 됩니다.
	targetURL := item.job.Target.URL
	if targetURL != "" && item.job.Target.Type != core.TargetTypeCategory {
		cached, cacheErr := p.urlCache.Exists(ctx, targetURL)
		if cacheErr != nil {
			// 캐시 장애 시 fetch를 차단하지 않고 경고 후 진행합니다.
			log.WithFields(map[string]interface{}{
				"job_id":  item.job.ID,
				"crawler": item.job.CrawlerName,
				"url":     targetURL,
			}).WithError(cacheErr).Warn("url cache check failed, proceeding with fetch")
		} else if cached {
			log.WithFields(map[string]interface{}{
				"job_id":  item.job.ID,
				"crawler": item.job.CrawlerName,
				"url":     targetURL,
			}).Debug("url already cached, skipping fetch")
			return p.commitMessage(ctx, item.msg)
		}
	}

	log.WithFields(map[string]interface{}{
		"job_id":  item.job.ID,
		"crawler": item.job.CrawlerName,
		"url":     item.job.Target.URL,
	}).Info("crawl job started")

	// circuit breaker가 open이면 DLQ로 보내고 원본을 commit합니다.
	// 소스 복구 후 운영자가 DLQ에서 재처리합니다.
	cb := p.cbRegistry.Get(item.job.CrawlerName)
	if !cb.Allow() {
		cbErr := &ErrCircuitOpen{Source: item.job.CrawlerName}
		log.WithFields(map[string]interface{}{
			"job_id":  item.job.ID,
			"crawler": item.job.CrawlerName,
		}).Warn("circuit breaker open, sending job to dlq")

		jobLog := log.WithFields(map[string]interface{}{
			"job_id":  item.job.ID,
			"crawler": item.job.CrawlerName,
		})
		if publishErr := p.sendToDLQ(ctx, item.msg, cbErr); publishErr != nil {
			logShutdownAware(ctx, jobLog, publishErr, "failed to send circuit-broken job to dlq, skipping commit to preserve message")
			return fmt.Errorf("circuit open dlq for job %s: %w", item.job.ID, publishErr)
		}

		if commitErr := p.commitMessage(ctx, item.msg); commitErr != nil {
			logShutdownAware(ctx, jobLog, commitErr, "failed to commit message after circuit breaker dlq")
		}

		return cbErr
	}

	contents, err := p.handler.Handle(ctx, item.job)
	if err != nil {
		cb.RecordFailure()

		// 재발행(requeue/DLQ)이 성공한 경우에만 원본 offset을 commit
		// 발행 실패 시 commit하지 않아 offset이 남고, 재소비 시 재처리
		var publishErr error
		if item.job.RetryCount >= item.job.MaxRetries {
			publishErr = p.sendToDLQ(ctx, item.msg, err)
		} else {
			publishErr = p.requeueWithRetry(ctx, item.job, err)
		}

		jobLog := log.WithFields(map[string]interface{}{
			"job_id":  item.job.ID,
			"crawler": item.job.CrawlerName,
		})
		if publishErr != nil {
			logShutdownAware(ctx, jobLog, publishErr, "failed to republish job, skipping commit to preserve message")
			return fmt.Errorf("handle job %s: republish: %w", item.job.ID, publishErr)
		}

		if commitErr := p.commitMessage(ctx, item.msg); commitErr != nil {
			logShutdownAware(ctx, jobLog, commitErr, "failed to commit message after error handling")
		}

		return fmt.Errorf("handle job %s: %w", item.job.ID, err)
	}

	if len(contents) == 0 {
		// handler가 빈 슬라이스나 nil을 반환해도 fetch 자체는 성공했으므로 캐시에 등록합니다.
		// 다음 주기에 동일 URL을 불필요하게 재요청하는 것을 방지합니다.
		p.cacheURLIfEligible(ctx, item, log)
		cb.RecordSuccess()
		return p.commitMessage(ctx, item.msg)
	}

	for _, c := range contents {
		if err := p.publishNormalized(ctx, c, item.job); err != nil {
			// publishNormalized 실패는 DB 저장/Kafka 발행 문제로 소스 자체의 건전성과 무관하지만
			// 작업이 완결되지 않았으므로 성공으로 기록하지 않고 반환합니다.
			return fmt.Errorf("publish normalized for job %s: %w", item.job.ID, err)
		}
	}

	p.cacheURLIfEligible(ctx, item, log)

	// 모든 부수 효과(DB 저장 + Kafka 발행)가 성공한 시점에 circuit breaker에 성공 기록
	cb.RecordSuccess()
	return p.commitMessage(ctx, item.msg)
}

// publishNormalized는 Content를 contents DB에 저장한 뒤 ContentRef를
// issuetracker.normalized 토픽에 ProcessingMessage로 발행합니다.
// 다운스트림 validator는 ref.ID로 DB에서 전체 데이터를 조회합니다.
//
// URL 정규화 (적용 지점 ②, ③):
//   - CanonicalURL: 파서가 채운 값(예: <link rel="canonical">)을 우선 정규화하고,
//     비어있으면 content.URL 을 정규화하여 채움 (파서 결정 우선순위 보존)
//   - Kafka 파티션 키: CanonicalURL 사용 (동일 기사가 동일 파티션으로 라우팅)
//
// 주의: 현재 contents DB 의 unique 제약은 url 컬럼 (idx_contents_url) 이므로
// CanonicalURL 정규화는 *DB 레벨* 중복 방지가 아닌 다운스트림 정규형 비교 및
// 파티션 키 일관성에 기여합니다. DB 레벨 중복 방지는 별도 PR 에서 url 컬럼
// 정규화 또는 unique 제약 변경으로 다뤄집니다.
//
// normalizer 미설정 시 normalizeURL 이 원본을 반환하여 기존 동작이 유지됩니다.
func (p *KafkaConsumerPool) publishNormalized(ctx context.Context, content *core.Content, job *core.CrawlJob) error {
	// 적용 지점 ②: CanonicalURL 정규화 (Store 직전)
	// 우선순위: 파서가 채운 CanonicalURL > content.URL
	// 파서가 <link rel="canonical"> 를 추출해 CanonicalURL 을 채운 경우 이를 보존하고,
	// 비어있을 때만 content.URL 을 fallback 으로 사용합니다.
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

	// 적용 지점 ③: Kafka 파티션 키 — CanonicalURL 사용
	// 동일 기사의 추적 파라미터/스킴 변형이 동일 파티션으로 라우팅되어
	// ordering·CB·중복 처리 일관성이 보장됩니다.
	// CanonicalURL 이 비어있으면 안전하게 content.URL 로 fallback 합니다.
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

	return p.producer.Publish(ctx, msg)
}

// cacheURLIfEligible은 fetch 성공 후 URL을 캐시에 등록합니다.
// 카테고리 페이지는 매 주기마다 새 기사 URL을 추출해야 하므로 캐시 대상에서 제외합니다.
// 캐시 등록 실패는 다음 주기에 중복 fetch가 발생할 뿐이므로 에러를 전파하지 않습니다.
func (p *KafkaConsumerPool) cacheURLIfEligible(ctx context.Context, item jobItem, log *logger.Logger) {
	url := item.job.Target.URL
	if url != "" && item.job.Target.Type != core.TargetTypeCategory {
		if cacheErr := p.urlCache.Set(ctx, url); cacheErr != nil {
			log.WithFields(map[string]interface{}{
				"job_id":  item.job.ID,
				"crawler": item.job.CrawlerName,
				"url":     url,
			}).WithError(cacheErr).Warn("failed to cache url after successful fetch")
		}
	}
}

// commitMessage 는 Kafka offset 을 commit 합니다.
// graceful shutdown 으로 ctx 가 canceled 된 경우 drain context (timeout 포함, parent
// cancel 분리) 로 한 번 더 시도하여 at-least-once 정확도를 높입니다.
//
// 재시도까지 실패하면 에러를 반환합니다 — 호출자(worker)는 이 에러를 보고 적절한 레벨
// (context.Canceled → DEBUG, 그 외 → ERROR) 로 로깅합니다. commit 되지 않은 offset 은
// 다음 worker 기동 시 재소비되어 동일 메시지가 다시 처리됩니다.
func (p *KafkaConsumerPool) commitMessage(ctx context.Context, msg *queue.Message) error {
	err := p.consumer.CommitMessages(ctx, msg)
	if err == nil {
		return nil
	}

	// graceful shutdown 으로 ctx 가 canceled 된 경우, drain context 로 한 번 더 시도
	// context.WithoutCancel 로 cancellation 만 분리하고 trace ID·logger 필드 등 메타데이터는 보존
	if errors.Is(err, context.Canceled) {
		drainCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), drainTimeout)
		defer cancel()
		if retryErr := p.consumer.CommitMessages(drainCtx, msg); retryErr == nil {
			return nil
		} else {
			// errors.Join 으로 최초 cancel 과 retryErr 를 모두 보존 — 호출자의
			// errors.Is(err, context.Canceled) 분기가 안정적으로 매칭되도록 보장.
			// retryErr 가 DeadlineExceeded 인 경우에도 chain 의 cancel 이 살아있음.
			return fmt.Errorf("commit offset (drain retry failed): %w", errors.Join(err, retryErr))
		}
	}

	return fmt.Errorf("commit offset: %w", err)
}

// sendToDLQ 는 메시지를 DLQ 토픽에 발행합니다.
// graceful shutdown 으로 ctx 가 canceled 된 경우 drain context 로 한 번 더 시도하여
// 데이터 무결성 작업(원본 메시지 보존)이 셧다운 직전에도 완료될 수 있도록 보장합니다.
//
// 로깅 정책: 본 함수는 로그를 직접 남기지 않고 에러만 반환합니다.
// 호출자(pollMessages, processJob)가 logShutdownAware 로 통일된 강등 정책에 따라
// 로깅하므로 이중 로깅을 방지합니다.
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

	err := p.producer.Publish(ctx, dlqMsg)
	if err != nil && errors.Is(err, context.Canceled) {
		drainCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), drainTimeout)
		defer cancel()
		// errors.Join 으로 최초 cancel 과 retryErr 를 모두 보존 — 호출자의
		// errors.Is(err, context.Canceled) 분기가 안정적으로 매칭되도록 보장.
		if retryErr := p.producer.Publish(drainCtx, dlqMsg); retryErr != nil {
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

	// RetryCount 기반 exponential backoff 계산 후 ScheduledAt에 저장합니다.
	// worker는 메시지를 소비한 뒤 processJob에서 ScheduledAt까지 대기하여 backoff를 적용합니다.
	backoffDelay := core.CalculateBackoff(kafkaRequeuePolicy, job.RetryCount)
	job.ScheduledAt = time.Now().Add(backoffDelay)

	data, err := job.Marshal()
	if err != nil {
		log.WithFields(map[string]interface{}{
			"job_id":  job.ID,
			"crawler": job.CrawlerName,
		}).WithError(err).Error("failed to marshal job for retry")
		return fmt.Errorf("marshal job for retry: %w", err)
	}

	topic := topicForPriority(job.Priority)

	msg := queue.Message{
		Topic: topic,
		Key:   []byte(job.ID),
		Value: data,
		Headers: map[string]string{
			"retry-count": fmt.Sprintf("%d", job.RetryCount),
			"last-error":  reason.Error(),
		},
	}

	log.WithFields(map[string]interface{}{
		"job_id":   job.ID,
		"crawler":  job.CrawlerName,
		"retry":    job.RetryCount,
		"delay_ms": backoffDelay.Milliseconds(),
		"topic":    topic,
	}).Warn("requeuing job with backoff delay")

	// 로깅 정책: 본 함수는 publish 실패 로그를 직접 남기지 않고 에러만 반환합니다.
	// 호출자(processJob)가 logShutdownAware 로 통일된 강등 정책에 따라 로깅합니다.
	err = p.producer.Publish(ctx, msg)
	if err != nil && errors.Is(err, context.Canceled) {
		// graceful shutdown 으로 ctx 가 canceled 된 경우, drain context 로 한 번 더 시도
		drainCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), drainTimeout)
		defer cancel()
		// errors.Join 으로 최초 cancel 과 retryErr 를 모두 보존
		if retryErr := p.producer.Publish(drainCtx, msg); retryErr != nil {
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

// logShutdownAware 는 graceful shutdown 으로 발생한 컨텍스트성 에러를 DEBUG 로,
// 그 외 에러는 ERROR 로 기록하는 공통 헬퍼입니다.
//
// shutdown 직후 in-flight Kafka 작업(DLQ publish, commit, requeue)이 ctx cancel 로
// 실패하는 것은 정상 종료 흐름의 일부이므로 ERROR 알림에서 제외합니다.
//
// 강등 조건은 ctx.Err() != nil 단독:
//   - "셧다운으로 인한 cancel 인지" 의 진실의 단일 소스(SoT)는 호출자 ctx 의 상태.
//   - 일부 라이브러리(예: chromedp)는 정상 timeout 후 내부 cleanup 으로 errors chain 에
//     context.Canceled 를 포함시키는 경우가 있음. errors.Is(err, context.Canceled) 로
//     판별하면 이 정상 케이스가 셧다운으로 오인되어 운영 모니터링에서 누락됨 (false positive).
//   - drain context (context.WithoutCancel(ctx) + WithTimeout) 가 자체 timeout 으로
//     context.DeadlineExceeded 를 반환하는 경우에도, 그 시점에 부모 ctx 가 cancel 됐다면
//     ctx.Err() 검사로 셧다운으로 분류됨.
func logShutdownAware(ctx context.Context, log *logger.Logger, err error, msg string) {
	if ctx != nil && ctx.Err() != nil {
		log.WithError(err).Debug(msg)
		return
	}
	log.WithError(err).Error(msg)
}

// topicForPriority는 우선순위에 맞는 Kafka 토픽 이름을 반환합니다.
func topicForPriority(p core.Priority) string {
	switch p {
	case core.PriorityHigh:
		return queue.TopicCrawlHigh
	case core.PriorityLow:
		return queue.TopicCrawlLow
	default:
		return queue.TopicCrawlNormal
	}
}
