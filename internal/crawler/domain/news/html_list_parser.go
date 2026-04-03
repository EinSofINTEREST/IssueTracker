package news

import (
	"issuetracker/internal/crawler/core"
	"issuetracker/pkg/links"
)

// HTMLListParser는 core.LinkExtractor를 news.NewsListParser 인터페이스로 어댑팅합니다.
// core.LinkExtractor 구현체를 주입받으므로 추출 전략을 교체하거나 mock으로 대체하기 쉽습니다.
//
// HTMLListParser adapts a core.LinkExtractor to the news.NewsListParser interface.
// The extractor is injected, making it easy to swap strategies or substitute mocks.
type HTMLListParser struct {
	extractor core.LinkExtractor
}

// NewHTMLListParser는 core.LinkExtractor를 주입받아 HTMLListParser를 생성합니다.
// 테스트에서 mock LinkExtractor를 주입하거나, 소스별 커스텀 추출기를 연결할 때 사용합니다.
func NewHTMLListParser(extractor core.LinkExtractor) *HTMLListParser {
	return &HTMLListParser{extractor: extractor}
}

// NewDefaultHTMLListParser는 core.HTMLLinkExtractor를 기본 추출기로 사용하는
// HTMLListParser를 생성합니다.
// 커스텀 ParseList 구현이 없는 신규 소스의 폴백으로 사용합니다.
//
// NewDefaultHTMLListParser creates an HTMLListParser backed by core.HTMLLinkExtractor.
// Use this as a fallback for new sources that lack a custom ParseList implementation.
// Existing sources (naver, yonhap, daum, cnn) use their own source-specific parsers.
func NewDefaultHTMLListParser(baseURL string, opts ...links.Option) *HTMLListParser {
	// 뉴스 목록 파싱은 항상 동일 오리진 링크만 대상으로 합니다.
	allOpts := append([]links.Option{links.WithSameOriginOnly()}, opts...)
	return NewHTMLListParser(core.NewHTMLLinkExtractor(baseURL, allOpts...))
}

// ParseList는 raw에서 링크를 추출하여 []NewsItem으로 반환합니다.
// 추출은 주입된 core.LinkExtractor에 위임합니다.
func (p *HTMLListParser) ParseList(raw *core.RawContent) ([]NewsItem, error) {
	extracted, err := p.extractor.Extract(raw)
	if err != nil {
		return nil, core.NewParseError("PARSE_001", "failed to extract links", raw.URL, err)
	}

	if len(extracted) == 0 {
		return nil, nil
	}

	items := make([]NewsItem, len(extracted))
	for i, l := range extracted {
		items[i] = NewsItem{URL: l.URL, Title: l.Text}
	}
	return items, nil
}
