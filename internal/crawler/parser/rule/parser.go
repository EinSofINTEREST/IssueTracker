package rule

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"

	"issuetracker/internal/crawler/core"
	"issuetracker/internal/crawler/parser"
	"issuetracker/internal/storage"
)

// resolveTimeout 은 ContentParser/LinkListParser 인터페이스가 ctx 인자를 받지 못해
// background ctx 를 사용해야 하는 경우의 안전망입니다 (Gemini code review #1, #2 피드백).
// Resolver 의 Redis/cache 핫패스가 막혀도 호출 worker 가 영원히 block 되지 않도록 5초.
const resolveTimeout = 5 * time.Second

// Parser 는 DB 기반 파싱 규칙으로 동작하는 단일 page parser engine 입니다 (이슈 #100).
//
// Parser implements both parser.ContentParser and parser.LinkListParser, driven by
// storage.ParsingRuleRecord resolved per request via Resolver. 사이트별 hardcode 파서
// (NaverParser/DaumParser/...) 를 대체 — 새 사이트 지원 = parsing_rules row 추가.
//
// 도메인 중립 — 뉴스 / 블로그 / 제품 페이지 / 일반 문서 모두 동일 engine 으로 처리.
// 호출자가 도메인-specific 모델로 변환 (예: Page → news.NewsArticle) 하면 됨.
//
// stateless / goroutine-safe — 모든 worker 가 단일 인스턴스 공유 가능.
type Parser struct {
	resolver    *Resolver
	dateLayouts []string // PublishedAt try-list (앞쪽 우선)
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

// defaultDateLayouts 는 PublishedAt 추출 시 시도할 layout 목록입니다.
// 사이트별 차이 (RFC3339 / Korean / ISO 8601 etc) 를 일반화. 운영 중 새 형식 발견 시 확장.
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

// ParsePage 는 RawContent 를 DB rule 기반으로 Page 로 파싱합니다 (parser.ContentParser 구현).
//
// 흐름:
//  1. raw.URL 의 host 로 active page rule lookup (Resolver — cache hit 핫패스)
//  2. rule.Selectors 에 따라 각 필드 (Title/MainContent/Author/PublishedAt/...) 추출
//  3. Title selector 누락 → ErrEmptySelector (필수 필드)
//  4. MainContent 매칭 0건 → ErrParseFailure (selector 는 있지만 매칭 0건 = stale rule 진단)
func (p *Parser) ParsePage(raw *core.RawContent) (*parser.Page, error) {
	if err := validateRaw(raw); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), resolveTimeout)
	defer cancel()
	rule, err := p.resolver.ResolveByURL(ctx, raw.URL, storage.TargetTypePage)
	if err != nil {
		return nil, err
	}

	if rule.Selectors.Title == nil {
		return nil, &Error{
			Code:       ErrEmptySelector,
			Message:    "page rule missing required Title selector",
			URL:        raw.URL,
			TargetType: string(storage.TargetTypePage),
		}
	}

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(raw.HTML))
	if err != nil {
		return nil, &Error{Code: ErrParseFailure, Message: "goquery parse failed", URL: raw.URL, Err: err}
	}

	page := &parser.Page{
		URL:         raw.URL,
		Title:       extractField(doc, rule.Selectors.Title),
		MainContent: extractField(doc, rule.Selectors.MainContent),
		Summary:     extractField(doc, rule.Selectors.Summary),
		Author:      extractField(doc, rule.Selectors.Author),
		Category:    extractField(doc, rule.Selectors.Category),
		Tags:        extractFieldMulti(doc, rule.Selectors.Tags),
		Images:      extractFieldMulti(doc, rule.Selectors.Images),
		PublishedAt: p.extractDate(doc, rule.Selectors.PublishedAt),
	}

	// Title 도 MainContent 와 동등한 필수 — selector 는 있지만 추출 결과 빈 경우도 stale 진단 (Gemini #5).
	if page.Title == "" || page.MainContent == "" {
		return nil, &Error{
			Code:       ErrParseFailure,
			Message:    "Title or MainContent selector matched 0 elements (rule may be stale)",
			URL:        raw.URL,
			TargetType: string(storage.TargetTypePage),
		}
	}
	return page, nil
}

// ParseLinks 는 RawContent 의 링크-허브 페이지를 LinkItem 슬라이스로 파싱합니다
// (parser.LinkListParser 구현).
//
// 흐름:
//  1. raw.URL 의 host 로 active list rule lookup
//  2. rule.ItemContainer selector 로 각 item element 순회
//  3. 각 item 안에서 ItemLink (href) / ItemTitle / ItemSnippet 추출
//  4. 상대 URL 은 raw.URL base 로 절대 URL 화
func (p *Parser) ParseLinks(raw *core.RawContent) ([]parser.LinkItem, error) {
	if err := validateRaw(raw); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), resolveTimeout)
	defer cancel()
	rule, err := p.resolver.ResolveByURL(ctx, raw.URL, storage.TargetTypeList)
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
		// raw.URL 이 잘못된 경우 — 상대 URL 절대화 못 하면 link 그대로 유지
		base = nil
	}

	var items []parser.LinkItem
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
		items = append(items, parser.LinkItem{
			URL:     absURL,
			Title:   extractFieldFromSelection(container, rule.Selectors.ItemTitle),
			Snippet: extractFieldFromSelection(container, rule.Selectors.ItemSnippet),
		})
	})

	// 결과 0건 → ItemContainer 가 사이트 구조 변경으로 매칭 실패 (stale rule 진단, Gemini #7).
	if len(items) == 0 {
		return nil, &Error{
			Code:       ErrParseFailure,
			Message:    "ItemContainer selector matched 0 elements (rule may be stale)",
			URL:        raw.URL,
			TargetType: string(storage.TargetTypeList),
		}
	}
	return items, nil
}

// validateRaw 는 ParsePage / ParseLinks 의 공통 raw 검증입니다 (Gemini #4, #6).
// raw 가 nil 이거나 HTML 이 비어있으면 진단 친화적으로 raw.URL 을 포함한 Error 반환.
func validateRaw(raw *core.RawContent) error {
	if raw != nil && raw.HTML != "" {
		return nil
	}
	var u string
	if raw != nil {
		u = raw.URL
	}
	return &Error{Code: ErrParseFailure, Message: "raw content empty", URL: u}
}

// extractField 는 단일 필드를 추출합니다 (selector 가 nil 이면 빈 문자열).
//
// Multi=false (기본): 첫 매칭 element 의 값 반환.
// Multi=true: 모든 매칭 element 의 값을 줄바꿈으로 합쳐 반환 (MainContent 다중 단락 등).
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

// extractFieldMulti 는 multi 결과를 string 슬라이스로 반환합니다 (Tags / Images 용).
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

// extractDate 는 PublishedAt 필드를 추출하고 dateLayouts 를 순회 시도합니다.
// 추출 실패 시 zero time 반환 — 호출자 (validator 등) 가 zero 검사로 분기.
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
	_ parser.ContentParser  = (*Parser)(nil)
	_ parser.LinkListParser = (*Parser)(nil)
)
