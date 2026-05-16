package indexonly_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"issuetracker/internal/processor/parser/rule/indexonly"
	"issuetracker/internal/processor/parser/types"
)

// TestIsIndexOnly_Defaults_PositiveCases 는 카테고리/링크 hub 페이지가 index-only 로 분류되는지 검증합니다.
func TestIsIndexOnly_Defaults_PositiveCases(t *testing.T) {
	tests := []struct {
		name string
		page *types.Page
		html string
	}{
		{
			name: "daum 카테고리 — 짧은 본문 + zero published + 다수 article-like link",
			page: &types.Page{
				URL:         "https://news.daum.net/breakingnews/politics",
				Title:       "정치",
				MainContent: "정치 뉴스",
				// PublishedAt zero
			},
			html: `<html><body>
				<a href="/news/article-1">기사 1 제목 가나다라마바사</a>
				<a href="/news/article-2">기사 2 제목 아자차카타파하</a>
				<a href="/news/article-3">기사 3 제목 일이삼사오육칠</a>
				<a href="/news/article-4">기사 4 제목 가나다라마바사</a>
				<a href="/news/article-5">기사 5 제목 가나다라마바사</a>
				<a href="/news/article-6">기사 6 제목 가나다라마바사</a>
			</body></html>`,
		},
		{
			name: "링크-허브 — 본문 사실상 anchor 텍스트뿐 → 높은 LinkRatio",
			page: &types.Page{
				URL:         "https://example.com/section/tech",
				Title:       "Tech",
				MainContent: "Tech",
				// PublishedAt zero
			},
			html: `<html><body>
				<nav>
					<a href="/tech/ai">AI 인공지능 관련 최신 기사 모음</a>
					<a href="/tech/cloud">클라우드 기술 동향 모음</a>
					<a href="/tech/security">보안 이슈 정리 모음</a>
					<a href="/tech/devops">DevOps 관련 글 모음</a>
				</nav>
			</body></html>`,
		},
		{
			name: "본문 zero + PublishedAt zero + 다수 article-like (절대 URL 동일 호스트)",
			page: &types.Page{
				URL:         "https://news.daum.net/breakingnews",
				Title:       "Breaking",
				MainContent: "",
			},
			html: `<html><body>
				<a href="https://news.daum.net/article/1">제목 1</a>
				<a href="https://news.daum.net/article/2">제목 2</a>
				<a href="https://news.daum.net/article/3">제목 3</a>
				<a href="https://news.daum.net/article/4">제목 4</a>
				<a href="https://news.daum.net/article/5">제목 5</a>
				<a href="https://news.daum.net/article/6">제목 6</a>
				<a href="https://news.daum.net/article/7">제목 7</a>
			</body></html>`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ok, score := indexonly.IsIndexOnly(tt.page, tt.html, indexonly.Config{})
			assert.True(t, ok, "expected index-only, score=%+v", score)
		})
	}
}

// TestIsIndexOnly_Defaults_NegativeCases 는 정상 article 페이지가 index-only 로 잘못 분류되지 않는지 검증합니다.
func TestIsIndexOnly_Defaults_NegativeCases(t *testing.T) {
	longBody := ""
	for i := 0; i < 50; i++ {
		longBody += "이것은 정상적인 기사 본문이며 충분한 길이를 가집니다. "
	}

	tests := []struct {
		name string
		page *types.Page
		html string
	}{
		{
			name: "정상 article — 긴 본문 + PublishedAt set",
			page: &types.Page{
				URL:         "https://news.daum.net/article/1234",
				Title:       "정상 기사 제목",
				MainContent: longBody,
				PublishedAt: time.Date(2026, 5, 16, 10, 0, 0, 0, time.UTC),
			},
			html: `<html><body><article>` + longBody + `</article></body></html>`,
		},
		{
			name: "긴 본문 + zero PublishedAt — published 부재만으로는 미달 (article 외 정적 페이지 보호)",
			page: &types.Page{
				URL:         "https://example.com/page",
				Title:       "정적 페이지",
				MainContent: longBody,
				// PublishedAt zero — 그러나 본문 충분히 길어 index-only 아님
			},
			html: `<html><body>` + longBody + `</body></html>`,
		},
		{
			name: "짧은 본문 + PublishedAt set — 게시일 있으면 article 로 신뢰",
			page: &types.Page{
				URL:         "https://example.com/short",
				Title:       "짧은 글",
				MainContent: "짧은 글 본문.",
				PublishedAt: time.Date(2026, 5, 16, 10, 0, 0, 0, time.UTC),
			},
			html: `<html><body>짧은 글 본문.<a href="/x">link</a></body></html>`,
		},
		{
			name: "짧은 본문 + zero PublishedAt — link 신호 없으면 index-only 아님",
			page: &types.Page{
				URL:         "https://example.com/blank",
				Title:       "Blank",
				MainContent: "짧음.",
			},
			html: `<html><body>짧음.</body></html>`,
		},
		{
			name: "외부 도메인 link 만 있는 경우 — article-like 카운트 0",
			page: &types.Page{
				URL:         "https://example.com/page",
				Title:       "Page",
				MainContent: "짧음.",
			},
			html: `<html><body>
				<a href="https://other.com/article/1">other 1</a>
				<a href="https://other.com/article/2">other 2</a>
				<a href="https://other.com/article/3">other 3</a>
				<a href="https://other.com/article/4">other 4</a>
				<a href="https://other.com/article/5">other 5</a>
				<a href="https://other.com/article/6">other 6</a>
				짧음.
			</body></html>`,
		},
		{
			name: "anchor 텍스트 비율 낮음 — 본문 위주",
			page: &types.Page{
				URL:         "https://example.com/x",
				Title:       "X",
				MainContent: "짧음.",
			},
			html: `<html><body>` + longBody + `<a href="/x">한줄</a></body></html>`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ok, score := indexonly.IsIndexOnly(tt.page, tt.html, indexonly.Config{})
			assert.False(t, ok, "expected NOT index-only, score=%+v", score)
		})
	}
}

// TestIsIndexOnly_NilPage 는 nil page 입력 시 false + zero Score 반환을 검증합니다.
func TestIsIndexOnly_NilPage(t *testing.T) {
	ok, score := indexonly.IsIndexOnly(nil, "", indexonly.Config{})
	assert.False(t, ok)
	assert.Equal(t, indexonly.Score{}, score)
}

// TestIsIndexOnly_EmptyHTML 는 rawHTML 이 비어있으면 HTML 의존 신호가 모두 false 인지 검증합니다.
func TestIsIndexOnly_EmptyHTML(t *testing.T) {
	page := &types.Page{
		URL:         "https://example.com/empty",
		Title:       "Empty",
		MainContent: "짧음.",
	}
	ok, score := indexonly.IsIndexOnly(page, "", indexonly.Config{})
	assert.False(t, ok, "no HTML → 링크 신호 모두 false → AND 미충족")
	assert.False(t, score.HighLinkRatio)
	assert.False(t, score.ManyArticleLinks)
	assert.Equal(t, 0, score.ArticleLinks)
}

// TestIsIndexOnly_CustomConfig 는 임계값 override 가 동작하는지 검증합니다.
func TestIsIndexOnly_CustomConfig(t *testing.T) {
	page := &types.Page{
		URL:         "https://example.com/page",
		Title:       "Page",
		MainContent: "100자 이내의 본문입니다.",
	}
	// 비-anchor 텍스트 충분히 많아서 LinkRatio 가 default 0.8 미만 → HighLinkRatio false.
	// 그리고 article-like link 가 2 개라 default MinArticleLinks=5 미만.
	html := `<html><body>
		이 페이지는 비교적 짧지만 anchor 만으로 구성되어 있지 않은 일반 페이지입니다.
		본문 텍스트가 anchor 텍스트보다 훨씬 많아서 LinkRatio 가 충분히 낮습니다.
		article-like link 는 두 개뿐이라 default MinArticleLinks 임계값 (5) 미만입니다.
		<a href="/a/1">link 1</a>
		<a href="/a/2">link 2</a>
	</body></html>`

	// default 임계값 (MinArticleLinks=5, MinLinkRatio=0.8) 이면 false
	ok, score := indexonly.IsIndexOnly(page, html, indexonly.Config{})
	assert.False(t, ok, "default 임계값에서 false 기대. score=%+v", score)

	// 임계값을 2 로 낮추면 true
	ok2, score2 := indexonly.IsIndexOnly(page, html, indexonly.Config{MinArticleLinks: 2})
	assert.True(t, ok2, "score=%+v", score2)
}

// TestScoreFields 는 Score 구조체가 디버깅 가능한 모든 필드를 채우는지 검증합니다.
func TestScoreFields(t *testing.T) {
	page := &types.Page{
		URL:         "https://example.com/cat",
		Title:       "카테고리",
		MainContent: "짧음.",
	}
	html := `<html><body>
		<a href="/article/1">article 1 제목입니다</a>
		<a href="/article/2">article 2 제목입니다</a>
		<a href="/article/3">article 3 제목입니다</a>
		<a href="/article/4">article 4 제목입니다</a>
		<a href="/article/5">article 5 제목입니다</a>
		<a href="/article/6">article 6 제목입니다</a>
	</body></html>`

	ok, score := indexonly.IsIndexOnly(page, html, indexonly.Config{})
	assert.True(t, ok)
	assert.Greater(t, score.BodyRunes, 0, "BodyRunes 측정")
	assert.True(t, score.BodyShort)
	assert.True(t, score.NoPublishedAt)
	assert.GreaterOrEqual(t, score.LinkRatio, 0.0)
	assert.LessOrEqual(t, score.LinkRatio, 1.0)
	assert.Equal(t, 6, score.ArticleLinks)
	assert.True(t, score.ManyArticleLinks)
}
