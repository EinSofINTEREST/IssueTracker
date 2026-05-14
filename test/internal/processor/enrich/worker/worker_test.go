// 본 sub-issue (#446) 의 enrich worker 는 passthrough 만 — 입력 TopicValidated 메시지를
// 그대로 TopicEnriched 로 forward. 후속 sub-issue 가 enrichment 로직을 본 worker 에
// 점진적으로 추가하면 본 테스트의 시그니처 (Forward 1회 + commit) 는 유지되되 추가 assertion
// 이 늘어남.
package worker_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"issuetracker/internal/bus"
	"issuetracker/internal/locks"
	"issuetracker/internal/processor/enrich/worker"
	"issuetracker/internal/processor/fetcher/core"
	"issuetracker/pkg/logger"
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

func newTestPublisher(producer queue.Producer) *bus.Publisher {
	return bus.New(producer, nil, logger.New(logger.DefaultConfig()))
}

func newEnrichWorker(consumer queue.Consumer, producer queue.Producer) *worker.Worker {
	return worker.NewWorker(consumer, newTestPublisher(producer), locks.NewNoopStageGate(), 1)
}

func makeValidatedMessage(t *testing.T, refID, url, country, sourceName string) *queue.Message {
	t.Helper()
	ref := core.ContentRef{
		ID:      refID,
		URL:     url,
		Country: country,
		SourceInfo: core.SourceInfo{
			Country: country,
			Name:    sourceName,
		},
	}
	data, err := json.Marshal(ref)
	if err != nil {
		t.Fatalf("marshal ref: %v", err)
	}
	pm := core.ProcessingMessage{
		ID:        "pm-test-001",
		Timestamp: time.Now(),
		Country:   country,
		Stage:     "validated",
		Data:      data,
	}
	value, err := json.Marshal(pm)
	if err != nil {
		t.Fatalf("marshal pm: %v", err)
	}
	return &queue.Message{
		Topic: queue.TopicValidated,
		Key:   []byte(refID),
		Value: value,
	}
}

func runWorker(t *testing.T, consumer *mockConsumer, w *worker.Worker, msg *queue.Message) {
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

// TestEnrichWorker_Passthrough_ForwardsToEnrichedTopic 는 입력 메시지가 그대로
// TopicEnriched 로 forward 되는지 검증합니다 (sub-issue #446 스켈레톤 동작).
func TestEnrichWorker_Passthrough_ForwardsToEnrichedTopic(t *testing.T) {
	consumer := new(mockConsumer)
	producer := new(mockProducer)
	w := newEnrichWorker(consumer, producer)

	msg := makeValidatedMessage(t, "content-001", "https://example.com/a", "US", "example")

	// Publish: 어떤 메시지든 받아서 SUCCESS — 본 sub-issue 는 payload assertion 만 헬퍼.
	var published queue.Message
	producer.On("Publish", mock.Anything, mock.MatchedBy(func(m queue.Message) bool {
		published = m
		return m.Topic == queue.TopicEnriched
	})).Return(nil).Once()
	producer.On("Close").Return(nil).Maybe()

	// Commit: any message
	consumer.On("CommitMessages", mock.Anything, mock.Anything).Return(nil).Maybe()

	runWorker(t, consumer, w, msg)

	consumer.AssertExpectations(t)
	producer.AssertExpectations(t)

	// 발행된 메시지의 페이로드도 ProcessingMessage + ContentRef 구조여야 함.
	var pm core.ProcessingMessage
	if err := json.Unmarshal(published.Value, &pm); err != nil {
		t.Fatalf("published value not a ProcessingMessage: %v", err)
	}
	assert.Equal(t, "enriched", pm.Stage)
	assert.Equal(t, "US", pm.Country)

	var ref core.ContentRef
	if err := json.Unmarshal(pm.Data, &ref); err != nil {
		t.Fatalf("published data not a ContentRef: %v", err)
	}
	assert.Equal(t, "content-001", ref.ID)
	assert.Equal(t, "https://example.com/a", ref.URL)
	assert.Equal(t, "enriched", published.Headers["stage"])
	assert.Equal(t, "example", published.Headers["source"])
}

// TestEnrichWorker_Passthrough_PreservesOriginalHeaders — 입력 메시지의 헤더 (trace ID
// 등 observability 메타데이터) 가 forward 시 보존되는지 검증 (gemini PR #451 피드백).
func TestEnrichWorker_Passthrough_PreservesOriginalHeaders(t *testing.T) {
	consumer := new(mockConsumer)
	producer := new(mockProducer)
	w := newEnrichWorker(consumer, producer)

	msg := makeValidatedMessage(t, "content-002", "https://example.com/b", "KR", "example-kr")
	msg.Headers = map[string]string{
		"x-trace-id":   "trace-abc-123",
		"x-request-id": "req-xyz-789",
		"stage":        "validated", // stage-specific 키는 덮어써져야 함
	}

	var published queue.Message
	producer.On("Publish", mock.Anything, mock.MatchedBy(func(m queue.Message) bool {
		if m.Topic != queue.TopicEnriched {
			return false
		}
		published = m
		return true
	})).Return(nil).Once()
	producer.On("Close").Return(nil).Maybe()
	consumer.On("CommitMessages", mock.Anything, mock.Anything).Return(nil).Maybe()

	runWorker(t, consumer, w, msg)

	// observability 헤더는 보존
	assert.Equal(t, "trace-abc-123", published.Headers["x-trace-id"])
	assert.Equal(t, "req-xyz-789", published.Headers["x-request-id"])
	// stage-specific 헤더는 덮어쓰기
	assert.Equal(t, "enriched", published.Headers["stage"])
	assert.Equal(t, "example-kr", published.Headers["source"])
	assert.Equal(t, "KR", published.Headers["country"])
}

// TestEnrichWorker_MalformedMessage_SendsToDLQ — 잘못된 JSON 은 DLQ 로 라우팅.
func TestEnrichWorker_MalformedMessage_SendsToDLQ(t *testing.T) {
	consumer := new(mockConsumer)
	producer := new(mockProducer)
	w := newEnrichWorker(consumer, producer)

	msg := &queue.Message{
		Topic: queue.TopicValidated,
		Key:   []byte("k"),
		Value: []byte("{not json"),
	}

	producer.On("Publish", mock.Anything, mock.MatchedBy(func(m queue.Message) bool {
		return m.Topic == queue.TopicDLQ
	})).Return(nil).Once()
	producer.On("Close").Return(nil).Maybe()
	consumer.On("CommitMessages", mock.Anything, mock.Anything).Return(nil).Maybe()

	runWorker(t, consumer, w, msg)

	consumer.AssertExpectations(t)
	producer.AssertExpectations(t)
}
