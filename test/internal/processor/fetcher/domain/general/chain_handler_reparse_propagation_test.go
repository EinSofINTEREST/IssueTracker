package general_test

// chain_handler_reparse_propagation_test.go — Sub C (#366) — TopicFetched 발행 시
// validate_reparse_count / validate_reparse_reason 헤더 전파 검증.

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/processor/fetcher/core"
	"issuetracker/internal/processor/fetcher/domain/general"
	"issuetracker/internal/publisher"
	"issuetracker/internal/storage"
	"issuetracker/pkg/logger"
	"issuetracker/pkg/queue"
)

// successChain 은 정상 raw content 를 반환하는 stub general.Handler.
type successChain struct {
	raw *core.RawContent
}

func (s *successChain) Handle(_ context.Context, _ *core.CrawlJob) (*core.RawContent, error) {
	return s.raw, nil
}
func (*successChain) SetNext(_ general.Handler) {}

// captureRawSvc 는 Store 호출을 캡쳐하면서 ID 를 부여하는 stub.
type captureRawSvc struct {
	mu        sync.Mutex
	stored    *core.RawContent
	returnID  string
	returnDup bool
}

func (c *captureRawSvc) Store(_ context.Context, raw *core.RawContent) (string, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stored = raw
	return c.returnID, c.returnDup, nil
}
func (*captureRawSvc) GetByID(_ context.Context, _ string) (*core.RawContent, error) {
	return nil, storage.ErrNotFound
}
func (*captureRawSvc) Delete(_ context.Context, _ string) error { return nil }
func (*captureRawSvc) List(_ context.Context, _ storage.RawContentFilter) ([]*core.RawContent, error) {
	return nil, nil
}
func (*captureRawSvc) PurgeOlderThan(_ context.Context, _ time.Time) (int64, error) { return 0, nil }

// captureProducer 는 Publish 호출의 msg 를 캡쳐합니다.
type captureProducer struct {
	mu        sync.Mutex
	published []queue.Message
}

func (p *captureProducer) Publish(_ context.Context, msg queue.Message) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.published = append(p.published, msg)
	return nil
}
func (p *captureProducer) PublishBatch(_ context.Context, msgs []queue.Message) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.published = append(p.published, msgs...)
	return nil
}
func (*captureProducer) Close() error { return nil }

func newCaptureHandler(t *testing.T, raw *core.RawContent, rawID string) (*general.ChainHandler, *captureProducer) {
	t.Helper()
	log := logger.New(logger.DefaultConfig())
	rawSvc := &captureRawSvc{returnID: rawID}
	prod := &captureProducer{}
	// 이슈 #392 — chain_handler 가 *publisher.Publisher 의존으로 변경됨에 따라 mock producer 를
	// 실제 publisher 로 wrap (Sub 5/7 동일 패턴). captureProducer.published 는 pub.Forward 위임
	// 경로로 그대로 트리거됨.
	pub := publisher.New(prod, nil, log)
	h := general.NewChainHandler(
		nil, // SourceCrawler — fast path 에서 사용 X
		&successChain{raw: raw},
		nil, // no chromedp chains
		nil, // no resolver
		rawSvc,
		pub,
		log,
	)
	return h, prod
}

// TestChainHandler_PropagatesReparseHeaders 는 inbox headers 의 reparse 키가
// TopicFetched 발행 메시지에 전파되는지 검증합니다 (이슈 #366 의 핵심 동작).
func TestChainHandler_PropagatesReparseHeaders(t *testing.T) {
	raw := &core.RawContent{
		URL:        "https://example.com/article/123",
		FetchedAt:  time.Now(),
		SourceInfo: core.SourceInfo{Name: "test-crawler", Country: "KR"},
		HTML:       "<html></html>",
		StatusCode: 200,
	}
	h, prod := newCaptureHandler(t, raw, "raw-001")

	// inbox headers 에 reparse 정보 첨부
	ctx := core.WithInboxHeaders(context.Background(), map[string]string{
		core.HeaderValidateReparseCount:  "1",
		core.HeaderValidateReparseReason: "PublishedAt required",
		core.HeaderCrawler:               "test-crawler",
	})

	job := &core.CrawlJob{
		ID:          "job-1",
		CrawlerName: "test-crawler",
		Target:      core.Target{URL: raw.URL, Type: core.TargetTypeArticle},
		Timeout:     30 * time.Second,
	}

	_, err := h.Handle(ctx, job)
	require.NoError(t, err)

	require.Len(t, prod.published, 1)
	msg := prod.published[0]
	assert.Equal(t, queue.TopicFetched, msg.Topic)
	// reparse 헤더가 정확히 propagate 되었는지 확인
	assert.Equal(t, "1", msg.Headers[core.HeaderValidateReparseCount])
	assert.Equal(t, "PublishedAt required", msg.Headers[core.HeaderValidateReparseReason])
	// 기존 헤더도 정상 (target_type, crawler 등)
	assert.Equal(t, string(core.TargetTypeArticle), msg.Headers[core.HeaderTargetType])
	assert.Equal(t, "test-crawler", msg.Headers[core.HeaderCrawler])
}

// TestChainHandler_NoReparseHeaders_PublishedClean 는 inbox headers 가 없으면
// 발행된 메시지에도 reparse 키가 없는지 검증 (정상 경로 회귀 보호).
func TestChainHandler_NoReparseHeaders_PublishedClean(t *testing.T) {
	raw := &core.RawContent{
		URL:        "https://example.com/article/456",
		FetchedAt:  time.Now(),
		SourceInfo: core.SourceInfo{Name: "test-crawler", Country: "KR"},
		HTML:       "<html></html>",
		StatusCode: 200,
	}
	h, prod := newCaptureHandler(t, raw, "raw-002")

	job := &core.CrawlJob{
		ID:          "job-2",
		CrawlerName: "test-crawler",
		Target:      core.Target{URL: raw.URL, Type: core.TargetTypeArticle},
		Timeout:     30 * time.Second,
	}

	// inbox headers 없는 ctx
	_, err := h.Handle(context.Background(), job)
	require.NoError(t, err)

	require.Len(t, prod.published, 1)
	msg := prod.published[0]
	_, hasCount := msg.Headers[core.HeaderValidateReparseCount]
	_, hasReason := msg.Headers[core.HeaderValidateReparseReason]
	assert.False(t, hasCount, "inbox 부재 시 reparse_count 없어야 함")
	assert.False(t, hasReason, "inbox 부재 시 reparse_reason 없어야 함")
}
