package worker_test

import (
  "context"
  "encoding/json"
  "errors"
  "testing"
  "time"

  "github.com/stretchr/testify/assert"
  "github.com/stretchr/testify/mock"

  "issuetracker/internal/crawler/core"
  "issuetracker/internal/crawler/worker"
  "issuetracker/internal/storage"
  "issuetracker/pkg/queue"
)

// ─────────────────────────────────────────────────────────────────────────────
// Mock 구현체
// ─────────────────────────────────────────────────────────────────────────────

type mockConsumer struct{ mock.Mock }

func (m *mockConsumer) FetchMessage(ctx context.Context) (*queue.Message, error) {
  args := m.Called(ctx)
  if args.Get(0) == nil {
    return nil, args.Error(1)
  }
  return args.Get(0).(*queue.Message), args.Error(1)
}

func (m *mockConsumer) CommitMessages(ctx context.Context, msgs ...*queue.Message) error {
  args := m.Called(ctx, msgs)
  return args.Error(0)
}

func (m *mockConsumer) Close() error {
  args := m.Called()
  return args.Error(0)
}

type mockProducer struct{ mock.Mock }

func (m *mockProducer) Publish(ctx context.Context, msg queue.Message) error {
  args := m.Called(ctx, msg)
  return args.Error(0)
}

func (m *mockProducer) PublishBatch(ctx context.Context, msgs []queue.Message) error {
  args := m.Called(ctx, msgs)
  return args.Error(0)
}

func (m *mockProducer) Close() error {
  args := m.Called()
  return args.Error(0)
}

type mockJobHandler struct{ mock.Mock }

func (m *mockJobHandler) Handle(ctx context.Context, job *core.CrawlJob) (*core.RawContent, error) {
  args := m.Called(ctx, job)
  if args.Get(0) == nil {
    return nil, args.Error(1)
  }
  return args.Get(0).(*core.RawContent), args.Error(1)
}

type mockRawContentService struct{ mock.Mock }

func (m *mockRawContentService) Store(ctx context.Context, raw *core.RawContent) (string, bool, error) {
  args := m.Called(ctx, raw)
  return args.String(0), args.Bool(1), args.Error(2)
}

func (m *mockRawContentService) GetByID(ctx context.Context, id string) (*core.RawContent, error) {
  args := m.Called(ctx, id)
  if args.Get(0) == nil {
    return nil, args.Error(1)
  }
  return args.Get(0).(*core.RawContent), args.Error(1)
}

func (m *mockRawContentService) List(ctx context.Context, filter storage.RawContentFilter) ([]*core.RawContent, error) {
  args := m.Called(ctx, filter)
  if args.Get(0) == nil {
    return nil, args.Error(1)
  }
  return args.Get(0).([]*core.RawContent), args.Error(1)
}

func (m *mockRawContentService) PurgeOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
  args := m.Called(ctx, cutoff)
  return args.Get(0).(int64), args.Error(1)
}

// ─────────────────────────────────────────────────────────────────────────────
// 헬퍼
// ─────────────────────────────────────────────────────────────────────────────

func newTestJob() *core.CrawlJob {
  return &core.CrawlJob{
    ID:          "job-001",
    CrawlerName: "test-crawler",
    Target:      core.Target{URL: "https://example.com/article"},
    Priority:    core.PriorityNormal,
    MaxRetries:  3,
  }
}

func newTestRawContent() *core.RawContent {
  return &core.RawContent{
    ID:        "raw-001",
    URL:       "https://example.com/article",
    HTML:      "<html><body>Test</body></html>",
    FetchedAt: time.Now(),
    SourceInfo: core.SourceInfo{
      Country:  "US",
      Type:     core.SourceTypeNews,
      Name:     "test-source",
      Language: "en",
    },
  }
}

func marshaledJobMsg(t *testing.T, job *core.CrawlJob) *queue.Message {
  t.Helper()
  data, err := job.Marshal()
  assert.NoError(t, err)
  return &queue.Message{
    Topic: queue.TopicCrawlNormal,
    Key:   []byte(job.ID),
    Value: data,
  }
}

// runPool은 pool을 구동하고 단일 메시지를 처리한 뒤 종료합니다.
// 두 번째 FetchMessage 호출 시 ctx를 cancel하여 pollMessages가 깨끗이 종료되도록 합니다.
func runPool(t *testing.T, consumer *mockConsumer, pool *worker.KafkaConsumerPool, msg *queue.Message) {
  t.Helper()

  ctx, cancel := context.WithCancel(context.Background())

  consumer.On("FetchMessage", mock.Anything).Return(msg, nil).Once()
  // 두 번째 호출에서 ctx를 cancel → pollMessages가 ctx.Err() != nil로 정상 종료
  consumer.On("FetchMessage", mock.Anything).
    Run(func(_ mock.Arguments) { cancel() }).
    Return(nil, context.Canceled)
  consumer.On("Close").Return(nil)

  pool.Start(ctx)

  // pollMessages가 ctx cancel을 감지하고 종료할 때까지 대기.
  // cancel()은 두 번째 FetchMessage 호출의 Run 훅에서 실행되므로,
  // 이 시점에는 첫 번째 메시지가 이미 p.jobs 채널에 버퍼링된 상태입니다.
  // pool.Stop이 p.jobs를 닫기 전에 pollMessages가 반드시 종료되어야 패닉을 방지합니다.
  <-ctx.Done()

  stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
  defer stopCancel()
  _ = pool.Stop(stopCtx)
}

// ─────────────────────────────────────────────────────────────────────────────
// 테스트
// ─────────────────────────────────────────────────────────────────────────────

// TestKafkaConsumerPool_ProcessJob_StoresInPostgresAndPublishesRef는
// 정상 흐름에서 RawContent를 Postgres에 저장하고 ID만 담긴 RawContentRef를 Kafka에 발행하는지 검증합니다.
func TestKafkaConsumerPool_ProcessJob_StoresInPostgresAndPublishesRef(t *testing.T) {
  consumer := new(mockConsumer)
  producer := new(mockProducer)
  handler := new(mockJobHandler)
  rawSvc := new(mockRawContentService)

  pool := worker.NewKafkaConsumerPool(consumer, producer, handler, rawSvc, 1, nil)

  job := newTestJob()
  raw := newTestRawContent()
  msg := marshaledJobMsg(t, job)

  handler.On("Handle", mock.Anything, job).Return(raw, nil)
  rawSvc.On("Store", mock.Anything, raw).Return("raw-001", false, nil)

  // RawContentRef가 올바른 토픽과 ID로 발행되는지 확인
  producer.On("Publish", mock.Anything, mock.MatchedBy(func(m queue.Message) bool {
    if m.Topic != queue.TopicRawUS {
      return false
    }
    var ref core.RawContentRef
    if err := json.Unmarshal(m.Value, &ref); err != nil {
      return false
    }
    return ref.ID == "raw-001" && ref.URL == raw.URL
  })).Return(nil)

  consumer.On("CommitMessages", mock.Anything, mock.Anything).Return(nil)

  runPool(t, consumer, pool, msg)

  handler.AssertExpectations(t)
  rawSvc.AssertExpectations(t)
  producer.AssertExpectations(t)
}

// TestKafkaConsumerPool_ProcessJob_DuplicateURL_PublishesExistingID는
// 중복 URL에서 Store가 기존 ID를 반환해도 RawContentRef를 정상 발행하는지 검증합니다.
func TestKafkaConsumerPool_ProcessJob_DuplicateURL_PublishesExistingID(t *testing.T) {
  consumer := new(mockConsumer)
  producer := new(mockProducer)
  handler := new(mockJobHandler)
  rawSvc := new(mockRawContentService)

  pool := worker.NewKafkaConsumerPool(consumer, producer, handler, rawSvc, 1, nil)

  job := newTestJob()
  raw := newTestRawContent()
  msg := marshaledJobMsg(t, job)

  handler.On("Handle", mock.Anything, job).Return(raw, nil)
  // 중복: Store가 기존 ID와 isDuplicate=true를 반환
  rawSvc.On("Store", mock.Anything, raw).Return("existing-raw-999", true, nil)

  // 기존 ID가 ref에 포함되어야 합니다
  producer.On("Publish", mock.Anything, mock.MatchedBy(func(m queue.Message) bool {
    var ref core.RawContentRef
    if err := json.Unmarshal(m.Value, &ref); err != nil {
      return false
    }
    return ref.ID == "existing-raw-999"
  })).Return(nil)

  consumer.On("CommitMessages", mock.Anything, mock.Anything).Return(nil)

  runPool(t, consumer, pool, msg)

  rawSvc.AssertExpectations(t)
  producer.AssertExpectations(t)
}

// TestKafkaConsumerPool_ProcessJob_StoreError_DoesNotPublish는
// Postgres 저장 실패 시 Kafka 발행 없이 에러를 반환하는지 검증합니다.
func TestKafkaConsumerPool_ProcessJob_StoreError_DoesNotPublish(t *testing.T) {
  consumer := new(mockConsumer)
  producer := new(mockProducer)
  handler := new(mockJobHandler)
  rawSvc := new(mockRawContentService)

  pool := worker.NewKafkaConsumerPool(consumer, producer, handler, rawSvc, 1, nil)

  job := newTestJob()
  raw := newTestRawContent()
  msg := marshaledJobMsg(t, job)

  handler.On("Handle", mock.Anything, job).Return(raw, nil)
  rawSvc.On("Store", mock.Anything, raw).Return("", false, errors.New("db connection failed"))

  runPool(t, consumer, pool, msg)

  rawSvc.AssertExpectations(t)
  // Store 실패 시 Publish가 호출되면 안 됩니다
  producer.AssertNotCalled(t, "Publish", mock.Anything, mock.Anything)
}

// TestKafkaConsumerPool_ProcessJob_NilRaw_CommitsWithoutStore는
// handler가 nil RawContent를 반환할 때 Store와 Publish를 호출하지 않고
// 바로 commit하는지 검증합니다.
func TestKafkaConsumerPool_ProcessJob_NilRaw_CommitsWithoutStore(t *testing.T) {
  consumer := new(mockConsumer)
  producer := new(mockProducer)
  handler := new(mockJobHandler)
  rawSvc := new(mockRawContentService)

  pool := worker.NewKafkaConsumerPool(consumer, producer, handler, rawSvc, 1, nil)

  job := newTestJob()
  msg := marshaledJobMsg(t, job)

  // handler가 nil 반환 (처리할 내용 없음)
  handler.On("Handle", mock.Anything, job).Return(nil, nil)
  consumer.On("CommitMessages", mock.Anything, mock.Anything).Return(nil)

  runPool(t, consumer, pool, msg)

  rawSvc.AssertNotCalled(t, "Store", mock.Anything, mock.Anything)
  producer.AssertNotCalled(t, "Publish", mock.Anything, mock.Anything)
  consumer.AssertCalled(t, "CommitMessages", mock.Anything, mock.Anything)
}

// TestKafkaConsumerPool_ProcessJob_PublishedRefHasNoHTML는
// Kafka에 발행되는 메시지에 HTML이 포함되지 않음을 검증합니다 (대용량 페이지 오프로딩).
func TestKafkaConsumerPool_ProcessJob_PublishedRefHasNoHTML(t *testing.T) {
  consumer := new(mockConsumer)
  producer := new(mockProducer)
  handler := new(mockJobHandler)
  rawSvc := new(mockRawContentService)

  pool := worker.NewKafkaConsumerPool(consumer, producer, handler, rawSvc, 1, nil)

  job := newTestJob()
  raw := newTestRawContent()
  // 대용량 HTML 시뮬레이션 (~5.6MB, CNN live blog 수준)
  raw.HTML = string(make([]byte, 5*1024*1024))

  msg := marshaledJobMsg(t, job)

  handler.On("Handle", mock.Anything, job).Return(raw, nil)
  rawSvc.On("Store", mock.Anything, raw).Return("raw-001", false, nil)

  var capturedMsg queue.Message
  producer.On("Publish", mock.Anything, mock.Anything).
    Run(func(args mock.Arguments) {
      capturedMsg = args.Get(1).(queue.Message)
    }).
    Return(nil)
  consumer.On("CommitMessages", mock.Anything, mock.Anything).Return(nil)

  runPool(t, consumer, pool, msg)

  producer.AssertExpectations(t)

  // 발행된 메시지가 1MB 이하인지 확인 (HTML이 없어야 함)
  assert.Less(t, len(capturedMsg.Value), 1024*1024, "Kafka 메시지가 1MB 이하여야 합니다")

  // RawContentRef로 역직렬화 가능하고 ID가 올바른지 확인
  var ref core.RawContentRef
  assert.NoError(t, json.Unmarshal(capturedMsg.Value, &ref))
  assert.Equal(t, "raw-001", ref.ID)
}

// TestKafkaConsumerPool_ProcessJob_KRSource_PublishesToKRTopic는
// 한국 소스의 RawContent를 kr 토픽에 발행하는지 검증합니다.
func TestKafkaConsumerPool_ProcessJob_KRSource_PublishesToKRTopic(t *testing.T) {
  consumer := new(mockConsumer)
  producer := new(mockProducer)
  handler := new(mockJobHandler)
  rawSvc := new(mockRawContentService)

  pool := worker.NewKafkaConsumerPool(consumer, producer, handler, rawSvc, 1, nil)

  job := newTestJob()
  raw := newTestRawContent()
  raw.SourceInfo.Country = "KR"

  msg := marshaledJobMsg(t, job)

  handler.On("Handle", mock.Anything, job).Return(raw, nil)
  rawSvc.On("Store", mock.Anything, raw).Return("raw-kr-001", false, nil)

  producer.On("Publish", mock.Anything, mock.MatchedBy(func(m queue.Message) bool {
    return m.Topic == queue.TopicRawKR
  })).Return(nil)

  consumer.On("CommitMessages", mock.Anything, mock.Anything).Return(nil)

  runPool(t, consumer, pool, msg)

  producer.AssertExpectations(t)
}
