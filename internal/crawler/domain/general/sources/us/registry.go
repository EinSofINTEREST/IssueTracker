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

func registerCNN(registry *handler.Registry, config core.Config, parser *rule.Parser, repo storage.NewsArticleRepository, publisher general.JobPublisher, log *logger.Logger) {
	cfg := cnn.Default()
	cfg.CrawlerConfig = config
	cfg.CrawlerConfig.SourceInfo = core.SourceInfo{
		Country: "US", Type: core.SourceTypeNews, Name: "cnn",
		BaseURL: "https://www.cnn.com", Language: "en",
	}

	gqCrawler := goquery.NewGoqueryCrawler("cnn-goquery", cfg.CrawlerConfig.SourceInfo, config)
	gqFetcher := fetcher.NewGoqueryFetcher(gqCrawler)

	cdpSource := cfg.CrawlerConfig.SourceInfo
	cdpCrawler := cdp.NewChromedpCrawlerWithOptions("cnn-browser", cdpSource, config, cdp.DefaultRemoteOptions())
	brFetcher := fetcher.NewBrowserFetcher(cdpCrawler, config)

	crawler := general.NewGenericCrawler("cnn", cfg.CrawlerConfig.SourceInfo, gqFetcher, cfg.BaseURL, config)
	chain := general.BuildChain(nil, gqFetcher, brFetcher, log,
		"data-lazy-src", "lazyload", "data-lazy",
	)

	registry.Register("cnn", general.NewChainHandler(crawler, chain, parser, publisher, repo, log))
	log.WithField("crawler", "cnn").Info("cnn crawler registered")
}
