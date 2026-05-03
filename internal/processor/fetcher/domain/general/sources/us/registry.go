// Package us 는 미국 사이트 크롤러를 조립하고 Registry 에 등록합니다.
//
// Package us assembles US site crawlers and registers them with the handler Registry.
package us

import (
	"fmt"

	"issuetracker/internal/processor/fetcher/core"
	"issuetracker/internal/processor/fetcher/domain/general"
	"issuetracker/internal/processor/fetcher/domain/general/fetcher"
	"issuetracker/internal/processor/fetcher/domain/general/sources/us/cnn"
	"issuetracker/internal/processor/fetcher/handler"
	cdp "issuetracker/internal/processor/fetcher/implementation/chromedp"
	"issuetracker/internal/processor/fetcher/implementation/goquery"
	"issuetracker/internal/processor/fetcher/rule"
	"issuetracker/internal/storage/service"
	"issuetracker/pkg/logger"
	"issuetracker/pkg/queue"
)

// Register 는 모든 미국 사이트 크롤러를 registry 에 등록합니다 (이슈 #134 — fetcher/parser 분리 후).
// resolver 는 호스트 단위 fetcher 룰 (이슈 #175) 적용. nil 이면 항상 default chain.
// chromedpRemoteURLs 는 worker_id 별 Chrome RemoteURL slice (이슈 #230). 길이 = chromedp pool 의
// WorkerCount. empty 면 chromedp chain 미wiring.
// 등록 중 wiring 실패 시 error 반환 — 호출자가 fail-fast 결정.
func Register(
	registry *handler.Registry,
	config core.Config,
	rawSvc service.RawContentService,
	producer queue.Producer,
	resolver rule.Resolver,
	chromedpRemoteURLs []string,
	log *logger.Logger,
) error {
	return registerCNN(registry, config, rawSvc, producer, resolver, chromedpRemoteURLs, log)
}

// buildChromedpChainsForCNN 는 CNN 의 chromedp chain 을 worker_id 별 N 개 build 합니다 (이슈 #230).
// kr/registry.go 의 buildChromedpChainsForSite 와 동일 로직 — site 패키지 경계로 인해 각자 보유.
func buildChromedpChainsForCNN(
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
		cdpCrawler := cdp.NewChromedpCrawlerWithOptions(fmt.Sprintf("cnn-browser-%d", i), sourceInfo, cfg, opts)
		brFetcher := fetcher.NewBrowserFetcher(cdpCrawler, cfg)
		chain, err := general.BuildChain(nil, brFetcher, log)
		if err != nil {
			// CodeRabbit 피드백: error message 에 RemoteURL 포함 시 ws://user:pass@... 같은
			// 인증 토큰이 로그에 누출. worker_id 만으로 triage 충분.
			return nil, fmt.Errorf("cnn chromedp chain wiring (worker_id=%d): %w", i, err)
		}
		chains[i] = chain
	}
	return chains, nil
}

// 사이트별 등록 패턴은 kr/registry.go 와 동일.
func registerCNN(registry *handler.Registry, _ core.Config, rawSvc service.RawContentService, producer queue.Producer, resolver rule.Resolver, chromedpRemoteURLs []string, log *logger.Logger) error {
	cfg := cnn.Default()

	gqCrawler := goquery.NewGoqueryCrawler("cnn-goquery", cfg.CrawlerConfig.SourceInfo, cfg.CrawlerConfig)
	gqFetcher := fetcher.NewGoqueryFetcher(gqCrawler)

	crawler := general.NewGenericCrawler("cnn", cfg.CrawlerConfig.SourceInfo, gqFetcher, cfg.BaseURL, cfg.CrawlerConfig)
	// 이슈 #218: DefaultChain = goquery only.
	defaultChain, err := general.BuildChain(gqFetcher, nil, log,
		"data-lazy-src", "lazyload", "data-lazy",
	)
	if err != nil {
		return fmt.Errorf("cnn default chain wiring: %w", err)
	}
	chromedpChains, err := buildChromedpChainsForCNN(cfg.CrawlerConfig.SourceInfo, cfg.CrawlerConfig, chromedpRemoteURLs, log)
	if err != nil {
		return err
	}

	registry.Register("cnn", general.NewChainHandler(crawler, defaultChain, chromedpChains, resolver, rawSvc, producer, log))
	log.WithFields(map[string]interface{}{
		"crawler":          "cnn",
		"chromedp_workers": len(chromedpChains),
	}).Info("cnn crawler registered")
	return nil
}
