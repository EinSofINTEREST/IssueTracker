// Package links는 HTML 페이지에서 하이퍼링크를 추출하는 범용 유틸리티를 제공합니다.
//
// Package links provides a generic utility for extracting hyperlinks from HTML pages.
// It has no dependency on any crawler domain and can be used in any context.
package links

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

// Link는 HTML 페이지에서 추출된 단일 하이퍼링크입니다.
//
// Link represents a single hyperlink extracted from an HTML page.
type Link struct {
	URL  string // 절대 URL
	Text string // <a> 태그의 텍스트 콘텐츠
}

// Extractor는 HTML 문자열에서 링크를 추출합니다.
// 동일 호스트 필터, 경로 접두사 필터, 제외 패턴을 조합하여 사용합니다.
//
// Extractor extracts links from an HTML string.
// Filters can be combined: same-origin, path prefix allowlist, and exclude patterns.
type Extractor struct {
	// baseURL은 상대 경로를 절대 URL로 변환하는 기준입니다.
	baseURL string

	// sameOriginOnly가 true이면 scheme+host가 모두 일치하는 링크만 포함합니다.
	sameOriginOnly bool

	// pathPrefixes가 설정되면 이 접두사 중 하나와 일치하는 경로만 포함합니다.
	// 빈 슬라이스면 모든 경로를 허용합니다.
	pathPrefixes []string

	// excludePatternsLower는 제외 패턴을 미리 소문자로 캐시한 슬라이스입니다.
	// shouldExclude 호출마다 ToLower를 반복하지 않습니다.
	excludePatternsLower []string

	// maxLinks는 Extract가 반환하는 최대 링크 수입니다.
	// 0이면 제한 없음.
	maxLinks int
}

// Option은 Extractor 생성 옵션입니다.
type Option func(*Extractor)

// WithSameOriginOnly는 baseURL과 scheme+host가 모두 일치하는 링크만 포함하도록 설정합니다.
// http/https 혼재나 비표준 포트 링크를 다른 오리진으로 정확하게 구분합니다.
func WithSameOriginOnly() Option {
	return func(e *Extractor) {
		e.sameOriginOnly = true
	}
}

// WithSameHostOnly는 WithSameOriginOnly의 별칭입니다.
// 기존 코드와의 호환성을 위해 제공합니다.
func WithSameHostOnly() Option {
	return WithSameOriginOnly()
}

// WithPathPrefixes는 허용할 URL 경로 접두사를 설정합니다.
// 예: "/article/", "/news/" → 해당 접두사로 시작하는 경로만 포함합니다.
func WithPathPrefixes(prefixes ...string) Option {
	return func(e *Extractor) {
		// 호출자 슬라이스 변경으로부터 독립적인 복사본 보관
		e.pathPrefixes = append([]string{}, prefixes...)
	}
}

// WithExcludePatterns는 기본 제외 패턴에 추가할 패턴을 설정합니다.
// 대소문자를 구분하지 않습니다.
// 예: "utm_source", "sponsored" → 해당 문자열이 포함된 URL을 제외합니다.
func WithExcludePatterns(patterns ...string) Option {
	return func(e *Extractor) {
		for _, p := range patterns {
			e.excludePatternsLower = append(e.excludePatternsLower, strings.ToLower(p))
		}
	}
}

// WithMaxLinks는 Extract가 반환하는 최대 링크 수를 설정합니다.
// 대용량 페이지에서 메모리 사용과 후속 발행량을 제한합니다.
// 0 이하면 제한 없음.
func WithMaxLinks(n int) Option {
	return func(e *Extractor) {
		if n > 0 {
			e.maxLinks = n
		}
	}
}

// defaultExcludePatterns는 모든 Extractor에 기본 적용되는 소문자 제외 패턴입니다.
var defaultExcludePatterns = []string{
	"javascript:",
	"mailto:",
	"tel:",
	"/login",
	"/signup",
	"/register",
	"/share",
	"/print",
	"/rss",
}

// NewExtractor는 새로운 Extractor를 생성합니다.
// baseURL은 상대 경로 해석과 동일 오리진 필터링에 사용됩니다.
// WithSameOriginOnly 옵션 없이는 외부 오리진 링크도 포함됩니다.
func NewExtractor(baseURL string, opts ...Option) *Extractor {
	// 기본 제외 패턴은 이미 소문자이므로 그대로 복사
	defaultLower := append([]string{}, defaultExcludePatterns...)

	e := &Extractor{
		baseURL:              baseURL,
		sameOriginOnly:       false,
		excludePatternsLower: defaultLower,
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// Extract는 html 문자열과 pageURL에서 링크를 추출합니다.
// pageURL은 상대 경로 해석 기준으로 사용됩니다. 빈 문자열이면 baseURL을 사용합니다.
// 반환된 링크는 중복이 제거되고 fragment(#...)가 제거된 절대 URL입니다.
// maxLinks가 설정된 경우 그 수를 초과하지 않습니다.
func (e *Extractor) Extract(html, pageURL string) ([]Link, error) {
	if html == "" {
		return nil, nil
	}

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return nil, fmt.Errorf("parse html: %w", err)
	}

	base, err := e.resolveBase(pageURL)
	if err != nil {
		return nil, fmt.Errorf("resolve base url: %w", err)
	}

	seen := make(map[string]struct{})
	var result []Link

	doc.Find("a[href]").EachWithBreak(func(_ int, s *goquery.Selection) bool {
		if e.maxLinks > 0 && len(result) >= e.maxLinks {
			return false
		}

		href, _ := s.Attr("href")
		href = strings.TrimSpace(href)
		if href == "" {
			return true
		}

		abs, ok := e.toAbsolute(href, base)
		if !ok {
			return true
		}

		if e.shouldExclude(abs) {
			return true
		}

		if e.sameOriginOnly && base != nil && !e.sameOrigin(abs, base) {
			return true
		}

		if !e.matchesPathPrefix(abs) {
			return true
		}

		if _, dup := seen[abs]; dup {
			return true
		}
		seen[abs] = struct{}{}

		result = append(result, Link{
			URL:  abs,
			Text: strings.TrimSpace(s.Text()),
		})
		return true
	})

	return result, nil
}

// resolveBase는 pageURL 또는 baseURL을 파싱하여 *url.URL을 반환합니다.
func (e *Extractor) resolveBase(pageURL string) (*url.URL, error) {
	src := pageURL
	if src == "" {
		src = e.baseURL
	}
	if src == "" {
		return nil, nil
	}
	return url.Parse(src)
}

// toAbsolute는 href를 절대 URL 문자열로 변환합니다.
// http/https 스킴이 아니거나 변환에 실패하면 (_, false)를 반환합니다.
// fragment는 제거합니다.
func (e *Extractor) toAbsolute(href string, base *url.URL) (string, bool) {
	ref, err := url.Parse(href)
	if err != nil {
		return "", false
	}

	var abs *url.URL
	if base != nil {
		abs = base.ResolveReference(ref)
	} else {
		abs = ref
	}

	if abs.Scheme != "http" && abs.Scheme != "https" {
		return "", false
	}

	abs.Fragment = ""
	return abs.String(), true
}

// sameOrigin은 target이 base와 scheme+host(포트 포함)가 모두 일치하는지 확인합니다.
// hostname만 비교하던 이전 방식과 달리 http/https 혼재, 비표준 포트를 다른 오리진으로 구분합니다.
func (e *Extractor) sameOrigin(target string, base *url.URL) bool {
	u, err := url.Parse(target)
	if err != nil {
		return false
	}
	return u.Scheme == base.Scheme && u.Host == base.Host
}

// shouldExclude는 URL에 제외 패턴이 포함되어 있으면 true를 반환합니다.
// 패턴은 생성 시점에 소문자로 캐시되므로 매 호출마다 ToLower를 수행하지 않습니다.
func (e *Extractor) shouldExclude(absURL string) bool {
	lower := strings.ToLower(absURL)
	for _, pat := range e.excludePatternsLower {
		if strings.Contains(lower, pat) {
			return true
		}
	}
	return false
}

// matchesPathPrefix는 pathPrefixes가 설정된 경우 URL 경로가 그 중 하나로 시작하는지 확인합니다.
// pathPrefixes가 비어 있으면 항상 true를 반환합니다.
func (e *Extractor) matchesPathPrefix(absURL string) bool {
	if len(e.pathPrefixes) == 0 {
		return true
	}
	u, err := url.Parse(absURL)
	if err != nil {
		return false
	}
	for _, prefix := range e.pathPrefixes {
		if strings.HasPrefix(u.Path, prefix) {
			return true
		}
	}
	return false
}
