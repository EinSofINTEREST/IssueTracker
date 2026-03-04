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

  "issuetracker/internal/crawler/core"
  "issuetracker/internal/storage/service"
  "issuetracker/pkg/logger"
  "issuetracker/pkg/queue"
)

// JobHandler는 CrawlJob을 처리하는 인터페이스입니다.
// 구현체는 여러 goroutine에서 동시에 호출되므로 goroutine-safe해야 합니다.
type JobHandler interface {
  Handle(ctx context.Context, job *core.CrawlJob) (*core.RawContent, error)
}

// KafkaConsumerPool은 Kafka consumer group 기반 crawler worker pool입니다.
//
// Kafka 토픽에서 CrawlJob 메시지를 읽어 내부 채널을 통해 worker goroutine에 분배합니다.
// 각 job의 RawContent는 먼저 rawContentSvc를 통해 Postgres에 저장되고,
// HTML을 제외한 경량 RawContentRef(ID만 포함)만 raw 토픽에 발행합니다.
// 이를 통해 대용량 페이지(예: CNN ~5.6MB)의 Kafka 메시지 크기 초과 문제를 방지합니다.
type KafkaConsumerPool struct {
  consumer      queue.Consumer
  producer      queue.Producer
  handler       JobHandler
  rawContentSvc service.RawContentService // RawContent를 Postgres에 저장하는 서비스
  workerCount   int
  rawTopicFn    RawTopicFunc // 국가 코드 → raw 토픽 이름 결정 함수
  jobs          chan jobItem
  wg            sync.WaitGroup
}

type jobItem struct {
  msg *queue.Message
  job *core.CrawlJob
}

// NewKafkaConsumerPool은 새로운 KafkaConsumerPool을 생성합니다.
//
// consumer에서 메시지를 읽어 handler로 처리하고, rawContentSvc로 Postgres에 저장한 뒤
// rawTopicFn이 반환하는 토픽에 RawContentRef를 발행합니다.
// workerCount는 동시에 실행되는 처리 goroutine 수를 결정합니다.
// rawTopicFn이 nil이면 DefaultRawTopicFunc를 사용합니다.
// rawContentSvc가 nil이면 에러를 반환합니다.
//
// TODO: 향후 Postgres 부하 절감을 위해 rawContentSvc 앞단에 in-memory 캐시(예: LRU)를
// 추가하여 동일 URL의 중복 저장 요청을 줄이는 캐싱 레이어를 구현할 것.
func NewKafkaConsumerPool(
  consumer queue.Consumer,
  producer queue.Producer,
  handler JobHandler,
  rawContentSvc service.RawContentService,
  workerCount int,
  rawTopicFn RawTopicFunc,
) (*KafkaConsumerPool, error) {
  // rawContentSvc는 필수 의존성입니다.
  // nil이면 processJob()에서 Store() 호출 시 런타임 패닉이 발생하므로
  // 초기화 단계에서 명확히 실패합니다.
  if rawContentSvc == nil {
    return nil, fmt.Errorf("NewKafkaConsumerPool: rawContentSvc가 nil이면 안 됩니다")
  }
  if rawTopicFn == nil {
    rawTopicFn = DefaultRawTopicFunc
  }
  return &KafkaConsumerPool{
    consumer:      consumer,
    producer:      producer,
    handler:       handler,
    rawContentSvc: rawContentSvc,
    workerCount:   workerCount,
    rawTopicFn:    rawTopicFn,
    // 버퍼 크기: worker 수의 2배로 polling과 처리 사이의 지연을 흡수
    jobs: make(chan jobItem, workerCount*2),
  }, nil
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

      log.WithError(err).Error("Kafka 메시지 수신 실패")
      continue
    }

    job, err := core.UnmarshalCrawlJob(msg.Value)
    if err != nil {
      log.WithError(err).Error("CrawlJob 역직렬화 실패, DLQ로 전송")
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
      }).WithError(err).Error("job 처리 실패")
    }
  }
}

func (p *KafkaConsumerPool) processJob(ctx context.Context, item jobItem) error {
  log := logger.FromContext(ctx)

  log.WithFields(map[string]interface{}{
    "job_id":  item.job.ID,
    "crawler": item.job.CrawlerName,
    "url":     item.job.Target.URL,
  }).Info("crawl job 처리 시작")

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

  // Postgres에 HTML 본문을 포함한 전체 RawContent 저장
  // 중복 URL이면 기존 ID를 반환하며 에러를 반환하지 않습니다
  id, _, err := p.rawContentSvc.Store(ctx, raw)
  if err != nil {
    return fmt.Errorf("store raw content for job %s: %w", item.job.ID, err)
  }

  // Kafka에는 ID만 포함된 경량 참조 메시지 발행 (1MB 한도 초과 방지)
  ref := &core.RawContentRef{
    ID:         id,
    URL:        raw.URL,
    FetchedAt:  raw.FetchedAt,
    SourceInfo: raw.SourceInfo,
  }

  if err := p.publishRawRef(ctx, ref, item.job); err != nil {
    return fmt.Errorf("publish raw ref for job %s: %w", item.job.ID, err)
  }

  return p.commitMessage(ctx, item.msg)
}

func (p *KafkaConsumerPool) publishRawRef(ctx context.Context, ref *core.RawContentRef, job *core.CrawlJob) error {
  data, err := json.Marshal(ref)
  if err != nil {
    return fmt.Errorf("RawContentRef 직렬화 실패: %w", err)
  }

  msg := queue.Message{
    Topic: p.rawTopicFn(ref.SourceInfo.Country), // 국가 코드 기반으로 raw 토픽 결정
    Key:   []byte(ref.URL),                       // URL을 파티션 키로 사용하여 동일 URL의 순서 보장
    Value: data,
    Headers: map[string]string{
      "source":  ref.SourceInfo.Name,
      "country": ref.SourceInfo.Country,
      "job_id":  job.ID,
    },
  }

  return p.producer.Publish(ctx, msg)
}

func (p *KafkaConsumerPool) commitMessage(ctx context.Context, msg *queue.Message) error {
  if err := p.consumer.CommitMessages(ctx, msg); err != nil {
    return fmt.Errorf("offset commit 실패: %w", err)
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
    log.WithError(err).Error("DLQ 전송 실패")
  }
}

func (p *KafkaConsumerPool) requeueWithRetry(ctx context.Context, job *core.CrawlJob, reason error) {
  log := logger.FromContext(ctx)

  job.RetryCount++

  data, err := job.Marshal()
  if err != nil {
    log.WithError(err).Error("재큐잉을 위한 job 직렬화 실패")
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
    log.WithError(err).Error("재시도 재큐잉 실패")
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
