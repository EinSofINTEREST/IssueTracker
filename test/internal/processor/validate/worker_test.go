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
	// 이슈 #135 — newsArticleRepo nil 전달 시 worker 가 update 단계를 skip 하므로 기존 테스트
	// 시나리오를 그대로 보존. 별도 테스트가 newsArticleRepo 호출 동작을 검증.
	return validate.NewWorker(consumer, producer, contentSvc, nil, 1, config.DefaultValidateConfig())
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

// ─────────────────────────────────────────────────────────────────────────────
// Issue #81: shutdown 시 commit / publish 실패 → drain context 재시도 검증
// ─────────────────────────────────────────────────────────────────────────────

// commit 첫 시도가 ctx.Canceled 로 실패해도, drain context 로 재시도하여 성공시키는지 검증.
// 이로써 graceful shutdown 직전 검증 통과 메시지의 offset 이 유실되지 않음.
func TestValidateWorker_CommitDrainsOnContextCanceled(t *testing.T) {
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

	// 첫 commit 호출은 ctx.Canceled, 두 번째(drain ctx)는 성공
	consumer.On("CommitMessages", mock.Anything, mock.Anything).
		Return(context.Canceled).Once()
	consumer.On("CommitMessages", mock.Anything, mock.Anything).
		Return(nil).Once()

	runWorker(t, consumer, w, msg)

	producer.AssertExpectations(t)
	// CommitMessages 가 정확히 두 번(원본 ctx + drain ctx) 호출되었는지 검증
	consumer.AssertNumberOfCalls(t, "CommitMessages", 2)
}

// commit 의 첫 시도가 ctx.Canceled 가 아닌 일반 에러일 때는 drain 재시도를 하지 않고 즉시 에러 반환하는지 검증.
// (drain 재시도는 graceful shutdown 시나리오 한정이어야 함)
func TestValidateWorker_CommitDoesNotDrainOnNonCancelError(t *testing.T) {
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

	// 일반 에러: drain 재시도 없음
	consumer.On("CommitMessages", mock.Anything, mock.Anything).
		Return(errors.New("kafka broker unreachable")).Once()

	runWorker(t, consumer, w, msg)

	consumer.AssertNumberOfCalls(t, "CommitMessages", 1)
}

// publish 첫 시도가 ctx.Canceled 로 실패해도, drain context 로 재시도하여 publish + commit 모두 성공시키는지 검증.
// 이로써 graceful shutdown 직전 검증 통과 메시지가 validated 토픽으로 발행되고 offset 도 commit 됨.
func TestValidateWorker_PublishDrainsOnContextCanceled(t *testing.T) {
	consumer := new(mockConsumer)
	producer := new(mockProducer)
	contentSvc := new(mockContentService)

	w := newWorker(consumer, producer, contentSvc)

	content := newNewsContent()
	msg := makeProcessingMessage(content, 0)

	contentSvc.On("GetByID", mock.Anything, content.ID).Return(content, nil)
	// 첫 publish 호출은 ctx.Canceled, 두 번째(drain ctx)는 성공
	producer.On("Publish", mock.Anything, mock.MatchedBy(func(m queue.Message) bool {
		return m.Topic == queue.TopicValidated
	})).Return(context.Canceled).Once()
	producer.On("Publish", mock.Anything, mock.MatchedBy(func(m queue.Message) bool {
		return m.Topic == queue.TopicValidated
	})).Return(nil).Once()

	consumer.On("CommitMessages", mock.Anything, mock.Anything).Return(nil)

	runWorker(t, consumer, w, msg)

	// Publish 가 정확히 두 번(원본 ctx + drain ctx) 호출되었는지 검증
	producer.AssertNumberOfCalls(t, "Publish", 2)
	// commit 도 호출되었는지 (drain publish 성공 후 commit 단계 진입)
	consumer.AssertCalled(t, "CommitMessages", mock.Anything, mock.Anything)
}

// publish 첫 시도가 ctx.Canceled, drain 재시도도 실패할 때:
//   - publish 두 번(원본+drain) 호출됨
//   - commit 은 호출되지 않음 (offset 보존 → 다음 기동 시 재소비)
func TestValidateWorker_PublishDrainFails_DoesNotCommit(t *testing.T) {
	consumer := new(mockConsumer)
	producer := new(mockProducer)
	contentSvc := new(mockContentService)

	w := newWorker(consumer, producer, contentSvc)

	content := newNewsContent()
	msg := makeProcessingMessage(content, 0)

	contentSvc.On("GetByID", mock.Anything, content.ID).Return(content, nil)
	// 첫 publish: ctx.Canceled
	producer.On("Publish", mock.Anything, mock.MatchedBy(func(m queue.Message) bool {
		return m.Topic == queue.TopicValidated
	})).Return(context.Canceled).Once()
	// drain publish: 일반 에러 (broker down 등)
	producer.On("Publish", mock.Anything, mock.MatchedBy(func(m queue.Message) bool {
		return m.Topic == queue.TopicValidated
	})).Return(errors.New("broker down")).Once()

	runWorker(t, consumer, w, msg)

	producer.AssertNumberOfCalls(t, "Publish", 2)
	// commit 은 호출되지 않아야 함 — offset 보존하여 재소비 가능 상태로 둠
	consumer.AssertNotCalled(t, "CommitMessages", mock.Anything, mock.Anything)
}

// publish 첫 시도가 ctx.Canceled 가 아닌 일반 에러일 때는 drain 재시도 없이 즉시 에러 반환,
// commit 도 호출되지 않음을 검증.
func TestValidateWorker_PublishDoesNotDrainOnNonCancelError(t *testing.T) {
	consumer := new(mockConsumer)
	producer := new(mockProducer)
	contentSvc := new(mockContentService)

	w := newWorker(consumer, producer, contentSvc)

	content := newNewsContent()
	msg := makeProcessingMessage(content, 0)

	contentSvc.On("GetByID", mock.Anything, content.ID).Return(content, nil)
	producer.On("Publish", mock.Anything, mock.MatchedBy(func(m queue.Message) bool {
		return m.Topic == queue.TopicValidated
	})).Return(errors.New("network unreachable")).Once()

	runWorker(t, consumer, w, msg)

	producer.AssertNumberOfCalls(t, "Publish", 1)
	consumer.AssertNotCalled(t, "CommitMessages", mock.Anything, mock.Anything)
}

// ─────────────────────────────────────────────────────────────────────────────
// gemini[high] 피드백: DLQ/requeue 실패 시 commit skip 으로 메시지 유실 방지
// ─────────────────────────────────────────────────────────────────────────────

// DLQ publish 실패 시 commit 이 호출되지 않아야 메시지가 유실되지 않음.
// 시나리오: malformed JSON message → sendToDLQ 호출 → publish 실패 (drain 도 실패)
//
//	→ process() 가 에러 반환 → commit 미호출 → 다음 기동 시 재소비.
func TestValidateWorker_DLQFailureSkipsCommit_PreventsMessageLoss(t *testing.T) {
	consumer := new(mockConsumer)
	producer := new(mockProducer)
	contentSvc := new(mockContentService)

	w := newWorker(consumer, producer, contentSvc)

	// malformed JSON → 첫 번째 unmarshal 분기에서 sendToDLQ 호출
	malformedMsg := &queue.Message{
		Topic: queue.TopicNormalized,
		Key:   []byte("bad-key"),
		Value: []byte("not-json{{{"),
	}

	// DLQ publish: 두 호출 모두 실패 (원본 ctx + drain ctx) — 일반 에러 시뮬레이션
	producer.On("Publish", mock.Anything, mock.MatchedBy(func(m queue.Message) bool {
		return m.Topic == queue.TopicDLQ
	})).Return(errors.New("kafka unavailable")).Once()

	runWorker(t, consumer, w, malformedMsg)

	// DLQ Publish 1회만 호출 (일반 에러는 drain 우회), commit 미호출 확인
	producer.AssertNumberOfCalls(t, "Publish", 1)
	consumer.AssertNotCalled(t, "CommitMessages", mock.Anything, mock.Anything)
}

// requeue publish 실패 시 commit 이 호출되지 않아야 함.
// 시나리오: 검증 실패 + RetryCount < maxRetries → requeue → publish 실패
//
//	→ process() 가 에러 반환 → commit 미호출 → 다음 기동 시 재소비.
func TestValidateWorker_RequeueFailureSkipsCommit_PreservesRetryChance(t *testing.T) {
	consumer := new(mockConsumer)
	producer := new(mockProducer)
	contentSvc := new(mockContentService)

	w := newWorker(consumer, producer, contentSvc)

	// 검증 실패할 컨텐츠 (제목/본문 길이 미달)
	content := newNewsContent()
	content.Title = ""
	content.Body = "x"
	content.WordCount = 0

	msg := makeProcessingMessage(content, 0) // RetryCount=0 → requeue 경로

	contentSvc.On("GetByID", mock.Anything, content.ID).Return(content, nil)
	// requeue 의 Publish 가 일반 에러로 실패
	producer.On("Publish", mock.Anything, mock.MatchedBy(func(m queue.Message) bool {
		return m.Topic == queue.TopicNormalized
	})).Return(errors.New("kafka unavailable")).Once()

	runWorker(t, consumer, w, msg)

	// requeue Publish 1회 호출, commit 미호출 확인
	producer.AssertNumberOfCalls(t, "Publish", 1)
	consumer.AssertNotCalled(t, "CommitMessages", mock.Anything, mock.Anything)
}

// DLQ 의 첫 publish 가 ctx.Canceled 로 실패해도 drain context 로 재시도하여 성공시키는지 검증.
// 성공 시 process() 는 commit 으로 진행함.
func TestValidateWorker_DLQDrainsOnContextCanceled(t *testing.T) {
	consumer := new(mockConsumer)
	producer := new(mockProducer)
	contentSvc := new(mockContentService)

	w := newWorker(consumer, producer, contentSvc)

	malformedMsg := &queue.Message{
		Topic: queue.TopicNormalized,
		Key:   []byte("bad-key"),
		Value: []byte("not-json{{{"),
	}

	// 첫 DLQ publish: ctx.Canceled, drain publish: 성공
	producer.On("Publish", mock.Anything, mock.MatchedBy(func(m queue.Message) bool {
		return m.Topic == queue.TopicDLQ
	})).Return(context.Canceled).Once()
	producer.On("Publish", mock.Anything, mock.MatchedBy(func(m queue.Message) bool {
		return m.Topic == queue.TopicDLQ
	})).Return(nil).Once()

	consumer.On("CommitMessages", mock.Anything, mock.Anything).Return(nil)

	runWorker(t, consumer, w, malformedMsg)

	producer.AssertNumberOfCalls(t, "Publish", 2)
	consumer.AssertCalled(t, "CommitMessages", mock.Anything, mock.Anything)
}
