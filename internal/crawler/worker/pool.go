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

  "ecoscrapper/internal/crawler/core"
  "ecoscrapper/pkg/logger"
  "ecoscrapper/pkg/queue"
)

// JobHandler는 CrawlJob을 처리하는 인터페이스입니다.
//
// JobHandler processes a CrawlJob and returns the fetched raw content.
// Implementations must be safe for concurrent use by multiple goroutines.
type JobHandler interface {
  Handle(ctx context.Context, job *core.CrawlJob) (*core.RawContent, error)
}

// KafkaConsumerPool은 Kafka consumer group 기반 crawler worker pool입니다.
//
// KafkaConsumerPool reads CrawlJob messages from a Kafka topic, distributes
// them to worker goroutines via an internal channel, and publishes the resulting
// RawContent to the raw topic determined by rawTopicFn. Failed jobs are retried or sent to DLQ.
type KafkaConsumerPool struct {
  consumer    queue.Consumer
  producer    queue.Producer
  handler     JobHandler
  workerCount int
  rawTopicFn  RawTopicFunc // 국가 코드 → raw 토픽 이름 결정 함수
  jobs        chan jobItem
  wg          sync.WaitGroup
}

type jobItem struct {
  msg *queue.Message
  job *core.CrawlJob
}

// NewKafkaConsumerPool은 새로운 KafkaConsumerPool을 생성합니다.
//
// NewKafkaConsumerPool creates a pool that reads from consumer, processes jobs
// via handler, and publishes raw content to the topic returned by rawTopicFn.
// workerCount controls the number of concurrent processing goroutines.
// rawTopicFn이 nil이면 DefaultRawTopicFunc를 사용합니다.
func NewKafkaConsumerPool(
  consumer queue.Consumer,
  producer queue.Producer,
  handler JobHandler,
  workerCount int,
  rawTopicFn RawTopicFunc,
) *KafkaConsumerPool {
  if rawTopicFn == nil {
    rawTopicFn = DefaultRawTopicFunc
  }
  return &KafkaConsumerPool{
    consumer:    consumer,
    producer:    producer,
    handler:     handler,
    workerCount: workerCount,
    rawTopicFn:  rawTopicFn,
    // 버퍼 크기: worker 수의 2배로 polling과 처리 사이의 지연을 흡수
    jobs: make(chan jobItem, workerCount*2),
  }
}

// Start는 worker goroutine들과 message polling goroutine을 시작합니다.
// context가 cancel되면 polling이 중단되고 진행 중인 작업이 완료됩니다.
func (p *KafkaConsumerPool) Start(ctx context.Context) {
  for i := 0; i < p.workerCount; i++ {
    p.wg.Add(1)
    go p.worker(ctx)
  }

  go p.pollMessages(ctx)
}

// Stop은 pool을 정상 종료합니다.
// jobs 채널을 닫아 모든 worker가 현재 작업을 완료한 후 종료되도록 합니다.
// ctx timeout 초과 시 강제 종료합니다.
func (p *KafkaConsumerPool) Stop(ctx context.Context) error {
  close(p.jobs)

  done := make(chan struct{})
  go func() {
    p.wg.Wait()
    close(done)
  }()

  log := logger.FromContext(ctx)

  select {
  case <-done:
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

      log.WithError(err).Error("failed to fetch message from kafka")
      continue
    }

    job, err := core.UnmarshalCrawlJob(msg.Value)
    if err != nil {
      log.WithError(err).Error("malformed crawl job message, sending to DLQ")
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
  }).Info("processing crawl job")

  raw, err := p.handler.Handle(ctx, item.job)
  if err != nil {
    // 재시도 횟수 초과 시 DLQ로 전송, 아니면 재큐잉
    if item.job.RetryCount >= item.job.MaxRetries {
      p.sendToDLQ(ctx, item.msg, err)
    } else {
      p.requeueWithRetry(ctx, item.job, err)
    }

    return fmt.Errorf("handle job %s: %w", item.job.ID, err)
  }

  if raw == nil {
    // handler가 nil을 반환하면 발행 없이 commit만 수행
    return p.commitMessage(ctx, item.msg)
  }

  if err := p.publishRaw(ctx, raw, item.job); err != nil {
    return fmt.Errorf("publish raw content for job %s: %w", item.job.ID, err)
  }

  return p.commitMessage(ctx, item.msg)
}

func (p *KafkaConsumerPool) publishRaw(ctx context.Context, raw *core.RawContent, job *core.CrawlJob) error {
  data, err := json.Marshal(raw)
  if err != nil {
    return fmt.Errorf("marshal raw content: %w", err)
  }

  msg := queue.Message{
    Topic: p.rawTopicFn(raw.SourceInfo.Country), // 국가 코드 기반으로 raw 토픽 결정
    Key:   []byte(raw.URL),                      // URL을 파티션 키로 사용하여 동일 URL의 순서 보장
    Value: data,
    Headers: map[string]string{
      "source":  raw.SourceInfo.Name,
      "country": raw.SourceInfo.Country,
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
    log.WithError(err).Error("failed to send message to DLQ")
  }
}

func (p *KafkaConsumerPool) requeueWithRetry(ctx context.Context, job *core.CrawlJob, reason error) {
  log := logger.FromContext(ctx)

  job.RetryCount++

  data, err := job.Marshal()
  if err != nil {
    log.WithError(err).Error("failed to marshal job for requeue")
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
    log.WithError(err).Error("failed to requeue job for retry")
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
