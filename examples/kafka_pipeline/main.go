// kafka_pipeline 예제는 실제 Kafka 없이 in-memory mock으로
// CrawlJob → KafkaConsumerPool → RawContent 파이프라인 전체 흐름을 검증합니다.
//
// 실행: go run ./examples/kafka_pipeline/
package main

import (
  "context"
  "encoding/json"
  "fmt"
  "strings"
  "sync"
  "time"

  "issuetracker/internal/crawler/core"
  "issuetracker/internal/crawler/worker"
  "issuetracker/pkg/logger"
  "issuetracker/pkg/queue"
)

// =========================================================
// Mock 구현체
// =========================================================

// mockProducer는 in-memory slice에 메시지를 저장하는 테스트용 Producer입니다.
type mockProducer struct {
  mu        sync.Mutex
  published []queue.Message
  onPublish func(queue.Message)
}

func (p *mockProducer) Publish(_ context.Context, msg queue.Message) error {
  p.mu.Lock()
  p.published = append(p.published, msg)
  p.mu.Unlock()

  if p.onPublish != nil {
    p.onPublish(msg)
  }

  return nil
}

func (p *mockProducer) PublishBatch(ctx context.Context, msgs []queue.Message) error {
  for _, msg := range msgs {
    if err := p.Publish(ctx, msg); err != nil {
      return err
    }
  }

  return nil
}

func (p *mockProducer) Close() error { return nil }

func (p *mockProducer) messages() []queue.Message {
  p.mu.Lock()
  defer p.mu.Unlock()

  result := make([]queue.Message, len(p.published))
  copy(result, p.published)

  return result
}

// mockConsumer는 미리 채워진 채널에서 메시지를 반환하는 테스트용 Consumer입니다.
// 채널이 비면 context가 cancel될 때까지 대기합니다.
type mockConsumer struct {
  ch chan *queue.Message
}

func newMockConsumer(msgs []*queue.Message) *mockConsumer {
  ch := make(chan *queue.Message, len(msgs))
  for _, m := range msgs {
    ch <- m
  }

  return &mockConsumer{ch: ch}
}

func (c *mockConsumer) FetchMessage(ctx context.Context) (*queue.Message, error) {
  select {
  case msg, ok := <-c.ch:
    if !ok {
      <-ctx.Done()
      return nil, ctx.Err()
    }

    return msg, nil

  case <-ctx.Done():
    return nil, ctx.Err()
  }
}

func (c *mockConsumer) CommitMessages(_ context.Context, _ ...*queue.Message) error {
  return nil
}

func (c *mockConsumer) Close() error { return nil }

// =========================================================
// 테스트용 크롤러 핸들러
// =========================================================

// testCrawlerHandler는 실제 HTTP 요청 없이 더미 RawContent를 생성합니다.
type testCrawlerHandler struct {
  log *logger.Logger
}

func (h *testCrawlerHandler) Handle(_ context.Context, job *core.CrawlJob) (*core.RawContent, error) {
  // 크롤링 지연 시뮬레이션
  time.Sleep(30 * time.Millisecond)

  h.log.WithFields(map[string]interface{}{
    "job_id":  job.ID,
    "crawler": job.CrawlerName,
    "url":     job.Target.URL,
  }).Info("crawling URL (simulated)")

  return &core.RawContent{
    ID:         job.ID,
    URL:        job.Target.URL,
    HTML:       fmt.Sprintf("<html><body><h1>Article from %s</h1></body></html>", job.CrawlerName),
    StatusCode: 200,
    FetchedAt:  time.Now(),
    SourceInfo: core.SourceInfo{
      Country:  "US",
      Name:     job.CrawlerName,
      Language: "en",
    },
  }, nil
}

// =========================================================
// Job / Message 헬퍼
// =========================================================

var testSources = []struct {
  name    string
  baseURL string
}{
  {"cnn", "https://cnn.com"},
  {"nytimes", "https://nytimes.com"},
  {"reuters", "https://reuters.com"},
  {"ap", "https://apnews.com"},
  {"bbc", "https://bbc.com"},
}

func makeTestJobs(count int) []*core.CrawlJob {
  jobs := make([]*core.CrawlJob, count)

  for i := 0; i < count; i++ {
    src := testSources[i%len(testSources)]

    jobs[i] = &core.CrawlJob{
      ID:          fmt.Sprintf("job-%03d", i+1),
      CrawlerName: src.name,
      Priority:    core.PriorityNormal,
      ScheduledAt: time.Now(),
      Timeout:     30 * time.Second,
      MaxRetries:  3,
      Target: core.Target{
        URL:  fmt.Sprintf("%s/article/%d", src.baseURL, i+1),
        Type: core.TargetTypeArticle,
      },
    }
  }

  return jobs
}

func marshalToMessages(log *logger.Logger, jobs []*core.CrawlJob) []*queue.Message {
  msgs := make([]*queue.Message, 0, len(jobs))

  for _, job := range jobs {
    data, err := job.Marshal()
    if err != nil {
      log.WithError(err).Errorf("failed to marshal job %s", job.ID)
      continue
    }

    msgs = append(msgs, &queue.Message{
      Topic: queue.TopicCrawlNormal,
      Key:   []byte(job.ID),
      Value: data,
      Time:  time.Now(),
      Headers: map[string]string{
        "source":  job.CrawlerName,
        "country": "US",
      },
    })
  }

  return msgs
}

// =========================================================
// 결과 출력
// =========================================================

func printPipelineResults(log *logger.Logger, published []queue.Message) {
  separator := strings.Repeat("─", 60)

  fmt.Println()
  fmt.Println(separator)
  fmt.Println("  Pipeline Results")
  fmt.Println(separator)

  var rawCount, dlqCount, requeueCount int

  for _, msg := range published {
    switch msg.Topic {
    case queue.TopicRawUS:
      rawCount++

      var raw core.RawContent
      if err := json.Unmarshal(msg.Value, &raw); err != nil {
        continue
      }

      log.WithFields(map[string]interface{}{
        "topic":   msg.Topic,
        "url":     raw.URL,
        "source":  raw.SourceInfo.Name,
        "country": raw.SourceInfo.Country,
        "size":    fmt.Sprintf("%d bytes", len(msg.Value)),
      }).Info("raw content")

    case queue.TopicDLQ:
      dlqCount++

      log.WithFields(map[string]interface{}{
        "topic":  msg.Topic,
        "key":    string(msg.Key),
        "error":  msg.Headers["error"],
      }).Warn("dead letter")

    default:
      // crawl.* 재큐잉 메시지
      if strings.HasPrefix(msg.Topic, "issuetracker.crawl.") {
        requeueCount++
      }
    }
  }

  fmt.Println(separator)
  log.WithFields(map[string]interface{}{
    "raw_published": rawCount,
    "dlq_sent":      dlqCount,
    "requeued":      requeueCount,
    "total":         len(published),
  }).Info("summary")
  fmt.Println(separator)
}

// =========================================================
// main
// =========================================================

func main() {
  logCfg := logger.DefaultConfig()
  logCfg.Pretty = true
  log := logger.New(logCfg)

  const jobCount = 7
  const workerCount = 3

  fmt.Println()
  log.WithFields(map[string]interface{}{
    "job_count":    jobCount,
    "worker_count": workerCount,
    "topic_in":     queue.TopicCrawlNormal,
    "topic_out":    queue.TopicRawUS,
  }).Info("=== Kafka Pipeline Example Start ===")

  ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
  defer cancel()
  ctx = log.ToContext(ctx)

  // 테스트 job 생성 및 mock consumer에 적재
  jobs := makeTestJobs(jobCount)
  msgs := marshalToMessages(log, jobs)

  log.WithField("count", len(msgs)).Info("test jobs enqueued to mock consumer")

  // raw 토픽 publish 완료 카운트 추적
  var processed sync.WaitGroup
  processed.Add(len(jobs))

  producer := &mockProducer{
    onPublish: func(msg queue.Message) {
      // raw 토픽으로 publish될 때만 완료로 카운트
      if msg.Topic == queue.TopicRawUS {
        processed.Done()
      }
    },
  }

  consumer := newMockConsumer(msgs)
  handler := &testCrawlerHandler{log: log}

  pool := worker.NewKafkaConsumerPool(
    consumer,
    producer,
    handler,
    workerCount,
    worker.DefaultRawTopicFunc, // 국가 코드 기반 raw 토픽 결정
  )

  start := time.Now()
  pool.Start(ctx)

  // 모든 job 처리 완료 대기
  doneCh := make(chan struct{})
  go func() {
    processed.Wait()
    close(doneCh)
  }()

  select {
  case <-doneCh:
    elapsed := time.Since(start)
    log.WithField("elapsed_ms", elapsed.Milliseconds()).Info("all jobs processed")

  case <-ctx.Done():
    log.Warn("timeout reached before all jobs completed")
  }

  // graceful shutdown
  cancel()

  shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
  defer shutdownCancel()
  pool.Stop(shutdownCtx)

  printPipelineResults(log, producer.messages())
}
