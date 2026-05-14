// Package sources 는 fetcher_rules DB 에서 모든 사이트 크롤러를 읽어 Registry 에 등록합니다.
//
// 기존 사이트별 kr.Register / us.Register 를 RegisterAll 하나로 통합.
// SourceInfo·RequestsPerHour 는 fetcher_rules 테이블 (migration 014) 에서 조회.
package sources

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"time"

	"issuetracker/internal/bus"
	"issuetracker/internal/processor/fetcher/core"
	"issuetracker/internal/processor/fetcher/domain/general"
	"issuetracker/internal/processor/fetcher/domain/general/fetcher"
	"issuetracker/internal/processor/fetcher/handler"
	cdp "issuetracker/internal/processor/fetcher/implementation/chromedp"
	"issuetracker/internal/processor/fetcher/implementation/goquery"
	"issuetracker/internal/processor/fetcher/rate_limiter"
	"issuetracker/internal/processor/fetcher/rule"
	"issuetracker/internal/storage/model"
	"issuetracker/internal/storage/repository"
	"issuetracker/internal/storage/service"
	"issuetracker/pkg/logger"
)

// dnsCacheTTL 는 fetcher rate limiter 가 사용하는 DNS resolver 의 entry TTL.
// IP 변경 빈도와 DNS lookup 비용 trade-off — 5분이 일반.
const dnsCacheTTL = 5 * time.Minute

// defaultRateLimitBurst 는 IPRateLimiterRegistry 생성 시 burst 기본값.
//
// fetcher_rules 테이블에 burst 컬럼은 아직 없음 — RPH 기반 단일 정책. 향후 컬럼 추가 시
// per-source 동적 값으로 대체.
const defaultRateLimitBurst = 10

// defaultLazyKeywords 는 대부분 뉴스 사이트가 사용하는 lazy-load 감지 attr 목록입니다.
// chromedp 가 활성화된 경우에만 BuildChain 에 전달합니다 — pool 이 꺼진 환경에서
// lazy sentinel 이 발생하면 TopicCrawlChromedp 로 republish 되지만 consumer 가 없어
// 메시지가 유실됩니다 .
var defaultLazyKeywords = []string{"data-lazy-src", "lazyload", "data-lazy"}

// RegisterAll 은 fetcher_rules 테이블에서 source_name 이 채워진 모든 source 를 읽어
// Registry 에 등록합니다.
//
// 각 source_name 별로 goquery + (optional) chromedp ChainHandler 를 생성.
// chromedpRemoteURLs 가 empty 이면 chromedp chain 없이 goquery-only 로 등록.
// source_name 이 비어있는 row 는 건너뜁니다.
// 동일 source_name 의 row 간 Country/Language/BaseURL/SourceType/RequestsPerHour 불일치 시 에러.
// 등록 가능한 source 가 하나도 없으면 에러 (migration 014 seed 누락 감지).
func RegisterAll(
	ctx context.Context,
	registry *handler.Registry,
	fetcherRuleRepo repository.FetcherRuleRepository,
	baseConfig core.Config,
	rawSvc service.RawContentService,
	pub *bus.Publisher,
	resolver rule.Resolver,
	chromedpRemoteURLs []string,
	log *logger.Logger,
) error {
	rules, err := fetcherRuleRepo.List(ctx)
	if err != nil {
		return fmt.Errorf("list fetcher rules: %w", err)
	}

	bySource, hostsBySource, baseURLsBySource, aerr := AnalyzeSources(rules)
	if aerr != nil {
		return fmt.Errorf("analyze sources: %w", aerr)
	}

	if len(bySource) == 0 {
		return fmt.Errorf("no sources found in fetcher_rules; apply migration 014 seed data")
	}

	// 사이트별 rate limiter 의 IP 해석에 사용할 공유 DNS resolver — 모든 source 공유.
	// 캐시 TTL 안에서 동일 host 의 반복 lookup 비용을 흡수.
	dnsResolver := rate_limiter.NewDNSIPResolver(dnsCacheTTL)

	// host 단위 SourceConfig (RPH 등) 를 동적 lookup 하는 resolver — 모든 source 공유.
	// fetcher_rules.requests_per_hour UPDATE 가 다음 limiter 생성 시 자연 반영. ttl 기본 5분.
	sourceConfigResolver, err := rate_limiter.NewSourceConfigResolver(fetcherRuleRepo, log, 0)
	if err != nil {
		return fmt.Errorf("construct source config resolver: %w", err)
	}

	// source_name 별로 handler 를 빌드한 뒤, source_name 과 각 host_pattern 양쪽으로 등록.
	// CrawlerName 이 host 기반으로 통일 (#248) — source_name 키는 하위 호환 유지.
	for sourceName, entry := range bySource {
		h, err := buildHandler(sourceName, entry.Rec,
			baseConfig, rawSvc, pub, resolver, chromedpRemoteURLs, dnsResolver, sourceConfigResolver, log)
		if err != nil {
			return err
		}
		registry.Register(sourceName, h)
		for _, host := range hostsBySource[sourceName] {
			registry.Register(host, h)
		}
		rateLimitMode := "unlimited"
		if entry.Rec.RequestsPerHour > 0 {
			rateLimitMode = "enforced"
		}
		fields := map[string]interface{}{
			"source":            sourceName,
			"hosts":             hostsBySource[sourceName],
			"requests_per_hour": entry.Rec.RequestsPerHour,
			"burst":             defaultRateLimitBurst,
			"rate_limit":        rateLimitMode,
		}
		// 같은 source_name 의 host 들이 다른 base_url 을 가지면 운영 가시성을 위해 명시 (이슈 #347).
		// HealthCheck 는 canonical 만 사용 — 비-canonical 의 base_url 은 기능적으로 무시됨.
		if urls := baseURLsBySource[sourceName]; len(urls) > 1 {
			distinct := make([]string, 0, len(urls))
			for u := range urls {
				distinct = append(distinct, u)
			}
			// 로그 안정성 — map iteration 비결정성 흡수 (Copilot 반영).
			sort.Strings(distinct)
			fields["base_urls"] = distinct
			fields["canonical_base_url"] = entry.Rec.BaseURL
			log.WithFields(fields).Info("crawler registered from db (multiple base_urls — canonical used for HealthCheck only)")
		} else {
			log.WithFields(fields).Info("crawler registered from db")
		}
	}
	return nil
}

// buildHandler 는 source 의 ChainHandler 를 빌드하여 반환합니다.
// 등록(registry.Register)은 호출자가 직접 수행 — source_name 과 host_pattern 양쪽 등록 지원.
//
// dnsResolver 는 모든 source 가 공유 — 사이트별 IPRateLimiterRegistry 가 IP 해석에 사용.
// sourceConfigResolver 도 모든 source 가 공유 — fetcher_rules.requests_per_hour 동적 lookup.
// rec.RequestsPerHour 가 0 이면 nil limiter 주입 (DNS lookup 비용 회피).
func buildHandler(
	sourceName string,
	rec *model.FetcherRuleRecord,
	baseConfig core.Config,
	rawSvc service.RawContentService,
	pub *bus.Publisher,
	resolver rule.Resolver,
	chromedpRemoteURLs []string,
	dnsResolver core.IPResolver,
	sourceConfigResolver rate_limiter.SourceConfigResolver,
	log *logger.Logger,
) (handler.Handler, error) {
	sourceInfo := core.SourceInfo{
		Country:  rec.Country,
		Type:     core.SourceType(rec.SourceType),
		Name:     rec.SourceName,
		BaseURL:  rec.BaseURL,
		Language: rec.Language,
	}

	cfg := baseConfig
	cfg.SourceInfo = sourceInfo
	// DB 값을 그대로 반영 — 0 은 스키마 상 "제한 없음" 이므로 > 0 조건 없이 항상 적용 (CodeRabbit Major 반영).
	cfg.RequestsPerHour = rec.RequestsPerHour

	// 사이트별 IPRateLimiterRegistry — 동일 source 의 모든 host (goquery + chromedp) 가 공유.
	// IP 단위 token bucket 이므로 host 가 같은 IP 면 limiter 도 공유.
	//
	// 정책: rec.RequestsPerHour 값과 무관하게 항상 resolver 주입형 limiter 생성 — 0→N 동적
	// 전환 (운영 중 fetcher_rules.requests_per_hour UPDATE) 을 지원하기 위한 trade-off.
	// 0 (unlimited) source 도 limiter 객체는 유지하되 NewRateLimiter(0, _) 가 noopLimiter 를
	// 반환해 Wait 가 즉시 통과 — DNS lookup 비용 (5min cache) 만 부담.
	//
	// configResolver 모드: 새 limiter 생성 시점에 sourceConfigResolver.Resolve(host).RPH 사용 →
	// 운영 중 UPDATE 가 다음 신규 IP limiter 부터 반영. 기존 limiter 는 evict TTL (1h) 후 재생성
	// 시 새 RPH 적용. 0→N 또는 N→0 양쪽 전환 모두 지원.
	rateLimiter := rate_limiter.NewIPRateLimiterRegistryWithResolver(dnsResolver, sourceConfigResolver, defaultRateLimitBurst)

	gqCrawler := goquery.NewGoqueryCrawlerWithRateLimiter(sourceName+"-goquery", sourceInfo, cfg, rateLimiter)
	gqFetcher := fetcher.NewGoqueryFetcher(gqCrawler)

	crawler := general.NewGenericCrawler(sourceName, sourceInfo, gqFetcher, rec.BaseURL, cfg)

	// lazy keyword 는 chromedp 가 활성화된 경우에만 전달.
	// pool 이 꺼진 환경에서 lazy sentinel 발생 시 republish 대상 consumer 가 없어 메시지 유실 (Copilot 반영).
	var lazyKeywords []string
	if len(chromedpRemoteURLs) > 0 {
		lazyKeywords = defaultLazyKeywords
	}
	defaultChain, err := general.BuildChain(gqFetcher, nil, log, lazyKeywords...)
	if err != nil {
		return nil, fmt.Errorf("%s default chain wiring: %w", sourceName, err)
	}

	chromedpChains, err := buildChromedpChains(sourceName, sourceInfo, cfg, chromedpRemoteURLs, rateLimiter, log)
	if err != nil {
		return nil, err
	}

	return general.NewChainHandler(crawler, defaultChain, chromedpChains, resolver, rawSvc, pub, log), nil
}

// SourceEntry 는 source_name 별 canonical row 와 그 row 가 canonical (base_url hostname ==
// host_pattern) 인지 여부를 보관합니다 — AnalyzeSources 가 채워서 반환.
type SourceEntry struct {
	Rec      *model.FetcherRuleRecord
	HasExact bool
}

// AnalyzeSources 는 fetcher_rules 의 rows 를 source_name 별로 분석하여 다음을 반환합니다:
//   - bySource         : source_name → canonical FetcherRuleRecord (base_url hostname == host_pattern 우선)
//   - hostsBySource    : source_name → []HostPattern (모든 host 보존)
//   - baseURLsBySource : source_name → 등장한 BaseURL set (다양성 감지 로깅용)
//
// 동일 source_name 내 SourceInfo 불일치 (Country / Language / SourceType / RequestsPerHour) 시
// error 즉시 반환. **BaseURL 은 strict check 에서 제외** (이슈 #347) — host 별로 다를 수 있으며
// canonical 만 HealthCheck 에 사용. baseURLsBySource 로 다양성 감지하여 운영 로그.
//
// source_name 이 빈 row 는 skip (legacy).
//
// 노출 사유 (gemini Medium 응답): 프로젝트 규칙 .claude/rules/05-testing.md 에 따라 모든 테스트는
// test/internal/<pkg>/ 하위에 외부 _test 패키지로 작성. 내부 helper 의 unit test 를 위한
// internal/<pkg>/*_test.go (same-package) 패턴은 본 repo 의 디렉토리 컨벤션 위반이므로,
// testability 최소 노출로 본 함수를 export. API 안정 약속 아님 — 내부 도우미.
func AnalyzeSources(rules []*model.FetcherRuleRecord) (
	bySource map[string]SourceEntry,
	hostsBySource map[string][]string,
	baseURLsBySource map[string]map[string]struct{},
	err error,
) {
	bySource = make(map[string]SourceEntry)
	hostsBySource = make(map[string][]string)
	baseURLsBySource = make(map[string]map[string]struct{})
	for _, r := range rules {
		if r.SourceName == "" {
			continue
		}
		hostsBySource[r.SourceName] = append(hostsBySource[r.SourceName], r.HostPattern)
		if _, ok := baseURLsBySource[r.SourceName]; !ok {
			baseURLsBySource[r.SourceName] = map[string]struct{}{}
		}
		baseURLsBySource[r.SourceName][r.BaseURL] = struct{}{}
		if prev, seen := bySource[r.SourceName]; seen {
			// 어느 필드가 mismatch 했는지 명시 (gemini Medium 반영) — 운영 boot fail 시 즉시 진단.
			var mismatched string
			switch {
			case prev.Rec.Country != r.Country:
				mismatched = "Country"
			case prev.Rec.Language != r.Language:
				mismatched = "Language"
			case prev.Rec.SourceType != r.SourceType:
				mismatched = "SourceType"
			case prev.Rec.RequestsPerHour != r.RequestsPerHour:
				mismatched = "RequestsPerHour"
			}
			if mismatched != "" {
				return nil, nil, nil, fmt.Errorf("inconsistent %s for source_name=%q (host=%q vs host=%q)",
					mismatched, r.SourceName, prev.Rec.HostPattern, r.HostPattern)
			}
			// canonical 우선: base_url hostname == host_pattern 인 row 로 교체.
			if !prev.HasExact && isCanonicalHost(r) {
				bySource[r.SourceName] = SourceEntry{Rec: r, HasExact: true}
			}
			continue
		}
		bySource[r.SourceName] = SourceEntry{Rec: r, HasExact: isCanonicalHost(r)}
	}
	return bySource, hostsBySource, baseURLsBySource, nil
}

// isCanonicalHost 는 r.BaseURL 의 hostname 이 r.HostPattern 과 일치하면 true 를 반환합니다.
// 동일 source_name 의 여러 row 중 canonical(대표) host 를 결정적으로 선택하는 데 사용합니다.
func isCanonicalHost(r *model.FetcherRuleRecord) bool {
	if r.BaseURL == "" {
		return false
	}
	u, err := url.Parse(r.BaseURL)
	if err != nil {
		return false
	}
	return u.Hostname() == r.HostPattern
}

// buildChromedpChains 는 source 의 chromedp chain 을 worker_id 별 N 개 build 합니다.
// remoteURLs 가 empty 이면 nil 반환 (chromedp 미사용).
//
// rateLimiter 는 buildHandler 가 생성한 source 단위 IPRateLimiterRegistry — 동일 source 의
// goquery / chromedp 가 같은 limiter 공유하여 IP 단위 token bucket 정책이 fetcher 종류와
// 무관하게 일관 적용.
func buildChromedpChains(
	sourceName string,
	sourceInfo core.SourceInfo,
	cfg core.Config,
	remoteURLs []string,
	rateLimiter core.URLRateLimiter,
	log *logger.Logger,
) ([]general.Handler, error) {
	if len(remoteURLs) == 0 {
		return nil, nil
	}
	chains := make([]general.Handler, len(remoteURLs))
	for i, remoteURL := range remoteURLs {
		opts := cdp.DefaultRemoteOptions()
		opts.RemoteURL = remoteURL
		cdpCrawler := cdp.NewChromedpCrawlerWithRateLimiter(
			fmt.Sprintf("%s-browser-%d", sourceName, i), sourceInfo, cfg, opts, rateLimiter,
		)
		brFetcher := fetcher.NewBrowserFetcher(cdpCrawler, cfg)
		chain, err := general.BuildChain(nil, brFetcher, log)
		if err != nil {
			return nil, fmt.Errorf("%s chromedp chain wiring (worker_id=%d): %w", sourceName, i, err)
		}
		chains[i] = chain
	}
	return chains, nil
}
