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
	"issuetracker/internal/storage/service"
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

func (m *mockJobHandler) Handle(ctx context.Context, job *core.CrawlJob) ([]*core.Content, error) {
	args := m.Called(ctx, job)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*core.Content), args.Error(1)
}

type mockContentService struct{ mock.Mock }

func (m *mockContentService) Store(ctx context.Context, content *core.Content) (string, bool, error) {
	args := m.Called(ctx, content)
	return args.String(0), args.Bool(1), args.Error(2)
}

func (m *mockContentService) StoreBatch(ctx context.Context, contents []*core.Content) ([]service.StoreResult, error) {
	args := m.Called(ctx, contents)
	return args.Get(0).([]service.StoreResult), args.Error(1)
}

func (m *mockContentService) GetByID(ctx context.Context, id string) (*core.Content, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*core.Content), args.Error(1)
}

func (m *mockContentService) ListByCountry(ctx context.Context, country string, filter storage.ContentFilter) ([]*core.Content, error) {
	args := m.Called(ctx, country, filter)
	return args.Get(0).([]*core.Content), args.Error(1)
}

func (m *mockContentService) Search(ctx context.Context, filter storage.ContentFilter) ([]*core.Content, error) {
	args := m.Called(ctx, filter)
	return args.Get(0).([]*core.Content), args.Error(1)
}

func (m *mockContentService) CountByCountry(ctx context.Context, days int) (map[string]int64, error) {
	args := m.Called(ctx, days)
	return args.Get(0).(map[string]int64), args.Error(1)
}

func (m *mockContentService) Delete(ctx context.Context, id string) error {
	args := m.Called(ctx, id)
	return args.Error(0)
}

// ─────────────────────────────────────────────────────────────────────────────
// 헬퍼
// ─────────────────────────────────────────────────────────────────────────────

func newTestJob() *core.CrawlJob {
	return &core.CrawlJob{
		ID:          "job-001",
		CrawlerName: "test-crawler",
		Target:      core.Target{URL: "https://example.com/article", Type: core.TargetTypeArticle},
		Priority:    core.PriorityNormal,
		MaxRetries:  3,
	}
}

func newTestContent() *core.Content {
	return &core.Content{
		ID:           "cnt-abc123",
		SourceID:     "test-source",
		SourceType:   core.SourceTypeNews,
		Country:      "US",
		Language:     "en",
		Title:        "Test Article",
		Body:         "This is test body content for the article.",
		URL:          "https://example.com/article",
		CanonicalURL: "https://example.com/article",
		PublishedAt:  time.Now(),
		WordCount:    8,
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
func runPool(t *testing.T, consumer *mockConsumer, pool *worker.KafkaConsumerPool, msg *queue.Message) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())

	consumer.On("FetchMessage", mock.Anything).Return(msg, nil).Once()
	consumer.On("FetchMessage", mock.Anything).
		Run(func(_ mock.Arguments) { cancel() }).
		Return(nil, context.Canceled)
	consumer.On("Close").Return(nil)

	pool.Start(ctx)
	<-ctx.Done()

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopCancel()
	_ = pool.Stop(stopCtx)
}

// ─────────────────────────────────────────────────────────────────────────────
// 테스트
// ─────────────────────────────────────────────────────────────────────────────

// TestKafkaConsumerPool_ProcessJob_PublishesToNormalized는
// 정상 흐름에서 ContentRef를 ProcessingMessage로 래핑하여 TopicNormalized에 발행하는지 검증합니다.
func TestKafkaConsumerPool_ProcessJob_PublishesToNormalized(t *testing.T) {
	consumer := new(mockConsumer)
	producer := new(mockProducer)
	handler := new(mockJobHandler)
	contentSvc := new(mockContentService)

	pool := worker.NewKafkaConsumerPool(consumer, producer, handler, contentSvc, 1)

	job := newTestJob()
	content := newTestContent()
	msg := marshaledJobMsg(t, job)

	handler.On("Handle", mock.Anything, job).Return([]*core.Content{content}, nil)
	contentSvc.On("Store", mock.Anything, content).Return(content.ID, false, nil)

	producer.On("Publish", mock.Anything, mock.MatchedBy(func(m queue.Message) bool {
		if m.Topic != queue.TopicNormalized {
			return false
		}
		var pm core.ProcessingMessage
		if err := json.Unmarshal(m.Value, &pm); err != nil {
			return false
		}
		return pm.Stage == "normalized" && pm.Country == content.Country
	})).Return(nil)

	consumer.On("CommitMessages", mock.Anything, mock.Anything).Return(nil)

	runPool(t, consumer, pool, msg)

	handler.AssertExpectations(t)
	contentSvc.AssertExpectations(t)
	producer.AssertExpectations(t)
}

// TestKafkaConsumerPool_ProcessJob_MultipleContents_PublishesAll는
// handler가 여러 Content(RSS 등)를 반환하면 모두 저장하고 발행하는지 검증합니다.
func TestKafkaConsumerPool_ProcessJob_MultipleContents_PublishesAll(t *testing.T) {
	consumer := new(mockConsumer)
	producer := new(mockProducer)
	handler := new(mockJobHandler)
	contentSvc := new(mockContentService)

	pool := worker.NewKafkaConsumerPool(consumer, producer, handler, contentSvc, 1)

	job := newTestJob()
	c1 := newTestContent()
	c1.ID = "cnt-001"
	c1.URL = "https://example.com/article/1"
	c2 := newTestContent()
	c2.ID = "cnt-002"
	c2.URL = "https://example.com/article/2"
	msg := marshaledJobMsg(t, job)

	handler.On("Handle", mock.Anything, job).Return([]*core.Content{c1, c2}, nil)
	contentSvc.On("Store", mock.Anything, c1).Return(c1.ID, false, nil)
	contentSvc.On("Store", mock.Anything, c2).Return(c2.ID, false, nil)

	publishCount := 0
	producer.On("Publish", mock.Anything, mock.MatchedBy(func(m queue.Message) bool {
		return m.Topic == queue.TopicNormalized
	})).Run(func(_ mock.Arguments) { publishCount++ }).Return(nil)

	consumer.On("CommitMessages", mock.Anything, mock.Anything).Return(nil)

	runPool(t, consumer, pool, msg)

	assert.Equal(t, 2, publishCount, "두 Content 모두 발행되어야 합니다")
	handler.AssertExpectations(t)
	contentSvc.AssertExpectations(t)
}

// TestKafkaConsumerPool_ProcessJob_EmptyContents_CommitsOnly는
// handler가 nil을 반환할 때 Store/Publish 없이 commit만 수행하는지 검증합니다.
func TestKafkaConsumerPool_ProcessJob_EmptyContents_CommitsOnly(t *testing.T) {
	consumer := new(mockConsumer)
	producer := new(mockProducer)
	handler := new(mockJobHandler)
	contentSvc := new(mockContentService)

	pool := worker.NewKafkaConsumerPool(consumer, producer, handler, contentSvc, 1)

	job := newTestJob()
	msg := marshaledJobMsg(t, job)

	handler.On("Handle", mock.Anything, job).Return(nil, nil)
	consumer.On("CommitMessages", mock.Anything, mock.Anything).Return(nil)

	runPool(t, consumer, pool, msg)

	producer.AssertNotCalled(t, "Publish", mock.Anything, mock.Anything)
	contentSvc.AssertNotCalled(t, "Store", mock.Anything, mock.Anything)
	consumer.AssertCalled(t, "CommitMessages", mock.Anything, mock.Anything)
}

// TestKafkaConsumerPool_ProcessJob_HandlerError_SendsToDLQ는
// handler 에러 발생 시 MaxRetries 초과 후 DLQ로 전송하고 원본 메시지를 커밋하는지 검증합니다.
func TestKafkaConsumerPool_ProcessJob_HandlerError_SendsToDLQ(t *testing.T) {
	consumer := new(mockConsumer)
	producer := new(mockProducer)
	handler := new(mockJobHandler)
	contentSvc := new(mockContentService)

	pool := worker.NewKafkaConsumerPool(consumer, producer, handler, contentSvc, 1)

	job := newTestJob()
	job.RetryCount = job.MaxRetries // 이미 최대 재시도 도달
	msg := marshaledJobMsg(t, job)

	handler.On("Handle", mock.Anything, job).Return(nil, errors.New("fetch failed"))

	producer.On("Publish", mock.Anything, mock.MatchedBy(func(m queue.Message) bool {
		return m.Topic == queue.TopicDLQ
	})).Return(nil)

	// DLQ 전송 후 원본 메시지가 커밋되어야 합니다.
	consumer.On("CommitMessages", mock.Anything, mock.Anything).Return(nil)

	runPool(t, consumer, pool, msg)

	producer.AssertExpectations(t)
	consumer.AssertCalled(t, "CommitMessages", mock.Anything, mock.Anything)
	contentSvc.AssertNotCalled(t, "Store", mock.Anything, mock.Anything)
}

// TestKafkaConsumerPool_ProcessJob_HandlerError_RequeuesAndCommits는
// handler 에러 발생 시 MaxRetries 미달이면 재큐잉하고 원본 메시지를 커밋하는지 검증합니다.
func TestKafkaConsumerPool_ProcessJob_HandlerError_RequeuesAndCommits(t *testing.T) {
	consumer := new(mockConsumer)
	producer := new(mockProducer)
	handler := new(mockJobHandler)
	contentSvc := new(mockContentService)

	pool := worker.NewKafkaConsumerPool(consumer, producer, handler, contentSvc, 1)

	job := newTestJob()
	job.RetryCount = 1 // MaxRetries(3) 미달
	msg := marshaledJobMsg(t, job)

	handler.On("Handle", mock.Anything, job).Return(nil, errors.New("temporary error"))

	// crawl 토픽으로 재발행되어야 합니다.
	producer.On("Publish", mock.Anything, mock.MatchedBy(func(m queue.Message) bool {
		return m.Topic == queue.TopicCrawlNormal
	})).Return(nil)

	// 재발행 후 원본 메시지가 커밋되어야 합니다.
	consumer.On("CommitMessages", mock.Anything, mock.Anything).Return(nil)

	runPool(t, consumer, pool, msg)

	producer.AssertExpectations(t)
	consumer.AssertCalled(t, "CommitMessages", mock.Anything, mock.Anything)
	contentSvc.AssertNotCalled(t, "Store", mock.Anything, mock.Anything)
}

// TestKafkaConsumerPool_ProcessJob_CircuitOpen_SendsToDLQ는
// circuit breaker가 open 상태일 때 handler를 호출하지 않고 DLQ로 전송하는지 검증합니다.
func TestKafkaConsumerPool_ProcessJob_CircuitOpen_SendsToDLQ(t *testing.T) {
	consumer := new(mockConsumer)
	producer := new(mockProducer)
	handler := new(mockJobHandler)
	contentSvc := new(mockContentService)

	// MaxFailures=1로 설정하여 즉시 circuit open 유도
	cbRegistry := worker.NewCircuitBreakerRegistry(worker.CircuitBreakerConfig{
		MaxFailures: 1,
		OpenTimeout: time.Minute,
	})
	// 첫 번째 실패로 circuit을 미리 open 상태로 만듭니다.
	cbRegistry.Get("test-crawler").RecordFailure()

	pool := worker.NewKafkaConsumerPoolWithCB(consumer, producer, handler, contentSvc, 1, cbRegistry)

	job := newTestJob()
	msg := marshaledJobMsg(t, job)

	// circuit open이므로 handler는 호출되지 않아야 합니다.
	producer.On("Publish", mock.Anything, mock.MatchedBy(func(m queue.Message) bool {
		return m.Topic == queue.TopicDLQ
	})).Return(nil)
	consumer.On("CommitMessages", mock.Anything, mock.Anything).Return(nil)

	runPool(t, consumer, pool, msg)

	handler.AssertNotCalled(t, "Handle", mock.Anything, mock.Anything)
	producer.AssertExpectations(t)
	consumer.AssertCalled(t, "CommitMessages", mock.Anything, mock.Anything)
}

// TestKafkaConsumerPool_ProcessJob_DLQPublishFails_SkipsCommit는
// DLQ 발행 실패 시 원본 offset을 commit하지 않아 메시지 유실을 방지하는지 검증합니다.
func TestKafkaConsumerPool_ProcessJob_DLQPublishFails_SkipsCommit(t *testing.T) {
	consumer := new(mockConsumer)
	producer := new(mockProducer)
	handler := new(mockJobHandler)
	contentSvc := new(mockContentService)

	pool := worker.NewKafkaConsumerPool(consumer, producer, handler, contentSvc, 1)

	job := newTestJob()
	job.RetryCount = job.MaxRetries // DLQ 경로
	msg := marshaledJobMsg(t, job)

	handler.On("Handle", mock.Anything, job).Return(nil, errors.New("fetch failed"))

	// DLQ 발행 실패를 시뮬레이션합니다.
	producer.On("Publish", mock.Anything, mock.MatchedBy(func(m queue.Message) bool {
		return m.Topic == queue.TopicDLQ
	})).Return(errors.New("kafka unavailable"))

	runPool(t, consumer, pool, msg)

	// 발행 실패 시 commit을 호출하지 않아야 합니다.
	consumer.AssertNotCalled(t, "CommitMessages", mock.Anything, mock.Anything)
	contentSvc.AssertNotCalled(t, "Store", mock.Anything, mock.Anything)
}

// TestKafkaConsumerPool_ProcessJob_RequeuePublishFails_SkipsCommit는
// 재큐잉 발행 실패 시 원본 offset을 commit하지 않아 메시지 유실을 방지하는지 검증합니다.
func TestKafkaConsumerPool_ProcessJob_RequeuePublishFails_SkipsCommit(t *testing.T) {
	consumer := new(mockConsumer)
	producer := new(mockProducer)
	handler := new(mockJobHandler)
	contentSvc := new(mockContentService)

	pool := worker.NewKafkaConsumerPool(consumer, producer, handler, contentSvc, 1)

	job := newTestJob()
	job.RetryCount = 1 // MaxRetries(3) 미달 → requeue 경로
	msg := marshaledJobMsg(t, job)

	handler.On("Handle", mock.Anything, job).Return(nil, errors.New("temporary error"))

	// requeue 발행 실패를 시뮬레이션합니다.
	producer.On("Publish", mock.Anything, mock.MatchedBy(func(m queue.Message) bool {
		return m.Topic == queue.TopicCrawlNormal
	})).Return(errors.New("kafka unavailable"))

	runPool(t, consumer, pool, msg)

	// 발행 실패 시 commit을 호출하지 않아야 합니다.
	consumer.AssertNotCalled(t, "CommitMessages", mock.Anything, mock.Anything)
	contentSvc.AssertNotCalled(t, "Store", mock.Anything, mock.Anything)
}

// TestKafkaConsumerPool_ProcessJob_NormalizedMessageHasContentRef는
// 발행된 ProcessingMessage의 Data 필드에 ContentRef가 담기는지 검증합니다.
func TestKafkaConsumerPool_ProcessJob_NormalizedMessageHasContentRef(t *testing.T) {
	consumer := new(mockConsumer)
	producer := new(mockProducer)
	handler := new(mockJobHandler)
	contentSvc := new(mockContentService)

	pool := worker.NewKafkaConsumerPool(consumer, producer, handler, contentSvc, 1)

	job := newTestJob()
	content := newTestContent()
	msg := marshaledJobMsg(t, job)

	handler.On("Handle", mock.Anything, job).Return([]*core.Content{content}, nil)
	contentSvc.On("Store", mock.Anything, content).Return(content.ID, false, nil)

	var capturedMsg queue.Message
	producer.On("Publish", mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			capturedMsg = args.Get(1).(queue.Message)
		}).
		Return(nil)
	consumer.On("CommitMessages", mock.Anything, mock.Anything).Return(nil)

	runPool(t, consumer, pool, msg)

	var pm core.ProcessingMessage
	assert.NoError(t, json.Unmarshal(capturedMsg.Value, &pm))
	assert.Equal(t, "normalized", pm.Stage)

	var ref core.ContentRef
	assert.NoError(t, json.Unmarshal(pm.Data, &ref))
	assert.Equal(t, content.ID, ref.ID)
	assert.Equal(t, content.URL, ref.URL)
	assert.Equal(t, content.Country, ref.Country)
}
