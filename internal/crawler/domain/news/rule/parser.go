package rule

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"

	"issuetracker/internal/crawler/core"
	"issuetracker/internal/crawler/domain/news"
	"issuetracker/internal/storage"
)

// Parser 는 DB 기반 파싱 규칙으로 동작하는 단일 parser engine 입니다 (이슈 #100).
//
// Parser implements both news.NewsArticleParser and news.NewsListParser interfaces,
// driven by storage.ParsingRuleRecord resolved per request via Resolver.
//
// 사이트별 hardcode 파서 (NaverParser/DaumParser/...) 를 대체:
//   - 새 사이트 지원 = parsing_rules row 추가 (코드 변경 없음)
//   - 미지원 page 진단 = ErrNoRule 반환 (LLM 자동 생성 fallback 진입점)
//   - selector 진화 = parsing_rules version 새 row + enabled flip
//
// 본 Parser 는 stateless (resolver / dateLayouts 만 보유) 라 단일 인스턴스를 모든
// worker 가 공유 가능 — goroutine-safe.
type Parser struct {
	resolver    *Resolver
	dateLayouts []string // ParseArticle 의 Date 필드 try-list (앞쪽 우선)
}

// NewParser 는 Resolver 를 사용하는 Parser 를 생성합니다.
// resolver 가 nil 이면 panic — wire 누락 즉시 가시화.
func NewParser(resolver *Resolver) *Parser {
	if resolver == nil {
		panic("rule: NewParser requires non-nil resolver")
	}
	return &Parser{
		resolver:    resolver,
		dateLayouts: defaultDateLayouts(),
	}
}

// defaultDateLayouts 는 ParseArticle 이 Date 추출 시 시도할 layout 목록입니다.
// 사이트별 차이 (RFC3339 / Korean / ISO 8601 etc) 를 일반화. 운영 중 새 형식이
// 발견되면 본 목록 확장으로 대응.
func defaultDateLayouts() []string {
	return []string{
		time.RFC3339,
		time.RFC3339Nano,
		"2006-01-02T15:04:05Z07:00",
		"2006-01-02 15:04:05",
		"2006-01-02 15:04",
		"2006.01.02 15:04",
		"2006.01.02. 15:04",
		"2006.01.02",
		"2006/01/02 15:04:05",
	}
}

// ParseArticle 은 RawContent 를 DB rule 기반으로 NewsArticle 로 파싱합니다.
//
// ParseArticle implements news.NewsArticleParser. 흐름:
//  1. raw.URL 의 host 로 active article rule lookup (Resolver — cache hit 핫패스)
//  2. rule 의 selectors 에 따라 각 필드 (Title/Body/Author/Date/...) 추출
//  3. Title 등 필수 필드 selector 누락 시 ErrEmptySelector
//  4. Body 가 빈 결과면 ErrParseFailure (selector 는 있지만 매칭 0건 → 실질 무의미)
func (p *Parser) ParseArticle(raw *core.RawContent) (*news.NewsArticle, error) {
	if raw == nil || raw.HTML == "" {
		return nil, &Error{Code: ErrParseFailure, Message: "raw content empty", URL: ""}
	}

	rule, err := p.resolver.ResolveByURL(context.Background(), raw.URL, storage.TargetTypeArticle)
	if err != nil {
		return nil, err
	}

	if rule.Selectors.Title == nil {
		return nil, &Error{
			Code:       ErrEmptySelector,
			Message:    "article rule missing required Title selector",
			URL:        raw.URL,
			TargetType: string(storage.TargetTypeArticle),
		}
	}

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(raw.HTML))
	if err != nil {
		return nil, &Error{Code: ErrParseFailure, Message: "goquery parse failed", URL: raw.URL, Err: err}
	}

	article := &news.NewsArticle{
		URL:         raw.URL,
		Title:       extractField(doc, rule.Selectors.Title),
		Summary:     extractField(doc, rule.Selectors.Summary),
		Body:        extractField(doc, rule.Selectors.Body),
		Author:      extractField(doc, rule.Selectors.Author),
		Category:    extractField(doc, rule.Selectors.Category),
		Tags:        extractFieldMulti(doc, rule.Selectors.Tags),
		ImageURLs:   extractFieldMulti(doc, rule.Selectors.ImageURLs),
		PublishedAt: p.extractDate(doc, rule.Selectors.Date),
	}

	if article.Body == "" {
		return nil, &Error{
			Code:       ErrParseFailure,
			Message:    "Body selector matched 0 elements (rule may be stale)",
			URL:        raw.URL,
			TargetType: string(storage.TargetTypeArticle),
		}
	}
	return article, nil
}

// ParseList 는 RawContent 의 카테고리/목록 페이지를 NewsItem 슬라이스로 파싱합니다.
//
// ParseList implements news.NewsListParser. 흐름:
//  1. raw.URL 의 host 로 active list rule lookup
//  2. rule.ItemContainer selector 로 각 item element 순회
//  3. 각 item 안에서 ItemLink (href) / ItemTitle / ItemSummary 추출
//  4. URL 은 raw.URL 기준으로 절대 URL 로 정규화 (상대 경로 처리)
func (p *Parser) ParseList(raw *core.RawContent) ([]news.NewsItem, error) {
	if raw == nil || raw.HTML == "" {
		return nil, &Error{Code: ErrParseFailure, Message: "raw content empty"}
	}

	rule, err := p.resolver.ResolveByURL(context.Background(), raw.URL, storage.TargetTypeList)
	if err != nil {
		return nil, err
	}

	if rule.Selectors.ItemContainer == nil || rule.Selectors.ItemLink == nil {
		return nil, &Error{
			Code:       ErrEmptySelector,
			Message:    "list rule missing required ItemContainer or ItemLink selector",
			URL:        raw.URL,
			TargetType: string(storage.TargetTypeList),
		}
	}

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(raw.HTML))
	if err != nil {
		return nil, &Error{Code: ErrParseFailure, Message: "goquery parse failed", URL: raw.URL, Err: err}
	}

	base, baseErr := url.Parse(raw.URL)
	if baseErr != nil {
		// raw.URL 이 잘못된 경우 — 절대 URL 화 못 하면 link skip
		base = nil
	}

	var items []news.NewsItem
	doc.Find(rule.Selectors.ItemContainer.CSS).Each(func(_ int, container *goquery.Selection) {
		link := extractFieldFromSelection(container, rule.Selectors.ItemLink)
		if link == "" {
			return
		}
		absURL := link
		if base != nil {
			if abs, err := absoluteURL(base, link); err == nil {
				absURL = abs
			}
		}
		items = append(items, news.NewsItem{
			URL:     absURL,
			Title:   extractFieldFromSelection(container, rule.Selectors.ItemTitle),
			Summary: extractFieldFromSelection(container, rule.Selectors.ItemSummary),
		})
	})
	return items, nil
}

// extractField 는 단일 필드를 추출합니다 (selector 가 nil 이면 빈 문자열).
//
// Multi=false (기본): 첫 매칭 element 의 값 반환.
// Multi=true: 모든 매칭 element 의 값을 줄바꿈으로 합쳐 반환 (Body 의 다중 단락 등).
func extractField(doc *goquery.Document, fs *storage.FieldSelector) string {
	if fs == nil || fs.CSS == "" {
		return ""
	}
	return extractFromSelection(doc.Selection, fs)
}

// extractFieldFromSelection 은 sub-selection 안에서 필드를 추출합니다 (list item 내부 lookup).
func extractFieldFromSelection(s *goquery.Selection, fs *storage.FieldSelector) string {
	if fs == nil || fs.CSS == "" {
		return ""
	}
	return extractFromSelection(s, fs)
}

// extractFromSelection 은 selection 범위에서 selector 적용 결과를 반환합니다.
func extractFromSelection(scope *goquery.Selection, fs *storage.FieldSelector) string {
	matched := scope.Find(fs.CSS)
	if matched.Length() == 0 {
		return ""
	}
	if !fs.Multi {
		return strings.TrimSpace(extractValue(matched.First(), fs.Attribute))
	}
	var parts []string
	matched.Each(func(_ int, sel *goquery.Selection) {
		v := strings.TrimSpace(extractValue(sel, fs.Attribute))
		if v != "" {
			parts = append(parts, v)
		}
	})
	return strings.Join(parts, "\n")
}

// extractFieldMulti 는 multi 결과를 string 슬라이스로 반환합니다 (Tags / ImageURLs 용).
//
// extractField 의 multi 모드는 줄바꿈으로 합치지만, 본 함수는 각 element 를 별도 항목으로 보존.
func extractFieldMulti(doc *goquery.Document, fs *storage.FieldSelector) []string {
	if fs == nil || fs.CSS == "" {
		return nil
	}
	matched := doc.Find(fs.CSS)
	if matched.Length() == 0 {
		return nil
	}
	out := make([]string, 0, matched.Length())
	matched.Each(func(_ int, sel *goquery.Selection) {
		v := strings.TrimSpace(extractValue(sel, fs.Attribute))
		if v != "" {
			out = append(out, v)
		}
	})
	if len(out) == 0 {
		return nil
	}
	return out
}

// extractValue 는 element 의 text (Attribute=="") 또는 attribute 값을 반환합니다.
func extractValue(sel *goquery.Selection, attribute string) string {
	if attribute == "" {
		return sel.Text()
	}
	v, _ := sel.Attr(attribute)
	return v
}

// extractDate 는 Date 필드를 추출하고 dateLayouts 를 순회 시도합니다.
// 추출 실패 시 zero time 반환 — 호출자 (validator) 가 zero 검사로 분기.
func (p *Parser) extractDate(doc *goquery.Document, fs *storage.FieldSelector) time.Time {
	raw := extractField(doc, fs)
	if raw == "" {
		return time.Time{}
	}
	for _, layout := range p.dateLayouts {
		if t, err := time.Parse(layout, raw); err == nil {
			return t
		}
	}
	return time.Time{}
}

// absoluteURL 은 link 를 base 기준 절대 URL 로 변환합니다.
func absoluteURL(base *url.URL, link string) (string, error) {
	ref, err := url.Parse(link)
	if err != nil {
		return "", fmt.Errorf("parse link: %w", err)
	}
	return base.ResolveReference(ref).String(), nil
}

// 컴파일 시 인터페이스 구현 검증 — 두 인터페이스 모두 만족해야 함.
var (
	_ news.NewsArticleParser = (*Parser)(nil)
	_ news.NewsListParser    = (*Parser)(nil)
)
