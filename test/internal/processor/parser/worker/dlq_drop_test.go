package worker_test

// dlq_drop_test.go — 이슈 #640: 메시지를 버리던 두 경로 검증.
//
//   1. 손상된 payload 를 DLQ 없이 commit 하던 경로
//   2. parser/publisher 미주입 시 raw 를 삭제하고 commit 하던 경로

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/bus"
	"issuetracker/internal/processor/fetcher/core"
	parserWorker "issuetracker/internal/processor/parser/worker"
	"issuetracker/internal/storage/model"
	"issuetracker/pkg/logger"
	"issuetracker/pkg/queue"
)

// ─────────────────────────────────────────────────────────────────────────────
// fakes
// ─────────────────────────────────────────────────────────────────────────────

// dlqProducer 는 발행 메시지를 보관하고, 필요 시 실패를 흉내냅니다.
type dlqProducer struct {
	mu        sync.Mutex
	published []queue.Message
	failWith  error
}

func (p *dlqProducer) Publish(_ context.Context, msg queue.Message) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failWith != nil {
		return p.failWith
	}
	p.published = append(p.published, msg)
	return nil
}

func (p *dlqProducer) PublishBatch(_ context.Context, msgs []queue.Message) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failWith != nil {
		return p.failWith
	}
	p.published = append(p.published, msgs...)
	return nil
}

func (p *dlqProducer) Close() error { return nil }

func (p *dlqProducer) messages() []queue.Message {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]queue.Message(nil), p.published...)
}

// presentRawSvc 는 raw 가 존재하는 상태를 흉내내고 Delete 호출을 셉니다.
//
// 기존 fakeRawSvc 는 GetByID 가 항상 ErrNotFound 라 parser 분기까지 도달하지 못합니다.
type presentRawSvc struct {
	mu          sync.Mutex
	raw         *core.RawContent
	deleteCalls int
}

func (s *presentRawSvc) Store(context.Context, *core.RawContent) (string, bool, error) {
	return "", false, nil
}

func (s *presentRawSvc) GetByID(context.Context, string) (*core.RawContent, error) {
	return s.raw, nil
}

func (s *presentRawSvc) Delete(context.Context, string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deleteCalls++
	return nil
}

func (s *presentRawSvc) List(context.Context, model.RawContentFilter) ([]*core.RawContent, error) {
	return nil, nil
}

func (s *presentRawSvc) PurgeOlderThan(context.Context, time.Time) (int64, error) {
	return 0, nil
}

func (s *presentRawSvc) deletes() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.deleteCalls
}

func testLogger() *logger.Logger {
	cfg := logger.DefaultConfig()
	cfg.Level = logger.LevelError
	return logger.New(cfg)
}

// newWorkerWithoutParser 는 parser 가 주입되지 않은 Worker 를 만듭니다 (배선 실수 재현).
func newWorkerWithoutParser(pub *bus.Publisher, rawSvc *presentRawSvc) *parserWorker.Worker {
	return parserWorker.NewWorker(
		nil,    // consumer
		pub,    // pub
		rawSvc, // rawSvc
		nil,    // contentSvc
		nil,    // parser ← 배선 누락
		nil,    // resolver
		nil,    // sampleSvc
		nil,    // gate → noop (항상 획득)
		nil,    // llmGen
		nil,    // failureCounter
		nil,    // rawIDTracker
		nil,    // upgrader
		0, 0, 1,
		testLogger(),
	)
}

func rawContentFixture() *core.RawContent {
	return &core.RawContent{
		ID:        "raw-640",
		URL:       "https://example.com/article/640",
		HTML:      "<html><body>본문</body></html>",
		FetchedAt: time.Now(),
		SourceInfo: core.SourceInfo{
			Name:    "test-crawler",
			Country: "KR",
		},
	}
}

func refMessage(t *testing.T, targetType core.TargetType) *queue.Message {
	t.Helper()

	payload, err := json.Marshal(core.RawContentRef{
		ID:        "raw-640",
		URL:       "https://example.com/article/640",
		FetchedAt: time.Now(),
	})
	require.NoError(t, err)

	return &queue.Message{
		Topic:   queue.TopicFetched,
		Value:   payload,
		Headers: map[string]string{"target_type": string(targetType)},
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 결함 1 — 손상 payload
// ─────────────────────────────────────────────────────────────────────────────

func malformedMessage() *queue.Message {
	return &queue.Message{
		Topic:   queue.TopicFetched,
		Key:     []byte("k-640"),
		Value:   []byte(`{"id": "raw-640", "url":`), // 잘린 JSON
		Headers: map[string]string{"crawler": "test-crawler"},
	}
}

func TestProcessMessage_MalformedPayload_SendsToDLQ(t *testing.T) {
	prod := &dlqProducer{}
	log := testLogger()
	w := newWorkerWithoutParser(bus.New(prod, nil, log), &presentRawSvc{})

	err := w.ProcessMessage(context.Background(), malformedMessage())

	// DLQ 발행에 성공했으면 호출자가 commit 하도록 nil 을 반환한다.
	require.NoError(t, err)

	msgs := prod.messages()
	require.Len(t, msgs, 1, "손상 payload 가 DLQ 로 가지 않고 버려졌습니다")
	assert.Equal(t, queue.TopicDLQ, msgs[0].Topic)
	assert.Equal(t, []byte("k-640"), msgs[0].Key)
	assert.Equal(t, queue.TopicFetched, msgs[0].Headers["original-topic"],
		"어느 토픽에서 왔는지 남아야 재처리가 가능합니다")
	assert.NotEmpty(t, msgs[0].Headers["error"], "실패 사유가 남아야 합니다")
	assert.Equal(t, "test-crawler", msgs[0].Headers["crawler"], "원본 헤더가 보존돼야 합니다")
}

// DLQ 발행이 실패했는데 commit 하면 메시지가 유실된다 — error 를 반환해 재소비를 보장한다.
func TestProcessMessage_MalformedPayload_DLQFailure_ReturnsError(t *testing.T) {
	prod := &dlqProducer{failWith: errors.New("broker unavailable")}
	log := testLogger()
	w := newWorkerWithoutParser(bus.New(prod, nil, log), &presentRawSvc{})

	err := w.ProcessMessage(context.Background(), malformedMessage())

	require.Error(t, err, "DLQ 실패인데 nil 을 반환하면 호출자가 commit 해 메시지가 사라집니다")
	assert.Contains(t, err.Error(), "dlq")
}

func TestProcessMessage_MalformedPayload_NoPublisher_ReturnsError(t *testing.T) {
	w := newWorkerWithoutParser(nil, &presentRawSvc{})

	err := w.ProcessMessage(context.Background(), malformedMessage())

	require.Error(t, err, "publisher 미주입이면 격리할 수단이 없으므로 commit 되면 안 됩니다")
}

// ─────────────────────────────────────────────────────────────────────────────
// 결함 2 — parser 미주입
// ─────────────────────────────────────────────────────────────────────────────

// 배선 실수로 크롤링한 원본을 삭제하면 복구 수단이 없다.
func TestProcessMessage_NilParser_Article_DoesNotDeleteRaw(t *testing.T) {
	rawSvc := &presentRawSvc{raw: rawContentFixture()}
	prod := &dlqProducer{}
	log := testLogger()
	w := newWorkerWithoutParser(bus.New(prod, nil, log), rawSvc)

	err := w.ProcessMessage(context.Background(), refMessage(t, core.TargetTypeArticle))

	require.Error(t, err, "parser 미주입인데 nil 을 반환하면 메시지가 commit 되어 사라집니다")
	assert.Equal(t, 0, rawSvc.deletes(), "배선 실수로 raw 가 삭제됐습니다")
}

func TestProcessMessage_NilParser_Category_DoesNotDeleteRaw(t *testing.T) {
	rawSvc := &presentRawSvc{raw: rawContentFixture()}
	prod := &dlqProducer{}
	log := testLogger()
	w := newWorkerWithoutParser(bus.New(prod, nil, log), rawSvc)

	err := w.ProcessMessage(context.Background(), refMessage(t, core.TargetTypeCategory))

	require.Error(t, err)
	assert.Equal(t, 0, rawSvc.deletes(), "배선 실수로 raw 가 삭제됐습니다")
}
