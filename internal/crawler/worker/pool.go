// Package worker provides Kafka-based crawler worker pool implementation.
//
// worker 패키지는 Kafka consumer group 기반 크롤러 워커 풀을 제공합니다.
// KafkaConsumerPool은 여러 goroutine이 병렬로 CrawlJob을 처리하도록 관리합니다.
package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"issuetracker/internal/crawler/core"
	"issuetracker/internal/storage/service"
	"issuetracker/pkg/logger"
	"issuetracker/pkg/queue"
)

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
) *KafkaConsumerPool {
	return &KafkaConsumerPool{
		consumer:    consumer,
		producer:    producer,
		handler:     handler,
		contentSvc:  contentSvc,
		workerCount: workerCount,
		cbRegistry:  cbRegistry,
		jobLocker:   jobLocker,
		// 버퍼 크기: worker 수의 2배로 polling과 처리 사이의 지연을 흡수
		jobs:     make(chan jobItem, workerCount*2),
		pollDone: make(chan struct{}),
	}
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
			p.sendToDLQ(ctx, msg, err)
			continue
		}

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
			log.WithFields(map[string]interface{}{
				"job_id":  item.job.ID,
				"crawler": item.job.CrawlerName,
			}).WithError(err).Error("job processing failed")
		}
	}
}

func (p *KafkaConsumerPool) processJob(ctx context.Context, item jobItem) error {
	log := logger.FromContext(ctx)

	// 동일 job_id가 여러 worker에서 동시에 처리되는 것을 방지합니다.
	// Kafka rebalance/재시작으로 동일 메시지가 중복 소비될 때 idempotency를 보장합니다.
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

	// 재큐잉 시 설정된 ScheduledAt이 미래면 해당 시점까지 대기
	if delay := time.Until(item.job.ScheduledAt); delay > 0 {
		log.WithFields(map[string]interface{}{
			"job_id":   item.job.ID,
			"crawler":  item.job.CrawlerName,
			"delay_ms": delay.Milliseconds(),
		}).Debug("waiting for backoff before processing retried job")

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
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

		if publishErr := p.sendToDLQ(ctx, item.msg, cbErr); publishErr != nil {
			log.WithFields(map[string]interface{}{
				"job_id":  item.job.ID,
				"crawler": item.job.CrawlerName,
			}).WithError(publishErr).Error("failed to send circuit-broken job to dlq, skipping commit to preserve message")
			return fmt.Errorf("circuit open dlq for job %s: %w", item.job.ID, publishErr)
		}

		if commitErr := p.commitMessage(ctx, item.msg); commitErr != nil {
			log.WithFields(map[string]interface{}{
				"job_id":  item.job.ID,
				"crawler": item.job.CrawlerName,
			}).WithError(commitErr).Error("failed to commit message after circuit breaker dlq")
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

		if publishErr != nil {
			log.WithFields(map[string]interface{}{
				"job_id":  item.job.ID,
				"crawler": item.job.CrawlerName,
			}).WithError(publishErr).Error("failed to republish job, skipping commit to preserve message")
			return fmt.Errorf("handle job %s: republish: %w", item.job.ID, publishErr)
		}

		if commitErr := p.commitMessage(ctx, item.msg); commitErr != nil {
			log.WithFields(map[string]interface{}{
				"job_id":  item.job.ID,
				"crawler": item.job.CrawlerName,
			}).WithError(commitErr).Error("failed to commit message after error handling")
		}

		return fmt.Errorf("handle job %s: %w", item.job.ID, err)
	}

	cb.RecordSuccess()

	if len(contents) == 0 {
		// handler가 빈 슬라이스나 nil을 반환하면 발행 없이 commit만 수행
		return p.commitMessage(ctx, item.msg)
	}

	for _, c := range contents {
		if err := p.publishNormalized(ctx, c, item.job); err != nil {
			return fmt.Errorf("publish normalized for job %s: %w", item.job.ID, err)
		}
	}

	return p.commitMessage(ctx, item.msg)
}

// publishNormalized는 Content를 contents DB에 저장한 뒤 ContentRef를
// issuetracker.normalized 토픽에 ProcessingMessage로 발행합니다.
// 다운스트림 validator는 ref.ID로 DB에서 전체 데이터를 조회합니다.
func (p *KafkaConsumerPool) publishNormalized(ctx context.Context, content *core.Content, job *core.CrawlJob) error {
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

	msg := queue.Message{
		Topic: queue.TopicNormalized,
		Key:   []byte(content.URL),
		Value: pmBytes,
		Headers: map[string]string{
			"source":  content.SourceID,
			"country": content.Country,
			"job_id":  job.ID,
		},
	}

	return p.producer.Publish(ctx, msg)
}

func (p *KafkaConsumerPool) commitMessage(ctx context.Context, msg *queue.Message) error {
	if err := p.consumer.CommitMessages(ctx, msg); err != nil {
		return fmt.Errorf("commit offset: %w", err)
	}

	return nil
}

func (p *KafkaConsumerPool) sendToDLQ(ctx context.Context, msg *queue.Message, reason error) error {
	log := logger.FromContext(ctx)

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

	if err := p.producer.Publish(ctx, dlqMsg); err != nil {
		log.WithError(err).Error("failed to send message to dlq")
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

	if err := p.producer.Publish(ctx, msg); err != nil {
		log.WithFields(map[string]interface{}{
			"job_id":   job.ID,
			"crawler":  job.CrawlerName,
			"topic":    topic,
			"priority": job.Priority,
		}).WithError(err).Error("failed to requeue job for retry")
		return fmt.Errorf("publish retry job: %w", err)
	}

	return nil
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
