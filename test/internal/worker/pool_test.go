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
		ID:          "cnt-abc123",
		SourceID:    "test-source",
		SourceType:  core.SourceTypeNews,
		Country:     "US",
		Language:    "en",
		Title:       "Test Article",
		Body:        "This is test body content for the article.",
		URL:         "https://example.com/article",
		CanonicalURL: "https://example.com/article",
		PublishedAt: time.Now(),
		WordCount:   8,
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
	<-ctx.Done()

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopCancel()
	_ = pool.Stop(stopCtx)
}

// ─────────────────────────────────────────────────────────────────────────────
// 테스트
// ─────────────────────────────────────────────────────────────────────────────

// TestKafkaConsumerPool_ProcessJob_PublishesToNormalized는
// 정상 흐름에서 Content를 ProcessingMessage로 래핑하여 TopicNormalized에 발행하는지 검증합니다.
func TestKafkaConsumerPool_ProcessJob_PublishesToNormalized(t *testing.T) {
	consumer := new(mockConsumer)
	producer := new(mockProducer)
	handler := new(mockJobHandler)

	pool := worker.NewKafkaConsumerPool(consumer, producer, handler, 1)

	job := newTestJob()
	content := newTestContent()
	msg := marshaledJobMsg(t, job)

	handler.On("Handle", mock.Anything, job).Return([]*core.Content{content}, nil)

	// ProcessingMessage가 TopicNormalized에 발행되는지 확인
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
	producer.AssertExpectations(t)
}

// TestKafkaConsumerPool_ProcessJob_MultipleContents_PublishesAll는
// handler가 여러 Content(RSS 등)를 반환하면 모두 발행하는지 검증합니다.
func TestKafkaConsumerPool_ProcessJob_MultipleContents_PublishesAll(t *testing.T) {
	consumer := new(mockConsumer)
	producer := new(mockProducer)
	handler := new(mockJobHandler)

	pool := worker.NewKafkaConsumerPool(consumer, producer, handler, 1)

	job := newTestJob()
	c1 := newTestContent()
	c1.URL = "https://example.com/article/1"
	c2 := newTestContent()
	c2.URL = "https://example.com/article/2"
	msg := marshaledJobMsg(t, job)

	handler.On("Handle", mock.Anything, job).Return([]*core.Content{c1, c2}, nil)

	publishCount := 0
	producer.On("Publish", mock.Anything, mock.MatchedBy(func(m queue.Message) bool {
		return m.Topic == queue.TopicNormalized
	})).Run(func(_ mock.Arguments) { publishCount++ }).Return(nil)

	consumer.On("CommitMessages", mock.Anything, mock.Anything).Return(nil)

	runPool(t, consumer, pool, msg)

	assert.Equal(t, 2, publishCount, "두 Content 모두 발행되어야 합니다")
	handler.AssertExpectations(t)
}

// TestKafkaConsumerPool_ProcessJob_EmptyContents_CommitsOnly는
// handler가 nil을 반환할 때 Publish 없이 commit만 수행하는지 검증합니다.
func TestKafkaConsumerPool_ProcessJob_EmptyContents_CommitsOnly(t *testing.T) {
	consumer := new(mockConsumer)
	producer := new(mockProducer)
	handler := new(mockJobHandler)

	pool := worker.NewKafkaConsumerPool(consumer, producer, handler, 1)

	job := newTestJob()
	msg := marshaledJobMsg(t, job)

	// handler가 nil 반환 (처리할 내용 없음)
	handler.On("Handle", mock.Anything, job).Return(nil, nil)
	consumer.On("CommitMessages", mock.Anything, mock.Anything).Return(nil)

	runPool(t, consumer, pool, msg)

	producer.AssertNotCalled(t, "Publish", mock.Anything, mock.Anything)
	consumer.AssertCalled(t, "CommitMessages", mock.Anything, mock.Anything)
}

// TestKafkaConsumerPool_ProcessJob_HandlerError_SendsToDLQ는
// handler 에러 발생 시 MaxRetries 초과 후 DLQ로 전송하는지 검증합니다.
func TestKafkaConsumerPool_ProcessJob_HandlerError_SendsToDLQ(t *testing.T) {
	consumer := new(mockConsumer)
	producer := new(mockProducer)
	handler := new(mockJobHandler)

	pool := worker.NewKafkaConsumerPool(consumer, producer, handler, 1)

	job := newTestJob()
	job.RetryCount = job.MaxRetries // 이미 최대 재시도 도달
	msg := marshaledJobMsg(t, job)

	handler.On("Handle", mock.Anything, job).Return(nil, errors.New("fetch failed"))

	// DLQ 발행 확인
	producer.On("Publish", mock.Anything, mock.MatchedBy(func(m queue.Message) bool {
		return m.Topic == queue.TopicDLQ
	})).Return(nil)

	runPool(t, consumer, pool, msg)

	producer.AssertExpectations(t)
}

// TestKafkaConsumerPool_ProcessJob_NormalizedMessageHasContentBody는
// 발행된 ProcessingMessage의 Data 필드에 Content.Body가 포함되는지 검증합니다.
func TestKafkaConsumerPool_ProcessJob_NormalizedMessageHasContentBody(t *testing.T) {
	consumer := new(mockConsumer)
	producer := new(mockProducer)
	handler := new(mockJobHandler)

	pool := worker.NewKafkaConsumerPool(consumer, producer, handler, 1)

	job := newTestJob()
	content := newTestContent()
	msg := marshaledJobMsg(t, job)

	handler.On("Handle", mock.Anything, job).Return([]*core.Content{content}, nil)

	var capturedMsg queue.Message
	producer.On("Publish", mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			capturedMsg = args.Get(1).(queue.Message)
		}).
		Return(nil)
	consumer.On("CommitMessages", mock.Anything, mock.Anything).Return(nil)

	runPool(t, consumer, pool, msg)

	// ProcessingMessage → Content 역직렬화 검증
	var pm core.ProcessingMessage
	assert.NoError(t, json.Unmarshal(capturedMsg.Value, &pm))
	assert.Equal(t, "normalized", pm.Stage)

	var decoded core.Content
	assert.NoError(t, json.Unmarshal(pm.Data, &decoded))
	assert.Equal(t, content.Body, decoded.Body)
	assert.Equal(t, content.Country, decoded.Country)
}
