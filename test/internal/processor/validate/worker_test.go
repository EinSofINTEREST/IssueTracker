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

// ─────────────────────────────────────────────────────────────────────────────
// 헬퍼
// ─────────────────────────────────────────────────────────────────────────────

func makeProcessingMessage(content *core.Content, retryCount int) *queue.Message {
	data, _ := json.Marshal(content)
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

	w := validate.NewWorker(consumer, producer, 1, config.DefaultValidateConfig())

	content := newNewsContent()
	msg := makeProcessingMessage(content, 0)

	producer.On("Publish", mock.Anything, mock.MatchedBy(func(m queue.Message) bool {
		return m.Topic == queue.TopicValidated
	})).Return(nil)
	consumer.On("CommitMessages", mock.Anything, mock.Anything).Return(nil)

	runWorker(t, consumer, w, msg)

	producer.AssertExpectations(t)
}

func TestValidateWorker_ValidCommunityContent_PublishesToValidatedTopic(t *testing.T) {
	consumer := new(mockConsumer)
	producer := new(mockProducer)

	w := validate.NewWorker(consumer, producer, 1, config.DefaultValidateConfig())

	content := newCommunityContent()
	msg := makeProcessingMessage(content, 0)

	producer.On("Publish", mock.Anything, mock.MatchedBy(func(m queue.Message) bool {
		return m.Topic == queue.TopicValidated
	})).Return(nil)
	consumer.On("CommitMessages", mock.Anything, mock.Anything).Return(nil)

	runWorker(t, consumer, w, msg)

	producer.AssertExpectations(t)
}

func TestValidateWorker_InvalidContent_SendsToDLQ(t *testing.T) {
	consumer := new(mockConsumer)
	producer := new(mockProducer)

	w := validate.NewWorker(consumer, producer, 1, config.DefaultValidateConfig())

	content := newNewsContent()
	content.Title = "x"         // 너무 짧음
	content.Body = "short body" // 너무 짧음
	content.PublishedAt = time.Time{}
	msg := makeProcessingMessage(content, 3) // retry count >= maxRetries(3)

	producer.On("Publish", mock.Anything, mock.MatchedBy(func(m queue.Message) bool {
		return m.Topic == queue.TopicDLQ
	})).Return(nil)
	consumer.On("CommitMessages", mock.Anything, mock.Anything).Return(nil)

	runWorker(t, consumer, w, msg)

	producer.AssertExpectations(t)
	producer.AssertNotCalled(t, "Publish", mock.Anything, mock.MatchedBy(func(m queue.Message) bool {
		return m.Topic == queue.TopicValidated
	}))
}

func TestValidateWorker_InvalidContent_RetriesBeforeDLQ(t *testing.T) {
	consumer := new(mockConsumer)
	producer := new(mockProducer)

	w := validate.NewWorker(consumer, producer, 1, config.DefaultValidateConfig())

	content := newNewsContent()
	content.Title = "x"
	content.Body = "short body"
	content.PublishedAt = time.Time{}
	msg := makeProcessingMessage(content, 0) // retry count = 0, 재큐잉해야 함

	// 재큐잉: TopicNormalized로 다시 publish
	producer.On("Publish", mock.Anything, mock.MatchedBy(func(m queue.Message) bool {
		return m.Topic == queue.TopicNormalized
	})).Return(nil)
	consumer.On("CommitMessages", mock.Anything, mock.Anything).Return(nil)

	runWorker(t, consumer, w, msg)

	producer.AssertExpectations(t)
}

func TestValidateWorker_MalformedMessage_SendsToDLQ(t *testing.T) {
	consumer := new(mockConsumer)
	producer := new(mockProducer)

	w := validate.NewWorker(consumer, producer, 1, config.DefaultValidateConfig())

	// 잘못된 JSON 메시지
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
}

func TestValidateWorker_ValidatedMessageContainsReliability(t *testing.T) {
	consumer := new(mockConsumer)
	producer := new(mockProducer)

	w := validate.NewWorker(consumer, producer, 1, config.DefaultValidateConfig())

	content := newNewsContent()
	msg := makeProcessingMessage(content, 0)

	var capturedMsg queue.Message
	producer.On("Publish", mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			capturedMsg = args.Get(1).(queue.Message)
		}).
		Return(nil)
	consumer.On("CommitMessages", mock.Anything, mock.Anything).Return(nil)

	runWorker(t, consumer, w, msg)

	// 발행된 ProcessingMessage.Data에 Reliability가 설정되어 있어야 함
	var pm core.ProcessingMessage
	assert.NoError(t, json.Unmarshal(capturedMsg.Value, &pm))

	var validated core.Content
	assert.NoError(t, json.Unmarshal(pm.Data, &validated))
	assert.Greater(t, validated.Reliability, float32(0.0))
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

	w := validate.NewWorker(consumer, producer, 1, config.DefaultValidateConfig())

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

	w := validate.NewWorker(consumer, producer, 1, config.DefaultValidateConfig())

	content := newNewsContent()
	msg := makeProcessingMessage(content, 0)

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

	w := validate.NewWorker(consumer, producer, 1, config.DefaultValidateConfig())

	content := newNewsContent()
	content.Body = strings.Repeat("This is a very long news article body. ", 200)
	content.WordCount = 1400
	msg := makeProcessingMessage(content, 0)

	producer.On("Publish", mock.Anything, mock.MatchedBy(func(m queue.Message) bool {
		return m.Topic == queue.TopicValidated
	})).Return(nil)
	consumer.On("CommitMessages", mock.Anything, mock.Anything).Return(nil)

	runWorker(t, consumer, w, msg)

	producer.AssertExpectations(t)
}
