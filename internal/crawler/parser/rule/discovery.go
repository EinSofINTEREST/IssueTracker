package rule

import (
	"fmt"
	"regexp"

	"issuetracker/internal/crawler/core"
	"issuetracker/internal/crawler/parser"
	"issuetracker/internal/storage"
	"issuetracker/pkg/links"
)

// PageLinkDiscovery 는 페이지 전체 <a href> 를 스캔한 뒤 LinkDiscoveryConfig 의
// ArticleURLPattern (RE2 regex) 로 article URL 만 통과시키는 generic discovery 입니다 (이슈 #139).
//
// PageLinkDiscovery scans every <a href> on a page, then filters by the rule's
// ArticleURLPattern regex. Replaces site-specific ItemContainer extraction when
// the rule opts into LinkDiscovery mode.
//
// 사이드바 / 추천 기사 / 카테고리 메뉴 / 관련 기사 등 ItemContainer 가 놓치는 영역까지
// 같은 fetch 비용으로 발견. noise 는 ExcludePatterns + MaxLinksPerPage + 다운스트림
// URL dedup / rate limiter 가 흡수합니다.
//
// stateless / goroutine-safe — 호출 시마다 regex 컴파일을 회피하기 위해
// Resolver 가 cache 한 ParsingRuleRecord 를 재사용하는 호출자 측에서 컴파일 결과를
// 메모이즈하는 것이 이상적이나, 본 PR 범위에서는 호출 시 한 번만 컴파일.
type PageLinkDiscovery struct{}

// NewPageLinkDiscovery 는 stateless discovery 컴포넌트를 생성합니다.
func NewPageLinkDiscovery() *PageLinkDiscovery { return &PageLinkDiscovery{} }

// Discover 는 raw 의 HTML 에서 cfg 정책에 부합하는 링크들을 LinkItem 으로 반환합니다.
//
// 흐름:
//  1. cfg.ArticleURLPattern compile (빈 문자열이면 ErrEmptySelector)
//  2. pkg/links.Extractor 로 raw.HTML 의 모든 <a href> 추출
//     (SameOriginOnly / PathPrefixes / ExcludePatterns / MaxLinksPerPage 적용)
//  3. extractor 결과를 ArticleURLPattern regex 로 추가 필터링
//
// 반환 LinkItem 의 Title 은 anchor text 로 채움 (ItemContainer 모드 와 달리 별도 selector
// 가 없음). Snippet 은 비워둠 — full-page discovery 는 list 페이지의 컨텍스트가 없으므로
// snippet 추출이 불가능. 호출자 (worker / publisher) 는 Title 빈 케이스를 허용해야 함.
func (d *PageLinkDiscovery) Discover(raw *core.RawContent, cfg *storage.LinkDiscoveryConfig) ([]parser.LinkItem, error) {
	if err := validateRaw(raw); err != nil {
		return nil, err
	}
	if cfg == nil || cfg.ArticleURLPattern == "" {
		return nil, &Error{
			Code:       ErrEmptySelector,
			Message:    "link discovery requires non-empty ArticleURLPattern",
			URL:        raw.URL,
			TargetType: string(storage.TargetTypeList),
		}
	}

	pattern, err := regexp.Compile(cfg.ArticleURLPattern)
	if err != nil {
		return nil, &Error{
			Code:       ErrEmptySelector,
			Message:    fmt.Sprintf("invalid ArticleURLPattern regex: %q", cfg.ArticleURLPattern),
			URL:        raw.URL,
			TargetType: string(storage.TargetTypeList),
			Err:        err,
		}
	}

	opts := []links.Option{}
	if cfg.SameOriginOnly {
		opts = append(opts, links.WithSameOriginOnly())
	}
	if len(cfg.PathPrefixes) > 0 {
		opts = append(opts, links.WithPathPrefixes(cfg.PathPrefixes...))
	}
	if len(cfg.ExcludePatterns) > 0 {
		opts = append(opts, links.WithExcludePatterns(cfg.ExcludePatterns...))
	}
	// MaxLinksPerPage 는 regex pre-filter 단계에서 cap 하면 매칭되지 않은 링크가
	// quota 를 차지해 article 발견량이 줄어듦. 따라서 extractor 단계 cap 은 적용하지 않고
	// regex 통과 후 직접 cap. (cap 으로 인한 OOM 방어는 extractor 의 dedup map 으로도 부분 보장)

	extractor := links.NewExtractor(raw.URL, opts...)
	candidates, err := extractor.Extract(raw.HTML, raw.URL)
	if err != nil {
		return nil, &Error{
			Code:       ErrParseFailure,
			Message:    "link extractor failed",
			URL:        raw.URL,
			TargetType: string(storage.TargetTypeList),
			Err:        err,
		}
	}

	maxOut := cfg.MaxLinksPerPage
	out := make([]parser.LinkItem, 0, len(candidates))
	for _, l := range candidates {
		if !pattern.MatchString(l.URL) {
			continue
		}
		out = append(out, parser.LinkItem{
			URL:   l.URL,
			Title: l.Text,
		})
		if maxOut > 0 && len(out) >= maxOut {
			break
		}
	}

	if len(out) == 0 {
		// 매칭 0건 — pattern stale 이거나 페이지에 article URL 자체가 없음.
		// ItemContainer 경로의 0건 진단과 동일한 메시지 패턴.
		return nil, &Error{
			Code:       ErrParseFailure,
			Message:    "ArticleURLPattern matched 0 links on page (pattern may be stale or page empty)",
			URL:        raw.URL,
			TargetType: string(storage.TargetTypeList),
		}
	}
	return out, nil
}
