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
	"issuetracker/internal/processor/enrich/extractor"
	"issuetracker/internal/processor/enrich/worker"
	"issuetracker/internal/processor/fetcher/core"
	"issuetracker/internal/storage"
	"issuetracker/internal/storage/model"
	"issuetracker/internal/storage/service"
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

// stubContentService 는 항상 ErrNotFound 를 반환하는 ContentService stub 입니다.
// 본 worker 의 enrichment 분기는 ErrNotFound 시 skip + forward 이므로 passthrough
// 시나리오에 적합 — content body 가 필요한 별도 테스트는 별도 stub 사용.
type stubContentService struct {
	service.ContentService
	content *core.Content
}

func (s *stubContentService) GetByID(_ context.Context, id string) (*core.Content, error) {
	if s.content != nil && s.content.ID == id {
		return s.content, nil
	}
	return nil, storage.ErrNotFound
}

// stub 의 다른 메소드는 본 테스트에서 미사용 — interface embedded 로 default nil-impl.
// 정적 검사 만족용 어서션:
var _ service.ContentService = (*stubContentService)(nil)
var _ = model.ValidationStatusPassed // model import keep — 외부 테스트가 향후 사용 시 보존

// fakeExtractor 는 호출 카운트를 추적하는 간단한 Extractor 입니다.
type fakeExtractor struct {
	facts    *extractor.EnrichedFacts
	err      error
	callsLog []extractor.Input
}

func (f *fakeExtractor) Extract(_ context.Context, in extractor.Input) (*extractor.EnrichedFacts, error) {
	f.callsLog = append(f.callsLog, in)
	if f.err != nil {
		return nil, f.err
	}
	return f.facts, nil
}

func newEnrichWorker(consumer queue.Consumer, producer queue.Producer) *worker.Worker {
	return worker.NewWorker(
		consumer,
		newTestPublisher(producer),
		&stubContentService{},
		extractor.NewNoopExtractor(),
		locks.NewNoopStageGate(),
		1,
	)
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

// TestEnrichWorker_Extraction_AttachesFactsToMetadata — 추출 성공 시 EnrichedFacts 가
// ProcessingMessage.Metadata["enriched_facts"] 로 첨부되어 forward 되는지 검증.
func TestEnrichWorker_Extraction_AttachesFactsToMetadata(t *testing.T) {
	consumer := new(mockConsumer)
	producer := new(mockProducer)

	contentSvc := &stubContentService{content: &core.Content{
		ID:    "content-extract",
		Title: "Sample article",
		Body:  "Body text of the article",
		URL:   "https://example.com/a",
	}}
	expectedFacts := &extractor.EnrichedFacts{
		Entities: []extractor.Entity{{Type: extractor.EntityTypeOrg, Name: "ACME", Mentions: 3}},
		Claims:   []extractor.Claim{{Text: "ACME announced X"}},
		Facts:    []extractor.Fact{{Key: "amount", Value: "100", Unit: "USD"}},
		Topics:   []string{"business"},
	}
	fake := &fakeExtractor{facts: expectedFacts}

	w := worker.NewWorker(
		consumer,
		newTestPublisher(producer),
		contentSvc,
		fake,
		locks.NewNoopStageGate(),
		1,
	)

	msg := makeValidatedMessage(t, "content-extract", "https://example.com/a", "US", "example")

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

	// extractor 호출 검증
	if assert.Len(t, fake.callsLog, 1) {
		in := fake.callsLog[0]
		assert.Equal(t, "https://example.com/a", in.URL)
		assert.Equal(t, "example.com", in.Host)
		assert.Equal(t, "Sample article", in.Title)
		assert.Equal(t, "Body text of the article", in.HTML)
	}

	// metadata 첨부 검증
	var pm core.ProcessingMessage
	if err := json.Unmarshal(published.Value, &pm); err != nil {
		t.Fatalf("unmarshal pm: %v", err)
	}
	if assert.NotNil(t, pm.Metadata) {
		facts, ok := pm.Metadata["enriched_facts"]
		assert.True(t, ok, "metadata should contain enriched_facts")
		_ = facts // type 은 map[string]interface{} 로 decode 됨 — 필드 존재만 검증
	}
}

// TestEnrichWorker_ExtractionFailure_StillForwards — 추출 실패는 pipeline 진행을 막지 않음.
// extractor 가 error 를 반환해도 forward 가 수행되어 다음 단계로 메시지가 전달되어야 함.
func TestEnrichWorker_ExtractionFailure_StillForwards(t *testing.T) {
	consumer := new(mockConsumer)
	producer := new(mockProducer)

	contentSvc := &stubContentService{content: &core.Content{
		ID:    "content-fail",
		Title: "T",
		Body:  "B",
	}}
	fake := &fakeExtractor{err: assertAnError()}

	w := worker.NewWorker(
		consumer,
		newTestPublisher(producer),
		contentSvc,
		fake,
		locks.NewNoopStageGate(),
		1,
	)
	msg := makeValidatedMessage(t, "content-fail", "https://example.com/b", "KR", "example-kr")

	producer.On("Publish", mock.Anything, mock.MatchedBy(func(m queue.Message) bool {
		return m.Topic == queue.TopicEnriched
	})).Return(nil).Once()
	producer.On("Close").Return(nil).Maybe()
	consumer.On("CommitMessages", mock.Anything, mock.Anything).Return(nil).Maybe()

	runWorker(t, consumer, w, msg)

	consumer.AssertExpectations(t)
	producer.AssertExpectations(t)
}

func assertAnError() error { return assertError{} }

type assertError struct{}

func (assertError) Error() string { return "extractor failure" }

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
