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
// 등록 중 wiring 실패 시 error 반환 — 호출자가 fail-fast 결정.
func Register(
	registry *handler.Registry,
	config core.Config,
	rawSvc service.RawContentService,
	producer queue.Producer,
	resolver rule.Resolver,
	log *logger.Logger,
) error {
	return registerCNN(registry, config, rawSvc, producer, resolver, log)
}

// 사이트별 등록 패턴은 kr/registry.go 와 동일.
func registerCNN(registry *handler.Registry, _ core.Config, rawSvc service.RawContentService, producer queue.Producer, resolver rule.Resolver, log *logger.Logger) error {
	cfg := cnn.Default()

	gqCrawler := goquery.NewGoqueryCrawler("cnn-goquery", cfg.CrawlerConfig.SourceInfo, cfg.CrawlerConfig)
	gqFetcher := fetcher.NewGoqueryFetcher(gqCrawler)

	cdpCrawler := cdp.NewChromedpCrawlerWithOptions("cnn-browser", cfg.CrawlerConfig.SourceInfo, cfg.CrawlerConfig, cdp.DefaultRemoteOptions())
	brFetcher := fetcher.NewBrowserFetcher(cdpCrawler, cfg.CrawlerConfig)

	crawler := general.NewGenericCrawler("cnn", cfg.CrawlerConfig.SourceInfo, gqFetcher, cfg.BaseURL, cfg.CrawlerConfig)
	defaultChain, err := general.BuildChain(gqFetcher, brFetcher, log,
		"data-lazy-src", "lazyload", "data-lazy",
	)
	if err != nil {
		return fmt.Errorf("cnn default chain wiring: %w", err)
	}
	chromedpChain, err := general.BuildChain(nil, brFetcher, log)
	if err != nil {
		return fmt.Errorf("cnn chromedp chain wiring: %w", err)
	}

	registry.Register("cnn", general.NewChainHandler(crawler, defaultChain, chromedpChain, resolver, rawSvc, producer, log))
	log.WithField("crawler", "cnn").Info("cnn crawler registered")
	return nil
}
