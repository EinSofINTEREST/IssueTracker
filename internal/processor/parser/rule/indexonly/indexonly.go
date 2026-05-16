// Package indexonly 은 ParsePage 결과가 카테고리/링크 hub 페이지 (index-only)
// 처럼 보이는지 휴리스틱으로 판정합니다 (이슈 #468, sub-issue #476).
//
// 호출 측 (parser engine 또는 worker) 가 본 결과를 받아 parser_blacklist 자동
// 강등 등의 액션을 수행합니다 — 본 패키지는 pure-logic 만 담당.
//
// 판정 기준 (AND):
//
//   - 본문이 짧음            : Page.MainContent rune 길이가 MinBodyRunes 미만
//   - PublishedAt zero-value : 본문은 article 이 아닌 카테고리/목록
//   - 링크 hub 신호 (둘 중 하나):
//   - 링크 텍스트 비율이 MinLinkRatio 이상 — 페이지 텍스트의 대부분이 anchor
//   - article-like link 가 MinArticleLinks 이상 — 카테고리/목록 페이지에 흔한 패턴
//
// 조건 결합을 AND 로 잡아 false-positive 회피.
package indexonly

import (
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/PuerkitoBio/goquery"

	"issuetracker/internal/processor/parser/types"
)

// 기본 임계값. 호출자가 Config 로 override 하지 않으면 본 값 적용.
const (
	DefaultMinBodyRunes    = 200
	DefaultMinLinkRatio    = 0.8
	DefaultMinArticleLinks = 5
)

// Config 는 휴리스틱 임계값. zero 값은 default 로 보정.
type Config struct {
	MinBodyRunes    int     // Page.MainContent rune 길이 임계값
	MinLinkRatio    float64 // link 텍스트 비율 임계값 (0.0~1.0)
	MinArticleLinks int     // article-like link 수 임계값
}

// withDefaults 는 zero 필드를 default 로 채웁니다.
func (c Config) withDefaults() Config {
	if c.MinBodyRunes <= 0 {
		c.MinBodyRunes = DefaultMinBodyRunes
	}
	if c.MinLinkRatio <= 0 {
		c.MinLinkRatio = DefaultMinLinkRatio
	}
	if c.MinArticleLinks <= 0 {
		c.MinArticleLinks = DefaultMinArticleLinks
	}
	return c
}

// Score 는 각 휴리스틱 조건의 결과를 풀어 담습니다. 로그/디버깅 / metric label 용.
type Score struct {
	BodyRunes        int     // Page.MainContent rune 길이
	BodyShort        bool    // BodyRunes < cfg.MinBodyRunes
	NoPublishedAt    bool    // Page.PublishedAt.IsZero()
	LinkRatio        float64 // 페이지 전체 텍스트 대비 anchor 텍스트 비율 (0~1)
	HighLinkRatio    bool    // LinkRatio >= cfg.MinLinkRatio
	ArticleLinks     int     // article-like <a href> 수
	ManyArticleLinks bool    // ArticleLinks >= cfg.MinArticleLinks
}

// IsIndexOnly 는 page 가 index-only 페이지로 보이는지 판정합니다.
//
// 인자:
//   - page    : ParsePage 결과 (nil 이면 false + zero Score 반환 — defensive)
//   - rawHTML : 원본 HTML — anchor 비율 및 article-like link 수 계산에 사용. 빈 문자열이면 HTML 의존 신호는 false.
//   - cfg     : 임계값. zero 값은 default 사용.
//
// 반환:
//   - ok    : true 면 index-only. false 면 article 등 일반 페이지.
//   - score : 디버깅/로그 용 풀어쓴 판정 결과.
func IsIndexOnly(page *types.Page, rawHTML string, cfg Config) (bool, Score) {
	cfg = cfg.withDefaults()
	if page == nil {
		return false, Score{}
	}

	score := Score{
		BodyRunes:     utf8.RuneCountInString(page.MainContent),
		NoPublishedAt: page.PublishedAt.IsZero(),
	}
	score.BodyShort = score.BodyRunes < cfg.MinBodyRunes

	score.LinkRatio, score.ArticleLinks = analyzeAnchors(rawHTML, page.URL)
	score.HighLinkRatio = score.LinkRatio >= cfg.MinLinkRatio
	score.ManyArticleLinks = score.ArticleLinks >= cfg.MinArticleLinks

	// AND: 본문 부족 + 게시일 부재 + (링크 hub OR article-like link 다수)
	ok := score.BodyShort && score.NoPublishedAt && (score.HighLinkRatio || score.ManyArticleLinks)
	return ok, score
}

// analyzeAnchors 는 HTML 의 anchor 텍스트 비율과 article-like link 수를 계산합니다.
// rawHTML 이 비었거나 parse 실패면 (0, 0) 반환 — 호출자는 HTML 의존 신호를 모두 false 로 인식.
func analyzeAnchors(rawHTML, pageURL string) (linkRatio float64, articleLinks int) {
	rawHTML = strings.TrimSpace(rawHTML)
	if rawHTML == "" {
		return 0, 0
	}
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(rawHTML))
	if err != nil {
		return 0, 0
	}

	// 페이지 전체 텍스트는 <body> 의 텍스트로 측정 — html/head 부분은 제외해야 anchor 비율이 의미 있음.
	bodySel := doc.Find("body")
	if bodySel.Length() == 0 {
		bodySel = doc.Selection
	}
	totalLen := utf8.RuneCountInString(collapseWhitespace(bodySel.Text()))
	if totalLen == 0 {
		return 0, 0
	}

	// LinkRatio / ArticleLinks 모두 same-host anchor 만 카운트 — 본 휴리스틱의 의도가
	// "사이트 내부 hub 페이지" 탐지이므로 외부 도메인 anchor 는 양 신호 모두에서 제외.
	// 외부 광고 / "관련 사이트" 영역이 ratio 를 부풀리는 false-positive 차단.
	// href trim / prefix 검사 / url.Parse 는 각 anchor 당 한 번만 수행하고 결과 *url.URL 을
	// 두 helper 에 전달 — 중복 파싱 회피 (gemini PR #478 피드백).
	pageHost := hostOf(pageURL)
	var linkTextLen int
	seen := make(map[string]struct{})
	bodySel.Find("a[href]").Each(func(_ int, a *goquery.Selection) {
		href, ok := a.Attr("href")
		if !ok {
			return
		}
		href = strings.TrimSpace(href)
		if href == "" || strings.HasPrefix(href, "#") || strings.HasPrefix(href, "javascript:") || strings.HasPrefix(href, "mailto:") {
			return
		}
		u, err := url.Parse(href)
		if err != nil {
			return
		}
		if !isSameHostOrRelative(u, pageHost) {
			return
		}
		linkTextLen += utf8.RuneCountInString(collapseWhitespace(a.Text()))
		if !isArticleLike(u, pageHost) {
			return
		}
		// 중복 href 는 한 번만 카운트 — 카테고리 페이지의 "더보기" 등 반복 anchor 보정.
		if _, dup := seen[href]; dup {
			return
		}
		seen[href] = struct{}{}
		articleLinks++
	})
	if linkTextLen > totalLen {
		// 안전장치 — 중첩 anchor 등 비정상 케이스에서 비율이 1 을 넘지 않도록 clamp.
		linkTextLen = totalLen
	}
	linkRatio = float64(linkTextLen) / float64(totalLen)
	return linkRatio, articleLinks
}

// isSameHostOrRelative 는 *url.URL 이 same-host (절대 URL) 이거나 상대 URL 인지 판정합니다.
// 외부 도메인 anchor 를 LinkRatio 계산에서 제외하기 위한 사전 필터.
func isSameHostOrRelative(u *url.URL, pageHost string) bool {
	if u.Host == "" {
		return true // 상대 URL — same-host 로 간주
	}
	return pageHost != "" && strings.EqualFold(u.Host, pageHost)
}

// isArticleLike 는 *url.URL 이 article 후보로 보이는지 판정합니다.
//
// 기준:
//   - 절대 URL 이면 host 가 pageHost 와 동일해야 함 (외부 도메인 제외)
//   - 상대 URL 도 허용 (대다수 카테고리 페이지 내부 link)
//   - path 가 "/" 만 있거나 비어있으면 제외
//   - path 가 최소 1 개의 non-empty segment 를 가져야 함
//
// u.Path 는 url.Parse 가 query 를 RawQuery 로 분리한 뒤의 결과 — query 영향 자동 제거.
func isArticleLike(u *url.URL, pageHost string) bool {
	if u.Host != "" && pageHost != "" && !strings.EqualFold(u.Host, pageHost) {
		return false
	}
	// strings.Trim 은 앞뒤 "/" 를 모두 제거 — 한 개라도 non-empty segment 가 있으면 결과 non-empty.
	return strings.Trim(strings.TrimSpace(u.Path), "/") != ""
}

// hostOf 는 URL 의 host 만 추출합니다 (parse 실패 시 빈 문자열).
func hostOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return u.Host
}

// collapseWhitespace 는 연속된 공백 문자열을 단일 공백으로 변환합니다.
// goquery.Text() 가 들여쓰기/줄바꿈 포함 텍스트를 반환하므로 텍스트 길이 측정의 노이즈를 제거.
//
// unicode.IsSpace 사용 — non-breaking space (U+00A0), ideographic space (U+3000) 등 다양한
// 유니코드 공백을 정확히 흡수 (i18n 컨텐츠 길이 측정 정확도).
func collapseWhitespace(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	prevSpace := true // leading whitespace 무시
	for _, r := range s {
		if unicode.IsSpace(r) {
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
			continue
		}
		b.WriteRune(r)
		prevSpace = false
	}
	out := b.String()
	return strings.TrimRight(out, " ")
}
