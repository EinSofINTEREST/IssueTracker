// Package us 는 미국 사이트 크롤러를 조립하고 Registry 에 등록합니다.
//
// Package us assembles US site crawlers and registers them with the handler Registry.
package us

import (
	"issuetracker/internal/crawler/core"
	"issuetracker/internal/crawler/domain/general"
	"issuetracker/internal/crawler/domain/general/fetcher"
	"issuetracker/internal/crawler/domain/general/sources/us/cnn"
	"issuetracker/internal/crawler/handler"
	cdp "issuetracker/internal/crawler/implementation/chromedp"
	"issuetracker/internal/crawler/implementation/goquery"
	"issuetracker/internal/crawler/parser/rule"
	"issuetracker/internal/storage"
	"issuetracker/pkg/logger"
)

// Register 는 모든 미국 사이트 크롤러를 registry 에 등록합니다.
func Register(
	registry *handler.Registry,
	config core.Config,
	parser *rule.Parser,
	repo storage.NewsArticleRepository,
	publisher general.JobPublisher,
	log *logger.Logger,
) {
	registerCNN(registry, config, parser, repo, publisher, log)
}

// 사이트별 등록 패턴은 kr/registry.go 와 동일 — cfg.CrawlerConfig 보존, 호출자 config 무시.
func registerCNN(registry *handler.Registry, _ core.Config, parser *rule.Parser, repo storage.NewsArticleRepository, publisher general.JobPublisher, log *logger.Logger) {
	cfg := cnn.Default()

	gqCrawler := goquery.NewGoqueryCrawler("cnn-goquery", cfg.CrawlerConfig.SourceInfo, cfg.CrawlerConfig)
	gqFetcher := fetcher.NewGoqueryFetcher(gqCrawler)

	cdpCrawler := cdp.NewChromedpCrawlerWithOptions("cnn-browser", cfg.CrawlerConfig.SourceInfo, cfg.CrawlerConfig, cdp.DefaultRemoteOptions())
	brFetcher := fetcher.NewBrowserFetcher(cdpCrawler, cfg.CrawlerConfig)

	crawler := general.NewGenericCrawler("cnn", cfg.CrawlerConfig.SourceInfo, gqFetcher, cfg.BaseURL, cfg.CrawlerConfig)
	chain := general.BuildChain(nil, gqFetcher, brFetcher, log,
		"data-lazy-src", "lazyload", "data-lazy",
	)

	registry.Register("cnn", general.NewChainHandler(crawler, chain, parser, publisher, repo, log))
	log.WithField("crawler", "cnn").Info("cnn crawler registered")
}
