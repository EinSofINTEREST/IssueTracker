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
	return &KafkaConsumerPool{
		consumer:    consumer,
		producer:    producer,
		handler:     handler,
		contentSvc:  contentSvc,
		workerCount: workerCount,
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

	log.WithFields(map[string]interface{}{
		"job_id":  item.job.ID,
		"crawler": item.job.CrawlerName,
		"url":     item.job.Target.URL,
	}).Info("crawl job started")

	contents, err := p.handler.Handle(ctx, item.job)
	if err != nil {
		// 재시도 횟수 초과 시 DLQ로 전송, 아니면 재큐잉
		if item.job.RetryCount >= item.job.MaxRetries {
			p.sendToDLQ(ctx, item.msg, err)
		} else {
			p.requeueWithRetry(ctx, item.job, err)
		}

		// 재발행/DLQ 전송 후 원본 메시지를 반드시 커밋해야 합니다.
		if commitErr := p.commitMessage(ctx, item.msg); commitErr != nil {
			log.WithFields(map[string]interface{}{
				"job_id":  item.job.ID,
				"crawler": item.job.CrawlerName,
			}).WithError(commitErr).Error("failed to commit message after error handling")
		}

		return fmt.Errorf("handle job %s: %w", item.job.ID, err)
	}

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

func (p *KafkaConsumerPool) sendToDLQ(ctx context.Context, msg *queue.Message, reason error) {
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
	}
}

func (p *KafkaConsumerPool) requeueWithRetry(ctx context.Context, job *core.CrawlJob, reason error) {
	log := logger.FromContext(ctx)

	job.RetryCount++

	data, err := job.Marshal()
	if err != nil {
		log.WithFields(map[string]interface{}{
			"job_id":  job.ID,
			"crawler": job.CrawlerName,
		}).WithError(err).Error("failed to marshal job for retry")
		return
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

	if err := p.producer.Publish(ctx, msg); err != nil {
		log.WithFields(map[string]interface{}{
			"job_id":   job.ID,
			"crawler":  job.CrawlerName,
			"topic":    topic,
			"priority": job.Priority,
		}).WithError(err).Error("failed to requeue job for retry")
	}
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
