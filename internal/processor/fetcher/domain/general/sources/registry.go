// Package sources 는 fetcher_rules DB 에서 모든 사이트 크롤러를 읽어 Registry 에 등록합니다 (이슈 #246).
//
// 기존 사이트별 kr.Register / us.Register 를 RegisterAll 하나로 통합.
// SourceInfo·RequestsPerHour 는 fetcher_rules 테이블 (migration 014) 에서 조회.
package sources

import (
	"context"
	"fmt"
	"net/url"

	"issuetracker/internal/processor/fetcher/core"
	"issuetracker/internal/processor/fetcher/domain/general"
	"issuetracker/internal/processor/fetcher/domain/general/fetcher"
	"issuetracker/internal/processor/fetcher/handler"
	cdp "issuetracker/internal/processor/fetcher/implementation/chromedp"
	"issuetracker/internal/processor/fetcher/implementation/goquery"
	"issuetracker/internal/processor/fetcher/rule"
	"issuetracker/internal/storage"
	"issuetracker/internal/storage/service"
	"issuetracker/pkg/logger"
	"issuetracker/pkg/queue"
)

// defaultLazyKeywords 는 대부분 뉴스 사이트가 사용하는 lazy-load 감지 attr 목록입니다.
// chromedp 가 활성화된 경우에만 BuildChain 에 전달합니다 — pool 이 꺼진 환경에서
// lazy sentinel 이 발생하면 TopicCrawlChromedp 로 republish 되지만 consumer 가 없어
// 메시지가 유실됩니다 (이슈 #218, Copilot 피드백 반영).
var defaultLazyKeywords = []string{"data-lazy-src", "lazyload", "data-lazy"}

// RegisterAll 은 fetcher_rules 테이블에서 source_name 이 채워진 모든 source 를 읽어
// Registry 에 등록합니다 (이슈 #246).
//
// 각 source_name 별로 goquery + (optional) chromedp ChainHandler 를 생성.
// chromedpRemoteURLs 가 empty 이면 chromedp chain 없이 goquery-only 로 등록.
// source_name 이 비어있는 row 는 건너뜁니다.
// 동일 source_name 의 row 간 Country/Language/BaseURL/SourceType/RequestsPerHour 불일치 시 에러.
// 등록 가능한 source 가 하나도 없으면 에러 (migration 014 seed 누락 감지).
func RegisterAll(
	ctx context.Context,
	registry *handler.Registry,
	fetcherRuleRepo storage.FetcherRuleRepository,
	baseConfig core.Config,
	rawSvc service.RawContentService,
	producer queue.Producer,
	resolver rule.Resolver,
	chromedpRemoteURLs []string,
	log *logger.Logger,
) error {
	rules, err := fetcherRuleRepo.List(ctx)
	if err != nil {
		return fmt.Errorf("list fetcher rules: %w", err)
	}

	// source_name 기준으로 canonical row 수집.
	// 1. base_url hostname == host_pattern 인 row 를 우선 canonical 로 선택.
	// 2. 동일 source_name 내 SourceInfo 필드가 불일치하면 즉시 에러 (CodeRabbit Major 반영).
	type sourceEntry struct {
		rec      *storage.FetcherRuleRecord
		hasExact bool
	}
	bySource := make(map[string]sourceEntry)
	for _, r := range rules {
		if r.SourceName == "" {
			continue
		}
		if prev, seen := bySource[r.SourceName]; seen {
			if prev.rec.Country != r.Country ||
				prev.rec.Language != r.Language ||
				prev.rec.BaseURL != r.BaseURL ||
				prev.rec.SourceType != r.SourceType ||
				prev.rec.RequestsPerHour != r.RequestsPerHour {
				return fmt.Errorf("inconsistent source metadata for source_name=%q (host=%q vs host=%q)",
					r.SourceName, prev.rec.HostPattern, r.HostPattern)
			}
			// canonical 우선: base_url hostname == host_pattern 인 row 로 교체.
			if !prev.hasExact && isCanonicalHost(r) {
				bySource[r.SourceName] = sourceEntry{rec: r, hasExact: true}
			}
			continue
		}
		bySource[r.SourceName] = sourceEntry{rec: r, hasExact: isCanonicalHost(r)}
	}

	if len(bySource) == 0 {
		return fmt.Errorf("no sources found in fetcher_rules; apply migration 014 seed data")
	}

	// source_name 별로 handler 를 빌드한 뒤, 해당 source 의 모든 host_pattern 에도 동일 handler 등록.
	// CrawlerName 이 host 기반으로 통일 (#248) 되면서 Registry 가 host key 로 dispatch 해야 함.
	// source_name key 도 유지 — 기존 CrawlJob 하위 호환.
	hostsBySource := make(map[string][]string)
	for _, r := range rules {
		if r.SourceName == "" {
			continue
		}
		hostsBySource[r.SourceName] = append(hostsBySource[r.SourceName], r.HostPattern)
	}

	for sourceName, entry := range bySource {
		h, err := buildHandler(registry, sourceName, entry.rec,
			baseConfig, rawSvc, producer, resolver, chromedpRemoteURLs, log)
		if err != nil {
			return err
		}
		// source_name 으로 등록 (기존 경로 호환).
		registry.Register(sourceName, h)
		// 각 host_pattern 으로도 등록 — host 기반 CrawlerName dispatch 지원 (이슈 #248).
		for _, host := range hostsBySource[sourceName] {
			registry.Register(host, h)
		}
		log.WithFields(map[string]interface{}{
			"source":            sourceName,
			"hosts":             hostsBySource[sourceName],
			"requests_per_hour": entry.rec.RequestsPerHour,
		}).Info("crawler registered from db")
	}
	return nil
}

// buildHandler 는 source 의 ChainHandler 를 빌드하여 반환합니다.
// 등록(registry.Register)은 호출자가 직접 수행 — source_name 과 host_pattern 양쪽 등록 지원.
func buildHandler(
	_ *handler.Registry,
	sourceName string,
	rec *storage.FetcherRuleRecord,
	baseConfig core.Config,
	rawSvc service.RawContentService,
	producer queue.Producer,
	resolver rule.Resolver,
	chromedpRemoteURLs []string,
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

	gqCrawler := goquery.NewGoqueryCrawler(sourceName+"-goquery", sourceInfo, cfg)
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

	chromedpChains, err := buildChromedpChains(sourceName, sourceInfo, cfg, chromedpRemoteURLs, log)
	if err != nil {
		return nil, err
	}

	return general.NewChainHandler(crawler, defaultChain, chromedpChains, resolver, rawSvc, producer, log), nil
}

// isCanonicalHost 는 r.BaseURL 의 hostname 이 r.HostPattern 과 일치하면 true 를 반환합니다.
// 동일 source_name 의 여러 row 중 canonical(대표) host 를 결정적으로 선택하는 데 사용합니다.
func isCanonicalHost(r *storage.FetcherRuleRecord) bool {
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
func buildChromedpChains(
	sourceName string,
	sourceInfo core.SourceInfo,
	cfg core.Config,
	remoteURLs []string,
	log *logger.Logger,
) ([]general.Handler, error) {
	if len(remoteURLs) == 0 {
		return nil, nil
	}
	chains := make([]general.Handler, len(remoteURLs))
	for i, remoteURL := range remoteURLs {
		opts := cdp.DefaultRemoteOptions()
		opts.RemoteURL = remoteURL
		cdpCrawler := cdp.NewChromedpCrawlerWithOptions(
			fmt.Sprintf("%s-browser-%d", sourceName, i), sourceInfo, cfg, opts,
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
