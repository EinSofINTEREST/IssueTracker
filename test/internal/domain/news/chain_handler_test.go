package news_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/crawler/core"
	"issuetracker/internal/crawler/domain/news"
	"issuetracker/pkg/logger"
)

// ─────────────────────────────────────────────────────────────────────────────
// Test doubles
// ─────────────────────────────────────────────────────────────────────────────

type stubChain struct {
	raw *core.RawContent
	err error
}

func (s *stubChain) Handle(_ context.Context, _ *core.CrawlJob) (*core.RawContent, error) {
	return s.raw, s.err
}
func (s *stubChain) SetNext(_ news.NewsHandler) {}

type stubListParser struct {
	items []news.NewsItem
	err   error
}

func (s *stubListParser) ParseList(_ *core.RawContent) ([]news.NewsItem, error) {
	return s.items, s.err
}

type stubArticleParser struct {
	article *news.NewsArticle
	err     error
}

func (s *stubArticleParser) ParseArticle(_ *core.RawContent) (*news.NewsArticle, error) {
	return s.article, s.err
}

type capturePublisher struct {
	calls []publishCall
	err   error
}

type publishCall struct {
	crawlerName string
	urls        []string
	targetType  core.TargetType
}

func (c *capturePublisher) Publish(_ context.Context, crawlerName string, urls []string, targetType core.TargetType, _ time.Duration) error {
	if c.err != nil {
		return c.err
	}
	c.calls = append(c.calls, publishCall{crawlerName: crawlerName, urls: urls, targetType: targetType})
	return nil
}

func newTestLogger() *logger.Logger {
	return logger.New(logger.DefaultConfig())
}

func categoryJob(url string) *core.CrawlJob {
	return &core.CrawlJob{
		ID:          "test-job",
		CrawlerName: "test-crawler",
		Target:      core.Target{URL: url, Type: core.TargetTypeCategory},
		Timeout:     5 * time.Second,
	}
}

func articleJob(url string) *core.CrawlJob {
	return &core.CrawlJob{
		ID:          "test-job",
		CrawlerName: "test-crawler",
		Target:      core.Target{URL: url, Type: core.TargetTypeArticle},
		Timeout:     5 * time.Second,
	}
}

func htmlRaw(url string, html string) *core.RawContent {
	return &core.RawContent{
		ID:         "raw-1",
		FetchedAt:  time.Now(),
		URL:        url,
		HTML:       html,
		StatusCode: 200,
		Headers:    map[string]string{},
		Metadata:   map[string]interface{}{},
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 초기화 검증
// ─────────────────────────────────────────────────────────────────────────────

func TestChainHandler_Handle_ChainNil_에러반환(t *testing.T) {
	h := news.NewChainHandler(nil, nil, nil, nil, nil, nil, newTestLogger())

	_, err := h.Handle(context.Background(), articleJob("https://example.com"))

	assert.Error(t, err)
}

func TestChainHandler_Handle_LogNil_에러반환(t *testing.T) {
	chain := &stubChain{raw: htmlRaw("https://example.com", "<html></html>")}
	h := news.NewChainHandler(nil, chain, nil, nil, nil, nil, nil)

	_, err := h.Handle(context.Background(), articleJob("https://example.com"))

	assert.Error(t, err)
}

// ─────────────────────────────────────────────────────────────────────────────
// nil raw 방어
// ─────────────────────────────────────────────────────────────────────────────

func TestChainHandler_Handle_ChainRawNil_에러반환(t *testing.T) {
	chain := &stubChain{raw: nil, err: nil}
	h := news.NewChainHandler(nil, chain, nil, nil, nil, nil, newTestLogger())

	_, err := h.Handle(context.Background(), articleJob("https://example.com"))

	assert.Error(t, err)
}

// ─────────────────────────────────────────────────────────────────────────────
// RSS 경로
// ─────────────────────────────────────────────────────────────────────────────

func TestChainHandler_Handle_RSSStrategy_다건Content반환(t *testing.T) {
	raw := &core.RawContent{
		ID:        "rss-1",
		FetchedAt: time.Now(),
		URL:       "https://rss.example.com/feed",
		Headers:   map[string]string{},
		Metadata: map[string]interface{}{
			"fetch_strategy": "rss",
			"rss_articles": []map[string]interface{}{
				{"title": "Title1", "url": "https://example.com/1", "body": "body1", "author": "A", "published_at": time.Now().Format(time.RFC3339)},
				{"title": "Title2", "url": "https://example.com/2", "body": "body2", "author": "B", "published_at": time.Now().Format(time.RFC3339)},
			},
		},
	}

	chain := &stubChain{raw: raw}
	h := news.NewChainHandler(nil, chain, nil, nil, nil, nil, newTestLogger())

	contents, err := h.Handle(context.Background(), &core.CrawlJob{
		ID: "job-1", CrawlerName: "cnn",
		Target: core.Target{URL: "https://rss.example.com/feed", Type: core.TargetTypeFeed},
	})

	require.NoError(t, err)
	assert.Len(t, contents, 2)
}

// ─────────────────────────────────────────────────────────────────────────────
// 카테고리 경로 — 정상
// ─────────────────────────────────────────────────────────────────────────────

func TestChainHandler_Handle_Category_기사URL발행(t *testing.T) {
	raw := htmlRaw("https://news.example.com/category", "<html></html>")
	chain := &stubChain{raw: raw}
	listParser := &stubListParser{items: []news.NewsItem{
		{URL: "https://news.example.com/1", Title: "A"},
		{URL: "https://news.example.com/2", Title: "B"},
	}}
	pub := &capturePublisher{}

	h := news.NewChainHandler(nil, chain, nil, listParser, pub, nil, newTestLogger())

	contents, err := h.Handle(context.Background(), categoryJob("https://news.example.com/category"))

	require.NoError(t, err)
	assert.Nil(t, contents)
	require.Len(t, pub.calls, 1)
	assert.Equal(t, []string{"https://news.example.com/1", "https://news.example.com/2"}, pub.calls[0].urls)
	assert.Equal(t, core.TargetTypeArticle, pub.calls[0].targetType)
	assert.Equal(t, "test-crawler", pub.calls[0].crawlerName)
}

func TestChainHandler_Handle_Category_중복URL제거(t *testing.T) {
	raw := htmlRaw("https://news.example.com/category", "<html></html>")
	chain := &stubChain{raw: raw}
	listParser := &stubListParser{items: []news.NewsItem{
		{URL: "https://news.example.com/1"},
		{URL: "https://news.example.com/1"}, // 중복
		{URL: "https://news.example.com/2"},
		{URL: ""}, // 빈 URL
	}}
	pub := &capturePublisher{}

	h := news.NewChainHandler(nil, chain, nil, listParser, pub, nil, newTestLogger())

	_, err := h.Handle(context.Background(), categoryJob("https://news.example.com/category"))

	require.NoError(t, err)
	require.Len(t, pub.calls, 1)
	assert.Equal(t, []string{"https://news.example.com/1", "https://news.example.com/2"}, pub.calls[0].urls)
}

func TestChainHandler_Handle_Category_빈목록_발행없음(t *testing.T) {
	raw := htmlRaw("https://news.example.com/category", "<html></html>")
	chain := &stubChain{raw: raw}
	listParser := &stubListParser{items: []news.NewsItem{}}
	pub := &capturePublisher{}

	h := news.NewChainHandler(nil, chain, nil, listParser, pub, nil, newTestLogger())

	contents, err := h.Handle(context.Background(), categoryJob("https://news.example.com/category"))

	require.NoError(t, err)
	assert.Nil(t, contents)
	assert.Empty(t, pub.calls)
}

// ─────────────────────────────────────────────────────────────────────────────
// 카테고리 경로 — 실패 전파 (silent drop 방지)
// ─────────────────────────────────────────────────────────────────────────────

func TestChainHandler_Handle_Category_ParseList실패_에러반환(t *testing.T) {
	// ParseList 실패 시 nil 반환(silent drop)이 아닌 에러 반환으로
	// worker의 재시도/DLQ 경로가 활성화되어야 합니다.
	raw := htmlRaw("https://news.example.com/category", "<html></html>")
	chain := &stubChain{raw: raw}
	parseErr := errors.New("selector not found")
	listParser := &stubListParser{err: parseErr}
	pub := &capturePublisher{}

	h := news.NewChainHandler(nil, chain, nil, listParser, pub, nil, newTestLogger())

	_, err := h.Handle(context.Background(), categoryJob("https://news.example.com/category"))

	assert.Error(t, err)
	assert.Empty(t, pub.calls)
}

func TestChainHandler_Handle_Category_Publish실패_에러반환(t *testing.T) {
	raw := htmlRaw("https://news.example.com/category", "<html></html>")
	chain := &stubChain{raw: raw}
	listParser := &stubListParser{items: []news.NewsItem{{URL: "https://news.example.com/1"}}}
	pub := &capturePublisher{err: errors.New("kafka unavailable")}

	h := news.NewChainHandler(nil, chain, nil, listParser, pub, nil, newTestLogger())

	_, err := h.Handle(context.Background(), categoryJob("https://news.example.com/category"))

	assert.Error(t, err)
}

// ─────────────────────────────────────────────────────────────────────────────
// 카테고리 경로 — nil 구성요소 (skip 의도 명시)
// ─────────────────────────────────────────────────────────────────────────────

func TestChainHandler_Handle_Category_ListParserNil_스킵(t *testing.T) {
	raw := htmlRaw("https://news.example.com/category", "<html></html>")
	chain := &stubChain{raw: raw}
	pub := &capturePublisher{}

	// listParser=nil → 체이닝 건너뜀, 에러 없음
	h := news.NewChainHandler(nil, chain, nil, nil, pub, nil, newTestLogger())

	contents, err := h.Handle(context.Background(), categoryJob("https://news.example.com/category"))

	require.NoError(t, err)
	assert.Nil(t, contents)
	assert.Empty(t, pub.calls)
}

func TestChainHandler_Handle_Category_PublisherNil_스킵(t *testing.T) {
	raw := htmlRaw("https://news.example.com/category", "<html></html>")
	chain := &stubChain{raw: raw}
	listParser := &stubListParser{items: []news.NewsItem{{URL: "https://news.example.com/1"}}}

	// publisher=nil → 체이닝 건너뜀, 에러 없음
	h := news.NewChainHandler(nil, chain, nil, listParser, nil, nil, newTestLogger())

	contents, err := h.Handle(context.Background(), categoryJob("https://news.example.com/category"))

	require.NoError(t, err)
	assert.Nil(t, contents)
}

// ─────────────────────────────────────────────────────────────────────────────
// 기사 경로
// ─────────────────────────────────────────────────────────────────────────────

func TestChainHandler_Handle_Article_파싱성공_Content반환(t *testing.T) {
	raw := htmlRaw("https://news.example.com/article/1", "<html><body>body</body></html>")
	chain := &stubChain{raw: raw}
	articleParser := &stubArticleParser{
		article: &news.NewsArticle{
			Title:       "Test Article",
			Body:        "Test body content",
			URL:         "https://news.example.com/article/1",
			PublishedAt: time.Now(),
		},
	}

	h := news.NewChainHandler(nil, chain, articleParser, nil, nil, nil, newTestLogger())

	contents, err := h.Handle(context.Background(), articleJob("https://news.example.com/article/1"))

	require.NoError(t, err)
	require.Len(t, contents, 1)
	assert.Equal(t, "Test Article", contents[0].Title)
}

func TestChainHandler_Handle_Article_파싱실패_nilContent반환(t *testing.T) {
	raw := htmlRaw("https://news.example.com/article/1", "<html></html>")
	chain := &stubChain{raw: raw}
	articleParser := &stubArticleParser{err: errors.New("missing title")}

	h := news.NewChainHandler(nil, chain, articleParser, nil, nil, nil, newTestLogger())

	contents, err := h.Handle(context.Background(), articleJob("https://news.example.com/article/1"))

	require.NoError(t, err) // 파싱 실패는 에러로 전파하지 않고 nil 반환
	assert.Nil(t, contents)
}

func TestChainHandler_Handle_Article_파서없음_nilContent반환(t *testing.T) {
	raw := htmlRaw("https://news.example.com/article/1", "<html></html>")
	chain := &stubChain{raw: raw}

	h := news.NewChainHandler(nil, chain, nil, nil, nil, nil, newTestLogger())

	contents, err := h.Handle(context.Background(), articleJob("https://news.example.com/article/1"))

	require.NoError(t, err)
	assert.Nil(t, contents)
}

// ─────────────────────────────────────────────────────────────────────────────
// 대량 URL 상한
// ─────────────────────────────────────────────────────────────────────────────

func TestChainHandler_Handle_Category_대량URL_상한적용(t *testing.T) {
	const total = 500
	items := make([]news.NewsItem, total)
	for i := range items {
		items[i] = news.NewsItem{URL: "https://example.com/" + string(rune('a'+i%26)) + string(rune('0'+i%10))}
	}
	// URL이 서로 다르도록 인덱스 포함
	for i := range items {
		items[i].URL = "https://example.com/article-" + string(rune('0'+i%10)) + string(rune('a'+i%26)) + "-" + "x"[0:1]
	}

	raw := htmlRaw("https://news.example.com/category", "<html></html>")
	chain := &stubChain{raw: raw}
	pub := &capturePublisher{}

	// 500개 중 일부는 중복될 수 있으므로 고유 URL로 재생성
	uniqueItems := make([]news.NewsItem, total)
	for i := range uniqueItems {
		uniqueItems[i] = news.NewsItem{URL: "https://example.com/article-" + itoa(i)}
	}
	listParser := &stubListParser{items: uniqueItems}

	h := news.NewChainHandler(nil, chain, nil, listParser, pub, nil, newTestLogger())

	_, err := h.Handle(context.Background(), categoryJob("https://news.example.com/category"))

	require.NoError(t, err)
	require.Len(t, pub.calls, 1)
	assert.LessOrEqual(t, len(pub.calls[0].urls), 200, "maxChainedURLs 상한 초과")
}

// itoa는 int를 string으로 변환합니다 (strconv 의존 없이).
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	b := make([]byte, 0, 10)
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
