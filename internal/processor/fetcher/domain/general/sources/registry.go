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
// BuildChain 에 전달되어 goquery-only chain 이 lazy page 를 감지하면 sentinel 을 반환,
// ChainHandler 가 chromedp pool 로 republish 하도록 합니다 (이슈 #218).
var defaultLazyKeywords = []string{"data-lazy-src", "lazyload", "data-lazy"}

// RegisterAll 은 fetcher_rules 테이블에서 SourceInfo 가 채워진 모든 source 를 읽어
// Registry 에 등록합니다 (이슈 #246).
//
// 각 source_name 별로 goquery + (optional) chromedp ChainHandler 를 생성.
// chromedpRemoteURLs 가 empty 이면 chromedp chain 없이 goquery-only 로 등록.
// source_name 이 비어있는 row (SourceInfo 미입력) 는 건너뜁니다.
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
		return fmt.Errorf("RegisterAll: list fetcher rules: %w", err)
	}

	// source_name 기준으로 대표 row 수집.
	// base_url 의 hostname 과 host_pattern 이 일치하는 row 를 canonical 로 우선 선택.
	// 일치하는 row 가 없으면 첫 번째 row 를 fallback 으로 사용.
	type sourceEntry struct {
		rec      *storage.FetcherRuleRecord
		hasExact bool // base_url hostname == host_pattern
	}
	bySource := make(map[string]sourceEntry)
	for _, r := range rules {
		if r.SourceName == "" {
			continue
		}
		exact := isCanonicalHost(r)
		existing, seen := bySource[r.SourceName]
		if !seen || (!existing.hasExact && exact) {
			bySource[r.SourceName] = sourceEntry{rec: r, hasExact: exact}
		}
	}

	for sourceName, entry := range bySource {
		if err := registerSource(
			ctx, registry, sourceName, entry.rec,
			baseConfig, rawSvc, producer, resolver, chromedpRemoteURLs, log,
		); err != nil {
			return err
		}
	}
	return nil
}

func registerSource(
	_ context.Context,
	registry *handler.Registry,
	sourceName string,
	rec *storage.FetcherRuleRecord,
	baseConfig core.Config,
	rawSvc service.RawContentService,
	producer queue.Producer,
	resolver rule.Resolver,
	chromedpRemoteURLs []string,
	log *logger.Logger,
) error {
	sourceInfo := core.SourceInfo{
		Country:  rec.Country,
		Type:     core.SourceType(rec.SourceType),
		Name:     rec.SourceName,
		BaseURL:  rec.BaseURL,
		Language: rec.Language,
	}

	cfg := baseConfig
	cfg.SourceInfo = sourceInfo
	if rec.RequestsPerHour > 0 {
		cfg.RequestsPerHour = rec.RequestsPerHour
	}

	gqCrawler := goquery.NewGoqueryCrawler(sourceName+"-goquery", sourceInfo, cfg)
	gqFetcher := fetcher.NewGoqueryFetcher(gqCrawler)

	crawler := general.NewGenericCrawler(sourceName, sourceInfo, gqFetcher, rec.BaseURL, cfg)

	defaultChain, err := general.BuildChain(gqFetcher, nil, log, defaultLazyKeywords...)
	if err != nil {
		return fmt.Errorf("RegisterAll: %s default chain wiring: %w", sourceName, err)
	}

	chromedpChains, err := buildChromedpChains(sourceName, sourceInfo, cfg, chromedpRemoteURLs, log)
	if err != nil {
		return err
	}

	registry.Register(sourceName, general.NewChainHandler(crawler, defaultChain, chromedpChains, resolver, rawSvc, producer, log))
	log.WithFields(map[string]interface{}{
		"crawler":           sourceName,
		"chromedp_workers":  len(chromedpChains),
		"requests_per_hour": cfg.RequestsPerHour,
	}).Info("crawler registered from db")
	return nil
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
	for i, url := range remoteURLs {
		opts := cdp.DefaultRemoteOptions()
		opts.RemoteURL = url
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
