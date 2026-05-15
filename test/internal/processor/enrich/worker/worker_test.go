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
	"github.com/stretchr/testify/require"

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

// fakeVerifier 는 verify 입력 추적 + 미리 정의된 verifications 또는 err 반환.
type fakeVerifier struct {
	verifications []extractor.Verification
	err           error
	callsLog      []extractor.VerifyInput
}

func (v *fakeVerifier) Verify(_ context.Context, in extractor.VerifyInput) ([]extractor.Verification, error) {
	v.callsLog = append(v.callsLog, in)
	if v.err != nil {
		return nil, v.err
	}
	return v.verifications, nil
}

func newEnrichWorker(consumer queue.Consumer, producer queue.Producer) *worker.Worker {
	return worker.NewWorker(
		consumer,
		newTestPublisher(producer),
		&stubContentService{},
		extractor.NewNoopExtractor(),
		extractor.NewNoopVerifier(),
		extractor.NewNoopContextualizer(),
		extractor.NewNoopScorer(),
		nil,
		locks.NewNoopStageGate(),
		1,
	)
}

// fakeContextualizer 는 context 입력을 캡쳐하고 미리 정의된 PageContext / err 를 반환합니다.
type fakeContextualizer struct {
	page     *extractor.PageContext
	err      error
	callsLog []extractor.ContextInput
}

func (c *fakeContextualizer) Provide(_ context.Context, in extractor.ContextInput) (*extractor.PageContext, error) {
	c.callsLog = append(c.callsLog, in)
	if c.err != nil {
		return nil, c.err
	}
	return c.page, nil
}

// fakeScorer 는 score 입력 추적 + 미리 정의된 result / err 반환.
type fakeScorer struct {
	result   *extractor.TrustScoreResult
	err      error
	callsLog []extractor.ScoreInput
}

func (s *fakeScorer) Score(_ context.Context, in extractor.ScoreInput) (*extractor.TrustScoreResult, error) {
	s.callsLog = append(s.callsLog, in)
	if s.err != nil {
		return nil, s.err
	}
	return s.result, nil
}

// fakeEnrichedRepo 는 EnrichedContentRepository stub — Upsert 호출 캡쳐 + err 반환.
type fakeEnrichedRepo struct {
	upsertCalls []*model.EnrichedContentRecord
	upsertErr   error
}

func (r *fakeEnrichedRepo) Upsert(_ context.Context, rec *model.EnrichedContentRecord) error {
	r.upsertCalls = append(r.upsertCalls, rec)
	if r.upsertErr != nil {
		return r.upsertErr
	}
	rec.ID = 42 // pretend assigned by DB
	return nil
}

func (r *fakeEnrichedRepo) GetByContentID(_ context.Context, _ string) (*model.EnrichedContentRecord, error) {
	return nil, storage.ErrNotFound
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
		extractor.NewNoopVerifier(),
		extractor.NewNoopContextualizer(),
		extractor.NewNoopScorer(),
		nil,
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
		extractor.NewNoopVerifier(),
		extractor.NewNoopContextualizer(),
		extractor.NewNoopScorer(),
		nil,
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

// listingStubContentService 는 GetByID + ListByCountry 를 모두 stub 합니다 — 이슈 #448 verification.
type listingStubContentService struct {
	service.ContentService
	primary *core.Content
	listed  []*core.Content
	listErr error
}

func (s *listingStubContentService) GetByID(_ context.Context, id string) (*core.Content, error) {
	if s.primary != nil && s.primary.ID == id {
		return s.primary, nil
	}
	return nil, storage.ErrNotFound
}

func (s *listingStubContentService) ListByCountry(_ context.Context, _ string, _ model.ContentFilter) ([]*core.Content, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.listed, nil
}

// TestEnrichWorker_Verification_AttachesVerificationsToFacts — claims 가 있으면 verifier
// 가 호출되고 결과가 facts.Verifications 에 첨부.
func TestEnrichWorker_Verification_AttachesVerificationsToFacts(t *testing.T) {
	consumer := new(mockConsumer)
	producer := new(mockProducer)

	publishedAt := time.Now().Add(-1 * time.Hour)
	primary := &core.Content{
		ID:          "c-primary",
		Title:       "Election turnout breaks record in city",
		Body:        "...",
		URL:         "https://example.com/p",
		Country:     "US",
		PublishedAt: publishedAt,
	}
	related := []*core.Content{
		{ID: "c-rel1", Title: "City election turnout sets new record", URL: "https://other.com/a", PublishedAt: publishedAt},
		{ID: "c-rel2", Title: "Local sports match results", URL: "https://other.com/b", PublishedAt: publishedAt},
		{ID: "c-primary", Title: "self should be skipped", URL: "https://example.com/p", PublishedAt: publishedAt},
	}
	contentSvc := &listingStubContentService{primary: primary, listed: related}

	extractedFacts := &extractor.EnrichedFacts{
		Claims: []extractor.Claim{{Text: "Turnout reached 60% in city"}},
	}
	fakeExt := &fakeExtractor{facts: extractedFacts}
	fakeVer := &fakeVerifier{verifications: []extractor.Verification{
		{ClaimIdx: 0, Verdict: "supported", Sources: []string{"https://news.example.org/x"}},
	}}

	w := worker.NewWorker(
		consumer,
		newTestPublisher(producer),
		contentSvc,
		fakeExt,
		fakeVer,
		extractor.NewNoopContextualizer(),
		extractor.NewNoopScorer(),
		nil,
		locks.NewNoopStageGate(),
		1,
	)
	msg := makeValidatedMessage(t, "c-primary", "https://example.com/p", "US", "example")

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

	// verifier 호출 검증 — candidate ranking: rel1 (overlap: city,election,turnout,record) 가 rel2 보다 우선
	if assert.Len(t, fakeVer.callsLog, 1, "verifier should be called once") {
		in := fakeVer.callsLog[0]
		assert.Equal(t, "https://example.com/p", in.URL)
		assert.Len(t, in.Claims, 1)
		// 본인 (c-primary URL) 은 candidate 에서 제외, rel1 이 rel2 보다 token overlap 높음
		assert.NotEmpty(t, in.Candidates)
		topCandidate := in.Candidates[0]
		assert.Equal(t, "https://other.com/a", topCandidate.URL)
		// 본인 URL 이 candidate 에 없음
		for _, c := range in.Candidates {
			assert.NotEqual(t, "https://example.com/p", c.URL, "source URL must be excluded")
		}
	}

	// metadata.enriched_facts.verifications 첨부 검증
	var pm core.ProcessingMessage
	if err := json.Unmarshal(published.Value, &pm); err != nil {
		t.Fatalf("unmarshal pm: %v", err)
	}
	rawFacts, ok := pm.Metadata["enriched_facts"].(map[string]interface{})
	if assert.True(t, ok, "metadata should contain enriched_facts map") {
		verifs, vok := rawFacts["verifications"].([]interface{})
		assert.True(t, vok)
		assert.Len(t, verifs, 1)
	}
}

// TestEnrichWorker_NoClaims_SkipsVerifier — claims 가 없으면 verifier 호출 안 함.
func TestEnrichWorker_NoClaims_SkipsVerifier(t *testing.T) {
	consumer := new(mockConsumer)
	producer := new(mockProducer)
	contentSvc := &listingStubContentService{primary: &core.Content{
		ID: "c-empty", Title: "t", Body: "b", URL: "https://example.com/e", Country: "US",
		PublishedAt: time.Now(),
	}}
	emptyFacts := &extractor.EnrichedFacts{Claims: []extractor.Claim{}}
	fakeVer := &fakeVerifier{}

	w := worker.NewWorker(
		consumer, newTestPublisher(producer), contentSvc,
		&fakeExtractor{facts: emptyFacts},
		fakeVer,
		extractor.NewNoopContextualizer(),
		extractor.NewNoopScorer(),
		nil,
		locks.NewNoopStageGate(),
		1,
	)
	msg := makeValidatedMessage(t, "c-empty", "https://example.com/e", "US", "example")

	producer.On("Publish", mock.Anything, mock.MatchedBy(func(m queue.Message) bool {
		return m.Topic == queue.TopicEnriched
	})).Return(nil).Once()
	producer.On("Close").Return(nil).Maybe()
	consumer.On("CommitMessages", mock.Anything, mock.Anything).Return(nil).Maybe()

	runWorker(t, consumer, w, msg)

	assert.Empty(t, fakeVer.callsLog, "verifier must not be called when no claims")
}

// TestEnrichWorker_VerificationFailure_StillForwards — verifier error 도 pipeline 진행 보장.
func TestEnrichWorker_VerificationFailure_StillForwards(t *testing.T) {
	consumer := new(mockConsumer)
	producer := new(mockProducer)
	contentSvc := &listingStubContentService{primary: &core.Content{
		ID: "c-v-fail", Title: "t", Body: "b", URL: "https://example.com/x", Country: "US",
		PublishedAt: time.Now(),
	}}
	facts := &extractor.EnrichedFacts{Claims: []extractor.Claim{{Text: "claim A"}}}

	w := worker.NewWorker(
		consumer, newTestPublisher(producer), contentSvc,
		&fakeExtractor{facts: facts},
		&fakeVerifier{err: assertAnError()},
		extractor.NewNoopContextualizer(),
		extractor.NewNoopScorer(),
		nil,
		locks.NewNoopStageGate(),
		1,
	)
	msg := makeValidatedMessage(t, "c-v-fail", "https://example.com/x", "US", "example")

	producer.On("Publish", mock.Anything, mock.MatchedBy(func(m queue.Message) bool {
		return m.Topic == queue.TopicEnriched
	})).Return(nil).Once()
	producer.On("Close").Return(nil).Maybe()
	consumer.On("CommitMessages", mock.Anything, mock.Anything).Return(nil).Maybe()

	runWorker(t, consumer, w, msg)

	consumer.AssertExpectations(t)
	producer.AssertExpectations(t)
}

// TestEnrichWorker_Context_AttachesPageContext — context provider 가 PageContext 를 반환하면
// facts.Context 에 첨부되어 forward 메시지의 metadata 에 포함 (이슈 #449).
func TestEnrichWorker_Context_AttachesPageContext(t *testing.T) {
	consumer := new(mockConsumer)
	producer := new(mockProducer)
	contentSvc := &listingStubContentService{primary: &core.Content{
		ID: "c-ctx", Title: "Sample", Body: "B", URL: "https://example.com/c", Country: "US",
		PublishedAt: time.Now(),
	}}
	facts := &extractor.EnrichedFacts{
		Entities: []extractor.Entity{{Type: extractor.EntityTypeOrg, Name: "Acme"}},
		Claims:   []extractor.Claim{{Text: "Acme is global"}},
	}
	contextResp := &extractor.PageContext{
		Background:   []extractor.BackgroundItem{{Subject: "Acme", Category: "org", Summary: "Founded 1980"}},
		Implications: &extractor.Implications{Social: "Workforce concerns"},
	}
	fakeCtx := &fakeContextualizer{page: contextResp}

	w := worker.NewWorker(
		consumer, newTestPublisher(producer), contentSvc,
		&fakeExtractor{facts: facts},
		&fakeVerifier{},
		fakeCtx,
		extractor.NewNoopScorer(),
		nil,
		locks.NewNoopStageGate(),
		1,
	)
	msg := makeValidatedMessage(t, "c-ctx", "https://example.com/c", "US", "example")

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

	// contextualizer 호출 입력 검증
	if assert.Len(t, fakeCtx.callsLog, 1) {
		in := fakeCtx.callsLog[0]
		assert.Equal(t, "https://example.com/c", in.URL)
		assert.Len(t, in.Entities, 1)
		assert.Len(t, in.Claims, 1)
	}

	// metadata.enriched_facts.context 첨부 검증
	var pm core.ProcessingMessage
	if err := json.Unmarshal(published.Value, &pm); err != nil {
		t.Fatalf("unmarshal pm: %v", err)
	}
	rawFacts, ok := pm.Metadata["enriched_facts"].(map[string]interface{})
	require.True(t, ok)
	pageCtx, ok := rawFacts["context"].(map[string]interface{})
	if assert.True(t, ok, "context should be attached") {
		bg, bgOK := pageCtx["background"].([]interface{})
		assert.True(t, bgOK)
		assert.Len(t, bg, 1)
	}
}

// TestEnrichWorker_NoEntitiesOrClaims_SkipsContext — 둘 다 없으면 contextualizer 미호출.
func TestEnrichWorker_NoEntitiesOrClaims_SkipsContext(t *testing.T) {
	consumer := new(mockConsumer)
	producer := new(mockProducer)
	contentSvc := &listingStubContentService{primary: &core.Content{
		ID: "c-skip", Title: "t", Body: "b", URL: "https://example.com/s", Country: "US",
		PublishedAt: time.Now(),
	}}
	facts := &extractor.EnrichedFacts{} // entities + claims 모두 nil/empty
	fakeCtx := &fakeContextualizer{}

	w := worker.NewWorker(
		consumer, newTestPublisher(producer), contentSvc,
		&fakeExtractor{facts: facts},
		&fakeVerifier{},
		fakeCtx,
		extractor.NewNoopScorer(),
		nil,
		locks.NewNoopStageGate(),
		1,
	)
	msg := makeValidatedMessage(t, "c-skip", "https://example.com/s", "US", "example")

	producer.On("Publish", mock.Anything, mock.MatchedBy(func(m queue.Message) bool {
		return m.Topic == queue.TopicEnriched
	})).Return(nil).Once()
	producer.On("Close").Return(nil).Maybe()
	consumer.On("CommitMessages", mock.Anything, mock.Anything).Return(nil).Maybe()

	runWorker(t, consumer, w, msg)

	assert.Empty(t, fakeCtx.callsLog, "contextualizer must not be called when entities/claims both empty")
}

// TestEnrichWorker_ContextFailure_StillForwards — context error 도 pipeline 진행 보장.
func TestEnrichWorker_ContextFailure_StillForwards(t *testing.T) {
	consumer := new(mockConsumer)
	producer := new(mockProducer)
	contentSvc := &listingStubContentService{primary: &core.Content{
		ID: "c-ctx-fail", Title: "t", Body: "b", URL: "https://example.com/x", Country: "US",
		PublishedAt: time.Now(),
	}}
	facts := &extractor.EnrichedFacts{
		Entities: []extractor.Entity{{Name: "X"}},
	}

	w := worker.NewWorker(
		consumer, newTestPublisher(producer), contentSvc,
		&fakeExtractor{facts: facts},
		&fakeVerifier{},
		&fakeContextualizer{err: assertAnError()},
		extractor.NewNoopScorer(),
		nil,
		locks.NewNoopStageGate(),
		1,
	)
	msg := makeValidatedMessage(t, "c-ctx-fail", "https://example.com/x", "US", "example")

	producer.On("Publish", mock.Anything, mock.MatchedBy(func(m queue.Message) bool {
		return m.Topic == queue.TopicEnriched
	})).Return(nil).Once()
	producer.On("Close").Return(nil).Maybe()
	consumer.On("CommitMessages", mock.Anything, mock.Anything).Return(nil).Maybe()

	runWorker(t, consumer, w, msg)

	consumer.AssertExpectations(t)
	producer.AssertExpectations(t)
}

// TestEnrichWorker_Scoring_PersistsToEnrichedContents — score 산출 후 enriched_contents
// 에 upsert 되고 metadata 에 trust_score 가 첨부되는지 검증 (이슈 #450).
func TestEnrichWorker_Scoring_PersistsToEnrichedContents(t *testing.T) {
	consumer := new(mockConsumer)
	producer := new(mockProducer)
	contentSvc := &listingStubContentService{primary: &core.Content{
		ID: "c-score", Title: "T", Body: "B", URL: "https://example.com/s", Country: "US",
		PublishedAt: time.Now(),
	}}
	facts := &extractor.EnrichedFacts{
		Entities: []extractor.Entity{{Name: "Acme"}},
		Claims:   []extractor.Claim{{Text: "Acme did X"}},
	}
	scorer := &fakeScorer{result: &extractor.TrustScoreResult{
		TrustScore: 0.85,
		Rationale:  "Two sources corroborate.",
		Factors: extractor.ScoreFactors{
			ClaimSupportRatio: 1.0, SourceDiversity: 0.8, ContextCompleteness: 0.6,
		},
	}}
	repo := &fakeEnrichedRepo{}

	w := worker.NewWorker(
		consumer, newTestPublisher(producer), contentSvc,
		&fakeExtractor{facts: facts},
		&fakeVerifier{verifications: []extractor.Verification{{ClaimIdx: 0, Verdict: "supported"}}},
		&fakeContextualizer{page: &extractor.PageContext{Background: []extractor.BackgroundItem{{Subject: "Acme"}}}},
		scorer,
		repo,
		locks.NewNoopStageGate(),
		1,
	)
	msg := makeValidatedMessage(t, "c-score", "https://example.com/s", "US", "example")

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

	// scorer 호출 입력 검증
	if assert.Len(t, scorer.callsLog, 1) {
		in := scorer.callsLog[0]
		assert.Equal(t, "https://example.com/s", in.URL)
		assert.NotEmpty(t, in.Facts)
	}

	// DB upsert 호출 검증
	if assert.Len(t, repo.upsertCalls, 1) {
		rec := repo.upsertCalls[0]
		assert.Equal(t, "c-score", rec.ContentID)
		assert.InDelta(t, 0.85, rec.TrustScore, 1e-9)
		assert.NotEmpty(t, rec.Facts)
		assert.NotEmpty(t, rec.Verifications)
		assert.NotEmpty(t, rec.Context)
	}

	// metadata.enriched_facts.trust_score 첨부 검증
	var pm core.ProcessingMessage
	require.NoError(t, json.Unmarshal(published.Value, &pm))
	rawFacts, ok := pm.Metadata["enriched_facts"].(map[string]interface{})
	if assert.True(t, ok) {
		ts, tsOK := rawFacts["trust_score"].(float64)
		assert.True(t, tsOK)
		assert.InDelta(t, 0.85, ts, 1e-9)
	}
}

// TestEnrichWorker_NoScorer_NoPersistence — scorer Noop 또는 score result nil 이면 DB 미저장.
func TestEnrichWorker_NoScorerResult_SkipsPersistence(t *testing.T) {
	consumer := new(mockConsumer)
	producer := new(mockProducer)
	contentSvc := &listingStubContentService{primary: &core.Content{
		ID: "c-noscore", Title: "T", Body: "B", URL: "https://example.com/n", Country: "US",
		PublishedAt: time.Now(),
	}}
	facts := &extractor.EnrichedFacts{Claims: []extractor.Claim{{Text: "x"}}}
	repo := &fakeEnrichedRepo{}

	w := worker.NewWorker(
		consumer, newTestPublisher(producer), contentSvc,
		&fakeExtractor{facts: facts},
		&fakeVerifier{},
		&fakeContextualizer{},
		extractor.NewNoopScorer(), // result nil
		repo,
		locks.NewNoopStageGate(),
		1,
	)
	msg := makeValidatedMessage(t, "c-noscore", "https://example.com/n", "US", "example")

	producer.On("Publish", mock.Anything, mock.MatchedBy(func(m queue.Message) bool {
		return m.Topic == queue.TopicEnriched
	})).Return(nil).Once()
	producer.On("Close").Return(nil).Maybe()
	consumer.On("CommitMessages", mock.Anything, mock.Anything).Return(nil).Maybe()

	runWorker(t, consumer, w, msg)

	assert.Empty(t, repo.upsertCalls, "no score → no DB upsert")
}

// TestEnrichWorker_NilRepo_StillForwards — enrichedRepo 가 nil 이어도 forward 보장.
func TestEnrichWorker_NilRepo_StillForwards(t *testing.T) {
	consumer := new(mockConsumer)
	producer := new(mockProducer)
	contentSvc := &listingStubContentService{primary: &core.Content{
		ID: "c-nilrepo", Title: "T", Body: "B", URL: "https://example.com/r", Country: "US",
		PublishedAt: time.Now(),
	}}
	facts := &extractor.EnrichedFacts{Claims: []extractor.Claim{{Text: "x"}}}

	w := worker.NewWorker(
		consumer, newTestPublisher(producer), contentSvc,
		&fakeExtractor{facts: facts},
		&fakeVerifier{},
		&fakeContextualizer{},
		&fakeScorer{result: &extractor.TrustScoreResult{TrustScore: 0.5}},
		nil, // enrichedRepo nil
		locks.NewNoopStageGate(),
		1,
	)
	msg := makeValidatedMessage(t, "c-nilrepo", "https://example.com/r", "US", "example")

	producer.On("Publish", mock.Anything, mock.MatchedBy(func(m queue.Message) bool {
		return m.Topic == queue.TopicEnriched
	})).Return(nil).Once()
	producer.On("Close").Return(nil).Maybe()
	consumer.On("CommitMessages", mock.Anything, mock.Anything).Return(nil).Maybe()

	runWorker(t, consumer, w, msg)

	consumer.AssertExpectations(t)
	producer.AssertExpectations(t)
}

// TestEnrichWorker_RepoUpsertFailure_StillForwards — DB upsert 실패도 forward 보장.
func TestEnrichWorker_RepoUpsertFailure_StillForwards(t *testing.T) {
	consumer := new(mockConsumer)
	producer := new(mockProducer)
	contentSvc := &listingStubContentService{primary: &core.Content{
		ID: "c-dberr", Title: "T", Body: "B", URL: "https://example.com/d", Country: "US",
		PublishedAt: time.Now(),
	}}
	facts := &extractor.EnrichedFacts{Claims: []extractor.Claim{{Text: "x"}}}
	repo := &fakeEnrichedRepo{upsertErr: assertAnError()}

	w := worker.NewWorker(
		consumer, newTestPublisher(producer), contentSvc,
		&fakeExtractor{facts: facts},
		&fakeVerifier{},
		&fakeContextualizer{},
		&fakeScorer{result: &extractor.TrustScoreResult{TrustScore: 0.5}},
		repo,
		locks.NewNoopStageGate(),
		1,
	)
	msg := makeValidatedMessage(t, "c-dberr", "https://example.com/d", "US", "example")

	producer.On("Publish", mock.Anything, mock.MatchedBy(func(m queue.Message) bool {
		return m.Topic == queue.TopicEnriched
	})).Return(nil).Once()
	producer.On("Close").Return(nil).Maybe()
	consumer.On("CommitMessages", mock.Anything, mock.Anything).Return(nil).Maybe()

	runWorker(t, consumer, w, msg)

	assert.Len(t, repo.upsertCalls, 1, "upsert should still be attempted")
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
