package validate_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"issuetracker/internal/crawler/core"
	"issuetracker/internal/processor/validate"
	"issuetracker/internal/storage"
	"issuetracker/internal/storage/service"
	"issuetracker/pkg/config"
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

// makeProcessingMessage는 ContentRef를 Data로 갖는 ProcessingMessage를 만들어 반환합니다.
func makeProcessingMessage(content *core.Content, retryCount int) *queue.Message {
	ref := core.ContentRef{
		ID:      content.ID,
		URL:     content.URL,
		Country: content.Country,
		SourceInfo: core.SourceInfo{
			Country:  content.Country,
			Type:     content.SourceType,
			Name:     content.SourceID,
			Language: content.Language,
		},
	}
	data, _ := json.Marshal(ref)
	pm := core.ProcessingMessage{
		ID:         "pm-001",
		Timestamp:  time.Now(),
		Country:    content.Country,
		Stage:      "normalized",
		Data:       data,
		RetryCount: retryCount,
	}
	value, _ := json.Marshal(pm)
	return &queue.Message{
		Topic: queue.TopicNormalized,
		Key:   []byte(content.ID),
		Value: value,
	}
}

func newWorker(consumer queue.Consumer, producer queue.Producer, contentSvc service.ContentService) *validate.Worker {
	return validate.NewWorker(consumer, producer, contentSvc, 1, config.DefaultValidateConfig())
}

func runWorker(t *testing.T, consumer *mockConsumer, w *validate.Worker, msg *queue.Message) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())

	consumer.On("FetchMessage", mock.Anything).Return(msg, nil).Once()
	consumer.On("FetchMessage", mock.Anything).
		Run(func(_ mock.Arguments) { cancel() }).
		Return(nil, context.Canceled)
	consumer.On("Close").Return(nil)

	w.Start(ctx)
	<-ctx.Done()

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopCancel()
	_ = w.Stop(stopCtx)
}

// ─────────────────────────────────────────────────────────────────────────────
// 테스트
// ─────────────────────────────────────────────────────────────────────────────

func TestValidateWorker_ValidNewsContent_PublishesToValidatedTopic(t *testing.T) {
	consumer := new(mockConsumer)
	producer := new(mockProducer)
	contentSvc := new(mockContentService)

	w := newWorker(consumer, producer, contentSvc)

	content := newNewsContent()
	msg := makeProcessingMessage(content, 0)

	contentSvc.On("GetByID", mock.Anything, content.ID).Return(content, nil)
	producer.On("Publish", mock.Anything, mock.MatchedBy(func(m queue.Message) bool {
		return m.Topic == queue.TopicValidated
	})).Return(nil)
	consumer.On("CommitMessages", mock.Anything, mock.Anything).Return(nil)

	runWorker(t, consumer, w, msg)

	producer.AssertExpectations(t)
	contentSvc.AssertExpectations(t)
	contentSvc.AssertNotCalled(t, "Delete", mock.Anything, mock.Anything)
}

func TestValidateWorker_ValidCommunityContent_PublishesToValidatedTopic(t *testing.T) {
	consumer := new(mockConsumer)
	producer := new(mockProducer)
	contentSvc := new(mockContentService)

	w := newWorker(consumer, producer, contentSvc)

	content := newCommunityContent()
	msg := makeProcessingMessage(content, 0)

	contentSvc.On("GetByID", mock.Anything, content.ID).Return(content, nil)
	producer.On("Publish", mock.Anything, mock.MatchedBy(func(m queue.Message) bool {
		return m.Topic == queue.TopicValidated
	})).Return(nil)
	consumer.On("CommitMessages", mock.Anything, mock.Anything).Return(nil)

	runWorker(t, consumer, w, msg)

	producer.AssertExpectations(t)
	contentSvc.AssertExpectations(t)
	contentSvc.AssertNotCalled(t, "Delete", mock.Anything, mock.Anything)
}

func TestValidateWorker_InvalidContent_SendsToDLQ(t *testing.T) {
	consumer := new(mockConsumer)
	producer := new(mockProducer)
	contentSvc := new(mockContentService)

	w := newWorker(consumer, producer, contentSvc)

	content := newNewsContent()
	content.Title = "x"         // 너무 짧음
	content.Body = "short body" // 너무 짧음
	content.PublishedAt = time.Time{}
	msg := makeProcessingMessage(content, 3) // retry count >= maxRetries(3)

	contentSvc.On("GetByID", mock.Anything, content.ID).Return(content, nil)
	contentSvc.On("Delete", mock.Anything, content.ID).Return(nil)
	producer.On("Publish", mock.Anything, mock.MatchedBy(func(m queue.Message) bool {
		return m.Topic == queue.TopicDLQ
	})).Return(nil)
	consumer.On("CommitMessages", mock.Anything, mock.Anything).Return(nil)

	runWorker(t, consumer, w, msg)

	producer.AssertExpectations(t)
	contentSvc.AssertExpectations(t)
	producer.AssertNotCalled(t, "Publish", mock.Anything, mock.MatchedBy(func(m queue.Message) bool {
		return m.Topic == queue.TopicValidated
	}))
}

func TestValidateWorker_InvalidContent_RetriesBeforeDLQ(t *testing.T) {
	consumer := new(mockConsumer)
	producer := new(mockProducer)
	contentSvc := new(mockContentService)

	w := newWorker(consumer, producer, contentSvc)

	content := newNewsContent()
	content.Title = "x"
	content.Body = "short body"
	content.PublishedAt = time.Time{}
	msg := makeProcessingMessage(content, 0) // retry count = 0, 재큐잉해야 함

	contentSvc.On("GetByID", mock.Anything, content.ID).Return(content, nil)
	// 재큐잉: TopicNormalized로 다시 publish (Delete는 호출되지 않음)
	producer.On("Publish", mock.Anything, mock.MatchedBy(func(m queue.Message) bool {
		return m.Topic == queue.TopicNormalized
	})).Return(nil)
	consumer.On("CommitMessages", mock.Anything, mock.Anything).Return(nil)

	runWorker(t, consumer, w, msg)

	producer.AssertExpectations(t)
	contentSvc.AssertExpectations(t)
	contentSvc.AssertNotCalled(t, "Delete", mock.Anything, mock.Anything)
}

func TestValidateWorker_MalformedMessage_SendsToDLQ(t *testing.T) {
	consumer := new(mockConsumer)
	producer := new(mockProducer)
	contentSvc := new(mockContentService)

	w := newWorker(consumer, producer, contentSvc)

	// 잘못된 JSON 메시지 — pm 역직렬화 실패로 contentSvc가 호출되지 않음
	msg := &queue.Message{
		Topic: queue.TopicNormalized,
		Key:   []byte("bad"),
		Value: []byte("not-valid-json"),
	}

	producer.On("Publish", mock.Anything, mock.MatchedBy(func(m queue.Message) bool {
		return m.Topic == queue.TopicDLQ
	})).Return(nil)
	consumer.On("CommitMessages", mock.Anything, mock.Anything).Return(nil)

	runWorker(t, consumer, w, msg)

	producer.AssertExpectations(t)
	contentSvc.AssertNotCalled(t, "GetByID", mock.Anything, mock.Anything)
	contentSvc.AssertNotCalled(t, "Delete", mock.Anything, mock.Anything)
}

func TestValidateWorker_ValidatedMessageContainsContentRef(t *testing.T) {
	consumer := new(mockConsumer)
	producer := new(mockProducer)
	contentSvc := new(mockContentService)

	w := newWorker(consumer, producer, contentSvc)

	content := newNewsContent()
	msg := makeProcessingMessage(content, 0)

	contentSvc.On("GetByID", mock.Anything, content.ID).Return(content, nil)

	var capturedMsg queue.Message
	producer.On("Publish", mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			capturedMsg = args.Get(1).(queue.Message)
		}).
		Return(nil)
	consumer.On("CommitMessages", mock.Anything, mock.Anything).Return(nil)

	runWorker(t, consumer, w, msg)

	// 발행된 ProcessingMessage.Data에 ContentRef가 담겨 있어야 함
	var pm core.ProcessingMessage
	assert.NoError(t, json.Unmarshal(capturedMsg.Value, &pm))

	var ref core.ContentRef
	assert.NoError(t, json.Unmarshal(pm.Data, &ref))
	assert.Equal(t, content.ID, ref.ID)
	assert.Equal(t, content.URL, ref.URL)
	assert.Equal(t, content.Country, ref.Country)
}

func TestValidateWorker_NewValidator_DispatchesBySourceType(t *testing.T) {
	tests := []struct {
		name       string
		sourceType core.SourceType
	}{
		{"news", core.SourceTypeNews},
		{"community", core.SourceTypeCommunity},
		{"social defaults to news", core.SourceTypeSocial},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := validate.NewValidator(tt.sourceType, config.DefaultValidateConfig())
			assert.NotNil(t, v)
		})
	}
}

func TestValidateWorker_Stop_ReturnsConsumerError(t *testing.T) {
	consumer := new(mockConsumer)
	producer := new(mockProducer)
	contentSvc := new(mockContentService)

	w := newWorker(consumer, producer, contentSvc)

	consumer.On("Close").Return(errors.New("close error"))

	ctx, cancel := context.WithCancel(context.Background())

	consumer.On("FetchMessage", mock.Anything).
		Run(func(_ mock.Arguments) { cancel() }).
		Return(nil, context.Canceled)

	w.Start(ctx)
	<-ctx.Done()

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopCancel()

	err := w.Stop(stopCtx)
	assert.Error(t, err)
}

func TestValidateWorker_ValidatedMessage_HasCorrectStage(t *testing.T) {
	consumer := new(mockConsumer)
	producer := new(mockProducer)
	contentSvc := new(mockContentService)

	w := newWorker(consumer, producer, contentSvc)

	content := newNewsContent()
	msg := makeProcessingMessage(content, 0)

	contentSvc.On("GetByID", mock.Anything, content.ID).Return(content, nil)

	var capturedMsg queue.Message
	producer.On("Publish", mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			capturedMsg = args.Get(1).(queue.Message)
		}).
		Return(nil)
	consumer.On("CommitMessages", mock.Anything, mock.Anything).Return(nil)

	runWorker(t, consumer, w, msg)

	var pm core.ProcessingMessage
	assert.NoError(t, json.Unmarshal(capturedMsg.Value, &pm))
	assert.Equal(t, "validated", pm.Stage)
}

func TestValidateWorker_LargeBody_ValidatesCorrectly(t *testing.T) {
	consumer := new(mockConsumer)
	producer := new(mockProducer)
	contentSvc := new(mockContentService)

	w := newWorker(consumer, producer, contentSvc)

	content := newNewsContent()
	content.Body = strings.Repeat("This is a very long news article body. ", 200)
	content.WordCount = 1400
	msg := makeProcessingMessage(content, 0)

	contentSvc.On("GetByID", mock.Anything, content.ID).Return(content, nil)
	producer.On("Publish", mock.Anything, mock.MatchedBy(func(m queue.Message) bool {
		return m.Topic == queue.TopicValidated
	})).Return(nil)
	consumer.On("CommitMessages", mock.Anything, mock.Anything).Return(nil)

	runWorker(t, consumer, w, msg)

	producer.AssertExpectations(t)
	contentSvc.AssertExpectations(t)
}
