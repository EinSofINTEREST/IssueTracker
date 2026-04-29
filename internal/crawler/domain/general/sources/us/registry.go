// Package us 는 미국 사이트 크롤러를 조립하고 Registry 에 등록합니다.
//
// Package us assembles US site crawlers and registers them with the handler Registry.
package us

import (
	"fmt"

	"issuetracker/internal/crawler/core"
	"issuetracker/internal/crawler/domain/general"
	"issuetracker/internal/crawler/domain/general/fetcher"
	"issuetracker/internal/crawler/domain/general/sources/us/cnn"
	"issuetracker/internal/crawler/handler"
	cdp "issuetracker/internal/crawler/implementation/chromedp"
	"issuetracker/internal/crawler/implementation/goquery"
	"issuetracker/internal/storage/service"
	"issuetracker/pkg/logger"
	"issuetracker/pkg/queue"
)

// Register 는 모든 미국 사이트 크롤러를 registry 에 등록합니다 (이슈 #134 — fetcher/parser 분리 후).
// 등록 중 wiring 실패 시 error 반환 — 호출자가 fail-fast 결정.
func Register(
	registry *handler.Registry,
	config core.Config,
	rawSvc service.RawContentService,
	producer queue.Producer,
	log *logger.Logger,
) error {
	return registerCNN(registry, config, rawSvc, producer, log)
}

// 사이트별 등록 패턴은 kr/registry.go 와 동일.
func registerCNN(registry *handler.Registry, _ core.Config, rawSvc service.RawContentService, producer queue.Producer, log *logger.Logger) error {
	cfg := cnn.Default()

	gqCrawler := goquery.NewGoqueryCrawler("cnn-goquery", cfg.CrawlerConfig.SourceInfo, cfg.CrawlerConfig)
	gqFetcher := fetcher.NewGoqueryFetcher(gqCrawler)

	cdpCrawler := cdp.NewChromedpCrawlerWithOptions("cnn-browser", cfg.CrawlerConfig.SourceInfo, cfg.CrawlerConfig, cdp.DefaultRemoteOptions())
	brFetcher := fetcher.NewBrowserFetcher(cdpCrawler, cfg.CrawlerConfig)

	crawler := general.NewGenericCrawler("cnn", cfg.CrawlerConfig.SourceInfo, gqFetcher, cfg.BaseURL, cfg.CrawlerConfig)
	chain, err := general.BuildChain(nil, gqFetcher, brFetcher, log,
		"data-lazy-src", "lazyload", "data-lazy",
	)
	if err != nil {
		return fmt.Errorf("cnn chain wiring: %w", err)
	}

	registry.Register("cnn", general.NewChainHandler(crawler, chain, rawSvc, producer, log))
	log.WithField("crawler", "cnn").Info("cnn crawler registered")
	return nil
}
