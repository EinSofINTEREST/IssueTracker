package rule

import (
	"fmt"
	"math/rand/v2"
	"net/url"
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
//  1. cfg.ArticleURLPattern 이 비어있지 않으면 compile (빈 문자열이면 all-pass 모드 — 이슈 #148)
//  2. pkg/links.Extractor 로 raw.HTML 의 모든 <a href> 추출
//     (SameOriginOnly / PathPrefixes / ExcludePatterns / MaxLinksPerPage 적용)
//  3. pattern 이 있으면 추가 regex 필터링, 없으면 extractor 결과 그대로
//
// All-pass 모드 (이슈 #148):
//   - 본 시스템의 타겟은 \"뉴스 기사\" 만이 아닌 페이지 내 모든 의미 있는 글 (이슈 #100 도메인 일반화)
//   - ArticleURLPattern 을 강제하면 사이트별 article URL regex 외 모든 컨텐츠가 누락됨
//   - 빈 pattern 은 \"ExcludePatterns + SameOriginOnly + MaxLinksPerPage 만으로 필터\" 의도
//
// 반환 LinkItem 의 Title 은 anchor text 로 채움 (ItemContainer 모드 와 달리 별도 selector
// 가 없음). Snippet 은 비워둠 — full-page discovery 는 list 페이지의 컨텍스트가 없으므로
// snippet 추출이 불가능. 호출자 (worker / publisher) 는 Title 빈 케이스를 허용해야 함.
func (d *PageLinkDiscovery) Discover(raw *core.RawContent, cfg *storage.LinkDiscoveryConfig) ([]parser.LinkItem, error) {
	if err := validateRaw(raw); err != nil {
		return nil, err
	}
	if cfg == nil {
		return nil, &Error{
			Code:       ErrEmptySelector,
			Message:    "link discovery requires non-nil LinkDiscoveryConfig",
			URL:        raw.URL,
			TargetType: string(storage.TargetTypeList),
		}
	}

	// pattern 이 비어있으면 all-pass — 이슈 #148. Extractor 의 다른 옵션만으로 필터링.
	var pattern *regexp.Regexp
	if cfg.ArticleURLPattern != "" {
		compiled, err := regexp.Compile(cfg.ArticleURLPattern)
		if err != nil {
			return nil, &Error{
				Code:       ErrEmptySelector,
				Message:    fmt.Sprintf("invalid ArticleURLPattern regex: %q", cfg.ArticleURLPattern),
				URL:        raw.URL,
				TargetType: string(storage.TargetTypeList),
				Err:        err,
			}
		}
		pattern = compiled
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

	// MaxLinksPerPage 우선순위 정책 (이슈 #148 후속):
	//   1. same-origin (raw.URL 의 host 와 동일) 링크는 maxOut 무시하고 모두 통과
	//   2. cross-origin 링크는 잔여 슬롯 (maxOut - len(same)) 만큼 무작위 sample
	//   3. maxOut == 0 (무제한) 이면 cross 도 모두 통과
	//
	// 이유: 운영 사이트 자체 컨텐츠 (same-origin) 는 noise 가 적고 가치 높음 — 모두 발행.
	// 외부 링크 (cross-origin) 는 광고/제휴 노이즈 비율 높으므로 cap 으로 통제하되,
	// 무작위 sample 로 특정 영역 (예: 페이지 상단의 광고 슬롯) 에 편향되지 않도록.
	maxOut := cfg.MaxLinksPerPage
	pageHost := hostOf(raw.URL)

	sameOrigin := make([]parser.LinkItem, 0, len(candidates))
	crossOrigin := make([]parser.LinkItem, 0)
	for _, l := range candidates {
		if pattern != nil && !pattern.MatchString(l.URL) {
			continue
		}
		item := parser.LinkItem{URL: l.URL, Title: l.Text}
		if pageHost != "" && hostOf(l.URL) == pageHost {
			sameOrigin = append(sameOrigin, item)
		} else {
			crossOrigin = append(crossOrigin, item)
		}
	}

	out := append(make([]parser.LinkItem, 0, len(sameOrigin)+len(crossOrigin)), sameOrigin...)

	switch {
	case len(crossOrigin) == 0:
		// nothing to add
	case maxOut <= 0:
		// 무제한 — cross 도 모두 추가
		out = append(out, crossOrigin...)
	default:
		remaining := maxOut - len(sameOrigin)
		switch {
		case remaining <= 0:
			// same-origin 만으로 cap 초과 — cross 0건 (정책상 same 우선)
		case remaining >= len(crossOrigin):
			out = append(out, crossOrigin...)
		default:
			// remaining < len(crossOrigin) — 무작위로 remaining 개 sample
			rand.Shuffle(len(crossOrigin), func(i, j int) {
				crossOrigin[i], crossOrigin[j] = crossOrigin[j], crossOrigin[i]
			})
			out = append(out, crossOrigin[:remaining]...)
		}
	}

	if len(out) == 0 {
		// 매칭 0건 — pattern 모드면 stale, all-pass 모드면 페이지 자체에 통과 가능 링크 없음.
		// 진단 메시지는 모드별로 분기하여 운영자가 원인 추적하기 쉽게.
		msg := "all <a href> filtered out (page has no eligible links)"
		if pattern != nil {
			msg = "ArticleURLPattern matched 0 links on page (pattern may be stale or page empty)"
		}
		return nil, &Error{
			Code:       ErrParseFailure,
			Message:    msg,
			URL:        raw.URL,
			TargetType: string(storage.TargetTypeList),
		}
	}
	return out, nil
}

// hostOf 는 URL 문자열에서 host 를 추출합니다 (parse 실패 시 빈 문자열).
// 본 함수는 same-origin 분류 비교용 — 빈 문자열 비교는 항상 cross-origin 으로 취급되도록.
func hostOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u == nil {
		return ""
	}
	return u.Host
}
