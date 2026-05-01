package core

import (
	"issuetracker/pkg/links"
)

// Link는 HTML 페이지에서 추출된 하이퍼링크입니다.
//
// Link represents a hyperlink extracted from an HTML page.
type Link struct {
	URL  string // 절대 URL
	Text string // <a> 태그의 텍스트 콘텐츠
}

// LinkExtractor는 RawContent에서 링크 목록을 추출하는 인터페이스입니다.
// 뉴스·커뮤니티·소셜 등 모든 도메인 목록 파서가 이 인터페이스에 의존할 수 있습니다.
//
// LinkExtractor extracts a list of links from a RawContent.
// Any domain list parser (news, community, social) may depend on this interface.
type LinkExtractor interface {
	Extract(raw *RawContent) ([]Link, error)
}

// HTMLLinkExtractor는 pkg/links.Extractor를 LinkExtractor 인터페이스로 감싸는
// 기본 구현체입니다. CSS 셀렉터 없이 HTML의 <a href> 태그에서 링크를 추출합니다.
//
// HTMLLinkExtractor is the default LinkExtractor implementation backed by pkg/links.Extractor.
// It extracts links from <a href> tags without requiring source-specific CSS selectors.
type HTMLLinkExtractor struct {
	inner *links.Extractor
}

// NewHTMLLinkExtractor는 새로운 HTMLLinkExtractor를 생성합니다.
// baseURL은 상대 경로 해석과 동일 오리진 필터링에 사용됩니다.
// 옵션은 pkg/links.Option을 그대로 사용합니다.
func NewHTMLLinkExtractor(baseURL string, opts ...links.Option) *HTMLLinkExtractor {
	return &HTMLLinkExtractor{
		inner: links.NewExtractor(baseURL, opts...),
	}
}

// Extract는 raw.HTML에서 링크를 추출하여 []Link로 반환합니다.
// raw가 nil이면 에러를 반환합니다.
//
// pkg/links 는 generic 유틸 패키지로 fmt.Errorf 를 유지하고,
// 본 boundary 에서 CrawlerError 로 변환합니다(.claude/rules/04-error-handling.md 참고).
func (e *HTMLLinkExtractor) Extract(raw *RawContent) ([]Link, error) {
	if raw == nil {
		return nil, NewInternalError(CodeInternal, "raw content is nil", nil)
	}

	extracted, err := e.inner.Extract(raw.HTML, raw.URL)
	if err != nil {
		return nil, NewParseError(CodeParseHTML, "failed to extract links", raw.URL, err)
	}

	if len(extracted) == 0 {
		return nil, nil
	}

	result := make([]Link, len(extracted))
	for i, l := range extracted {
		result[i] = Link{URL: l.URL, Text: l.Text}
	}
	return result, nil
}
