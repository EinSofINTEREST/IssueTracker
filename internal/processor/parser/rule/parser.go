package rule

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"

	"issuetracker/internal/processor/fetcher/core"
	"issuetracker/internal/processor/parser/rule/indexonly"
	"issuetracker/internal/processor/parser/types"
	"issuetracker/internal/storage/model"
	"issuetracker/pkg/logger"
)

// resolveTimeout 은 호출자 ctx 가 deadline 없을 때 추가 안전망입니다.
// Resolver 의 Redis/cache 핫패스가 막혀도 호출 worker 가 영원히 block 되지 않도록 5초.
//
// ctx 에 이미 더 짧은 deadline 이 있으면 그것이 우선 (context.WithTimeout 이 합성).
const resolveTimeout = 5 * time.Second

// Parser 는 DB 기반 파싱 규칙으로 동작하는 단일 page parser engine 입니다.
//
// Parser implements both types.ContentParser and types.LinkListParser, driven by
// model.ParserRuleRecord resolved per request via Resolver. 사이트별 hardcode 파서
// (NaverParser/DaumParser/...) 를 대체 — 새 사이트 지원 = parser_rules row 추가.
//
// 도메인 중립 — 뉴스 / 블로그 / 제품 페이지 / 일반 문서 모두 동일 engine 으로 처리.
// 호출자가 도메인-specific 모델로 변환 (예: Page → news.NewsArticle) 하면 됨.
//
// stateless / goroutine-safe — 모든 worker 가 단일 인스턴스 공유 가능.
type Parser struct {
	resolver    RuleLookup
	discovery   *PageLinkDiscovery // full-page link discovery
	dateLayouts []string           // PublishedAt try-list (앞쪽 우선)

	// 이슈 #477 — ParsePage 결과가 index-only 페이지로 판정되면 parser_blacklist 에
	// extract_links_only mode 로 자동 등록. nil 이면 기능 비활성 (기존 동작 100% 유지).
	autoDemoter *autoDemoter
}

// ParserOption 은 NewParser 의 functional option 입니다.
type ParserOption func(*Parser)

// WithBlacklistAutoDemote 는 ParsePage 가 index-only 페이지로 판정한 URL 을
// parser_blacklist 에 자동 등록하는 기능을 활성화합니다 (이슈 #477).
//
//   - repo    : Insert 만 사용. service.BlacklistService / repository.BlacklistRepository
//     모두 AutoDemoteRegisterer 를 만족.
//   - metrics : nil 허용. nil 이면 Record* 가 noop — 기능은 동작하지만 metric 노출 안 됨.
//   - log     : non-nil 필수. WARN 로그 (auto-demote 발생 / Insert 실패) 출력.
//
// 인자 중 repo 또는 log 가 nil 이면 옵션 자체가 noop — 기존 ParsePage 흐름 유지.
func WithBlacklistAutoDemote(repo AutoDemoteRegisterer, metrics *AutoDemoteMetrics, log *logger.Logger) ParserOption {
	return func(p *Parser) {
		if repo == nil || log == nil {
			return
		}
		p.autoDemoter = &autoDemoter{repo: repo, metrics: metrics, log: log}
	}
}

// RuleLookup 은 URL + target_type 으로 활성 ParserRule 을 조회하는 추상 (이슈 #463).
//
// *Resolver 가 자연스럽게 본 인터페이스를 만족 — Parser 는 concrete 대신 interface 의존으로
// 단위 테스트 시 mock 주입 가능. 운영 wiring (main.go) 은 변경 없음.
type RuleLookup interface {
	ResolveByURL(ctx context.Context, rawURL string, targetType model.TargetType) (*model.ParserRuleRecord, error)
}

// 컴파일 타임 검증: *Resolver 가 RuleLookup 을 만족.
var _ RuleLookup = (*Resolver)(nil)

// NewParser 는 RuleLookup 을 사용하는 Parser 를 생성합니다.
// resolver 가 nil 이면 error — 호출자 (cmd/main) 가 boot fatal 처리.
//
// opts 는 functional option (예: WithBlacklistAutoDemote). 호출자가 미지정하면 기존 동작 유지.
func NewParser(resolver RuleLookup, opts ...ParserOption) (*Parser, error) {
	if resolver == nil {
		return nil, errors.New("rule: NewParser requires non-nil resolver")
	}
	p := &Parser{
		resolver:    resolver,
		discovery:   NewPageLinkDiscovery(),
		dateLayouts: defaultDateLayouts(),
	}
	for _, opt := range opts {
		opt(p)
	}
	return p, nil
}

// ParsePage 는 RawContent 를 DB rule 기반으로 Page 로 파싱합니다 (types.ContentParser 구현).
//
// 흐름:
//  1. raw.URL 의 host 로 active page rule lookup (Resolver — cache hit 핫패스)
//  2. rule.Selectors 에 따라 각 필드 (Title/MainContent/Author/PublishedAt/...) 추출
//  3. Title selector 누락 → ErrEmptySelector (필수 필드)
//  4. MainContent 매칭 0건 → ErrParseFailure (selector 는 있지만 매칭 0건 = stale rule 진단)
func (p *Parser) ParsePage(ctx context.Context, raw *core.RawContent) (*types.Page, error) {
	if err := validateRaw(raw); err != nil {
		return nil, err
	}

	// 호출자 ctx 의 cancel/trace metadata 를 보존하면서 추가 timeout 안전망 적용.
	resolveCtx, cancel := context.WithTimeout(ctx, resolveTimeout)
	defer cancel()
	rule, err := p.resolver.ResolveByURL(resolveCtx, raw.URL, model.TargetTypePage)
	if err != nil {
		return nil, err
	}
	// RuleLookup interface contract 가 nil-success 를 명시적으로 금지하지 않으므로 mock /
	// 향후 구현체가 (nil, nil) 반환 시 dereferencing panic 방어 (coderabbit-review #464).
	if rule == nil {
		return nil, &Error{
			Code:       ErrNoRule,
			Message:    "rule lookup returned nil rule",
			URL:        raw.URL,
			TargetType: string(model.TargetTypePage),
		}
	}

	// Title + MainContent 둘 다 필수 — nil 또는 CSS 빈 문자열 모두 ErrEmptySelector 로 분류
	// (Coderabbit 피드백: nil 만 검사하면 zero-value selector 가 ErrParseFailure 로 잘못 분류됨)
	if !hasRequiredSelector(rule.Selectors.Title) || !hasRequiredSelector(rule.Selectors.MainContent) {
		return nil, &Error{
			Code:       ErrEmptySelector,
			Message:    "page rule missing required Title or MainContent selector",
			URL:        raw.URL,
			TargetType: string(model.TargetTypePage),
		}
	}

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(raw.HTML))
	if err != nil {
		return nil, &Error{Code: ErrParseFailure, Message: "goquery parse failed", URL: raw.URL, Err: err}
	}

	page := &types.Page{
		URL:         raw.URL,
		Title:       extractField(doc, rule.Selectors.Title),
		MainContent: extractField(doc, rule.Selectors.MainContent),
		Summary:     extractField(doc, rule.Selectors.Summary),
		Author:      extractField(doc, rule.Selectors.Author),
		Category:    extractField(doc, rule.Selectors.Category),
		Tags:        extractFieldMulti(doc, rule.Selectors.Tags),
		Images:      extractFieldMulti(doc, rule.Selectors.Images),
		PublishedAt: p.extractDate(doc, rule.Selectors.PublishedAt),
		Article:     rule.Article,
	}

	// Title 도 MainContent 와 동등한 필수 — selector 는 있지만 추출 결과 빈 경우도 stale 진단.
	if page.Title == "" || page.MainContent == "" {
		return nil, &Error{
			Code:       ErrParseFailure,
			Message:    "Title or MainContent selector matched 0 elements (rule may be stale)",
			URL:        raw.URL,
			TargetType: string(model.TargetTypePage),
		}
	}

	// 이슈 #477 — index-only 페이지 자동 강등. autoDemoter 가 nil 이면 분기 자체 skip
	// (기능 비활성 시 기존 동작 100% 유지). 본 호출의 page 자체는 정상 반환 — 다음 호출부터
	// matcher 가 본 URL 을 extract_links_only 로 라우팅 (publisher 직전).
	if p.autoDemoter != nil {
		if ok, score := indexonly.IsIndexOnly(page, raw.HTML, indexonly.Config{}); ok {
			p.autoDemoter.demote(ctx, raw.URL, score)
		}
	}
	return page, nil
}

// ParseLinks 는 RawContent 의 링크-허브 페이지를 LinkItem 슬라이스로 파싱합니다
// (types.LinkListParser 구현).
//
// 모드 분기:
//   - rule.Selectors.LinkDiscovery.ArticleURLPattern 이 설정 → full-page discovery
//     (페이지 전체 <a href> + URL pattern 필터). 사이드바 / 추천 / 관련 기사 포함.
//   - 그렇지 않으면 → 기존 ItemContainer 경로 (정확한 컨테이너 기반 추출).
//
// ItemContainer 경로 흐름:
//  1. raw.URL 의 host 로 active list rule lookup
//  2. rule.ItemContainer selector 로 각 item element 순회
//  3. 각 item 안에서 ItemLink (href) / ItemTitle / ItemSnippet 추출
//  4. 상대 URL 은 raw.URL base 로 절대 URL 화
//
// LinkDiscovery 경로 흐름은 PageLinkDiscovery.Discover 에 위임.
func (p *Parser) ParseLinks(ctx context.Context, raw *core.RawContent) ([]types.LinkItem, error) {
	if err := validateRaw(raw); err != nil {
		return nil, err
	}

	// 호출자 ctx 의 cancel/trace metadata 를 보존하면서 추가 timeout 안전망 적용.
	resolveCtx, cancel := context.WithTimeout(ctx, resolveTimeout)
	defer cancel()
	rule, err := p.resolver.ResolveByURL(resolveCtx, raw.URL, model.TargetTypeList)
	if err != nil {
		return nil, err
	}
	// RuleLookup interface contract 가 nil-success 를 명시적으로 금지하지 않으므로 방어 (coderabbit-review #464).
	if rule == nil {
		return nil, &Error{
			Code:       ErrNoRule,
			Message:    "rule lookup returned nil rule",
			URL:        raw.URL,
			TargetType: string(model.TargetTypeList),
		}
	}

	// LinkDiscovery 모드 — opt-in.
	// LinkDiscovery 객체 자체가 채워져 있으면 discovery 경로 (ArticleURLPattern 빈 문자열도 허용 — all-pass).
	// LinkDiscovery 가 nil 일 때만 ItemContainer fallback.
	if cfg := rule.Selectors.LinkDiscovery; cfg != nil {
		return p.discovery.Discover(raw, cfg)
	}

	// ItemContainer + ItemLink 둘 다 필수 — nil 또는 CSS 빈 문자열 모두 ErrEmptySelector
	if !hasRequiredSelector(rule.Selectors.ItemContainer) || !hasRequiredSelector(rule.Selectors.ItemLink) {
		return nil, &Error{
			Code:       ErrEmptySelector,
			Message:    "list rule missing required ItemContainer or ItemLink selector (or set LinkDiscovery.ArticleURLPattern)",
			URL:        raw.URL,
			TargetType: string(model.TargetTypeList),
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

	containers := doc.Find(rule.Selectors.ItemContainer.CSS)
	// ItemContainer 자체가 매칭 0건 — 사이트 구조 변경으로 selector stale.
	// (Coderabbit 피드백: ItemLink 모두 빈 case 와 분리하여 정확한 진단 메시지 제공)
	if containers.Length() == 0 {
		return nil, &Error{
			Code:       ErrParseFailure,
			Message:    "ItemContainer selector matched 0 elements (rule may be stale)",
			URL:        raw.URL,
			TargetType: string(model.TargetTypeList),
		}
	}

	var items []types.LinkItem
	containers.Each(func(_ int, container *goquery.Selection) {
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
		items = append(items, types.LinkItem{
			URL:     absURL,
			Title:   extractFieldFromSelection(container, rule.Selectors.ItemTitle),
			Snippet: extractFieldFromSelection(container, rule.Selectors.ItemSnippet),
		})
	})

	// ItemContainer 는 매칭됐지만 모든 ItemLink 가 빈 결과 — ItemLink selector stale.
	if len(items) == 0 {
		return nil, &Error{
			Code:       ErrParseFailure,
			Message:    "ItemContainer matched but no valid ItemLink found (ItemLink selector may be stale)",
			URL:        raw.URL,
			TargetType: string(model.TargetTypeList),
		}
	}
	return items, nil
}

// 컴파일 시 인터페이스 구현 검증 — 두 인터페이스 모두 만족해야 함.
var (
	_ types.ContentParser  = (*Parser)(nil)
	_ types.LinkListParser = (*Parser)(nil)
)
