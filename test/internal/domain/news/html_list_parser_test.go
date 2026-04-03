package news_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/crawler/core"
	"issuetracker/internal/crawler/domain/news"
	"issuetracker/pkg/links"
)

// ─────────────────────────────────────────────────────────────────────────────
// mock LinkExtractor — 인터페이스 주입 테스트용
// ─────────────────────────────────────────────────────────────────────────────

type mockLinkExtractor struct {
	result []core.Link
	err    error
}

func (m *mockLinkExtractor) Extract(_ *core.RawContent) ([]core.Link, error) {
	return m.result, m.err
}

func rawHTMLContent(pageURL, html string) *core.RawContent {
	return &core.RawContent{
		URL:      pageURL,
		HTML:     html,
		Headers:  map[string]string{},
		Metadata: map[string]interface{}{},
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// NewHTMLListParser — 인터페이스 주입 경로
// ─────────────────────────────────────────────────────────────────────────────

func TestHTMLListParser_NewHTMLListParser_mock주입_결과변환(t *testing.T) {
	mock := &mockLinkExtractor{result: []core.Link{
		{URL: "https://news.example.com/1", Text: "기사 1"},
		{URL: "https://news.example.com/2", Text: "기사 2"},
	}}

	p := news.NewHTMLListParser(mock)
	items, err := p.ParseList(rawHTMLContent("https://news.example.com/category", "<html></html>"))

	require.NoError(t, err)
	require.Len(t, items, 2)
	assert.Equal(t, "https://news.example.com/1", items[0].URL)
	assert.Equal(t, "기사 1", items[0].Title)
}

func TestHTMLListParser_NewHTMLListParser_extractor실패_에러반환(t *testing.T) {
	mock := &mockLinkExtractor{err: errors.New("fetch error")}

	p := news.NewHTMLListParser(mock)
	_, err := p.ParseList(rawHTMLContent("https://news.example.com/category", "<html></html>"))

	assert.Error(t, err)
}

func TestHTMLListParser_NewHTMLListParser_빈결과_nil반환(t *testing.T) {
	mock := &mockLinkExtractor{result: nil}

	p := news.NewHTMLListParser(mock)
	items, err := p.ParseList(rawHTMLContent("https://news.example.com/category", "<html></html>"))

	require.NoError(t, err)
	assert.Nil(t, items)
}

// ─────────────────────────────────────────────────────────────────────────────
// NewDefaultHTMLListParser — HTMLLinkExtractor 기본 구현 경로
// ─────────────────────────────────────────────────────────────────────────────

func TestHTMLListParser_Default_동일오리진링크추출(t *testing.T) {
	html := `<html><body>
		<a href="/article/1">기사 1</a>
		<a href="/article/2">기사 2</a>
		<a href="https://other.com/article/3">외부 링크</a>
	</body></html>`

	p := news.NewDefaultHTMLListParser("https://news.example.com")
	items, err := p.ParseList(rawHTMLContent("https://news.example.com/category", html))

	require.NoError(t, err)
	urls := toURLs(items)
	assert.Contains(t, urls, "https://news.example.com/article/1")
	assert.Contains(t, urls, "https://news.example.com/article/2")
	assert.NotContains(t, urls, "https://other.com/article/3")
}

func TestHTMLListParser_Default_scheme불일치_제외(t *testing.T) {
	html := `<html><body>
		<a href="https://news.example.com/article/1">HTTPS 기사</a>
		<a href="http://news.example.com/article/2">HTTP 기사 (scheme 불일치)</a>
	</body></html>`

	p := news.NewDefaultHTMLListParser("https://news.example.com")
	items, err := p.ParseList(rawHTMLContent("https://news.example.com/category", html))

	require.NoError(t, err)
	urls := toURLs(items)
	assert.Contains(t, urls, "https://news.example.com/article/1")
	assert.NotContains(t, urls, "http://news.example.com/article/2")
}

func TestHTMLListParser_Default_상대경로절대URL변환(t *testing.T) {
	html := `<html><body>
		<a href="/news/article/100">상대경로 기사</a>
	</body></html>`

	p := news.NewDefaultHTMLListParser("https://news.example.com")
	items, err := p.ParseList(rawHTMLContent("https://news.example.com/category/", html))

	require.NoError(t, err)
	assert.Contains(t, toURLs(items), "https://news.example.com/news/article/100")
}

func TestHTMLListParser_Default_중복URL제거(t *testing.T) {
	html := `<html><body>
		<a href="/article/1">기사 1</a>
		<a href="/article/1">기사 1 중복</a>
		<a href="/article/2">기사 2</a>
	</body></html>`

	p := news.NewDefaultHTMLListParser("https://news.example.com")
	items, err := p.ParseList(rawHTMLContent("https://news.example.com/category", html))

	require.NoError(t, err)
	assert.Len(t, items, 2)
}

func TestHTMLListParser_Default_기본제외패턴적용(t *testing.T) {
	html := `<html><body>
		<a href="/article/1">기사</a>
		<a href="/login">로그인</a>
		<a href="/share/article/1">공유</a>
		<a href="/rss/feed">RSS</a>
		<a href="javascript:void(0)">JS</a>
		<a href="mailto:info@example.com">메일</a>
	</body></html>`

	p := news.NewDefaultHTMLListParser("https://news.example.com")
	items, err := p.ParseList(rawHTMLContent("https://news.example.com/category", html))

	require.NoError(t, err)
	urls := toURLs(items)
	assert.Contains(t, urls, "https://news.example.com/article/1")
	assert.NotContains(t, urls, "https://news.example.com/login")
	assert.NotContains(t, urls, "https://news.example.com/share/article/1")
	assert.NotContains(t, urls, "https://news.example.com/rss/feed")
}

func TestHTMLListParser_Default_fragment제거(t *testing.T) {
	html := `<html><body>
		<a href="/article/1#comments">기사 (앵커)</a>
		<a href="/article/1">기사</a>
	</body></html>`

	p := news.NewDefaultHTMLListParser("https://news.example.com")
	items, err := p.ParseList(rawHTMLContent("https://news.example.com/category", html))

	require.NoError(t, err)
	assert.Len(t, items, 1)
	assert.Equal(t, "https://news.example.com/article/1", items[0].URL)
}

func TestHTMLListParser_Default_링크없음_nil반환(t *testing.T) {
	p := news.NewDefaultHTMLListParser("https://news.example.com")
	items, err := p.ParseList(rawHTMLContent("https://news.example.com/category", "<html><body><p>링크 없음</p></body></html>"))

	require.NoError(t, err)
	assert.Nil(t, items)
}

func TestHTMLListParser_Default_PathPrefixes옵션(t *testing.T) {
	html := `<html><body>
		<a href="/article/1">기사</a>
		<a href="/video/1">비디오</a>
		<a href="/opinion/1">오피니언</a>
	</body></html>`

	p := news.NewDefaultHTMLListParser("https://news.example.com",
		links.WithPathPrefixes("/article/", "/opinion/"),
	)
	items, err := p.ParseList(rawHTMLContent("https://news.example.com/category", html))

	require.NoError(t, err)
	urls := toURLs(items)
	assert.Contains(t, urls, "https://news.example.com/article/1")
	assert.Contains(t, urls, "https://news.example.com/opinion/1")
	assert.NotContains(t, urls, "https://news.example.com/video/1")
}

func TestHTMLListParser_Default_ExcludePatterns옵션(t *testing.T) {
	html := `<html><body>
		<a href="/article/1">기사</a>
		<a href="/article/1?utm_source=ad">광고</a>
		<a href="/sponsored/1">스폰서</a>
	</body></html>`

	p := news.NewDefaultHTMLListParser("https://news.example.com",
		links.WithExcludePatterns("utm_source", "sponsored"),
	)
	items, err := p.ParseList(rawHTMLContent("https://news.example.com/category", html))

	require.NoError(t, err)
	urls := toURLs(items)
	assert.Contains(t, urls, "https://news.example.com/article/1")
	assert.NotContains(t, urls, "https://news.example.com/article/1?utm_source=ad")
	assert.NotContains(t, urls, "https://news.example.com/sponsored/1")
}

func TestHTMLListParser_Default_MaxLinks옵션_상한적용(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("<html><body>")
	for i := 0; i < 100; i++ {
		sb.WriteString(`<a href="/article/` + itoa3(i) + `">기사</a>`)
	}
	sb.WriteString("</body></html>")

	p := news.NewDefaultHTMLListParser("https://news.example.com",
		links.WithMaxLinks(10),
	)
	items, err := p.ParseList(rawHTMLContent("https://news.example.com/category", sb.String()))

	require.NoError(t, err)
	assert.LessOrEqual(t, len(items), 10)
}

// ─────────────────────────────────────────────────────────────────────────────
// 런타임 wiring 통합 테스트
// HTMLListParser(+HTMLLinkExtractor)가 ChainHandler에 주입되어 동작함을 검증합니다.
// ─────────────────────────────────────────────────────────────────────────────

func TestHTMLListParser_ChainHandler통합_카테고리URL추출(t *testing.T) {
	categoryHTML := `<html><body>
		<a href="/article/100">뉴스 기사 1</a>
		<a href="/article/200">뉴스 기사 2</a>
		<a href="https://external.com/news">외부 링크</a>
	</body></html>`

	chain := &stubChain{raw: &core.RawContent{
		URL:      "https://new-source.com/category",
		HTML:     categoryHTML,
		Headers:  map[string]string{},
		Metadata: map[string]interface{}{},
	}}

	listParser := news.NewDefaultHTMLListParser("https://new-source.com",
		links.WithPathPrefixes("/article/"),
	)
	pub := &capturePublisher{}

	h := news.NewChainHandler(nil, chain, nil, listParser, pub, nil, newTestLogger())

	job := &core.CrawlJob{
		ID:          "job-1",
		CrawlerName: "new-source",
		Target: core.Target{
			URL:  "https://new-source.com/category",
			Type: core.TargetTypeCategory,
		},
	}

	contents, err := h.Handle(context.Background(), job)

	require.NoError(t, err)
	assert.Nil(t, contents)
	require.Len(t, pub.calls, 1)
	assert.Equal(t, core.TargetTypeArticle, pub.calls[0].targetType)
	assert.Equal(t, "new-source", pub.calls[0].crawlerName)
	assert.Len(t, pub.calls[0].urls, 2)
	assert.Contains(t, pub.calls[0].urls, "https://new-source.com/article/100")
	assert.Contains(t, pub.calls[0].urls, "https://new-source.com/article/200")
}

// ─────────────────────────────────────────────────────────────────────────────
// 헬퍼
// ─────────────────────────────────────────────────────────────────────────────

func toURLs(items []news.NewsItem) []string {
	urls := make([]string, len(items))
	for i, item := range items {
		urls[i] = item.URL
	}
	return urls
}

func itoa3(n int) string {
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
