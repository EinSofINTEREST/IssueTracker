// Package us는 미국 뉴스 소스 크롤러를 조립하고 Registry에 등록합니다.
// 이 패키지만이 구체 구현 패키지(goquery, chromedp, fetcher, cnn)를 모두 import합니다.
//
// Package us assembles US news crawlers and registers them with the handler Registry.
// This is the only package that imports all concrete US implementation packages.
package us

import (
	"issuetracker/internal/crawler/core"
	"issuetracker/internal/crawler/domain/news"
	"issuetracker/internal/crawler/domain/news/fetcher"
	"issuetracker/internal/crawler/domain/news/us/cnn"
	"issuetracker/internal/crawler/handler"
	cdp "issuetracker/internal/crawler/implementation/chromedp"
	"issuetracker/internal/crawler/implementation/goquery"
	"issuetracker/internal/storage"
	"issuetracker/pkg/logger"
)

// Register는 모든 미국 뉴스 크롤러를 registry에 등록합니다.
// cmd/crawler/main.go에서 이 함수를 호출하여 US 뉴스 크롤러를 활성화합니다.
// repo가 nil이면 DB 저장 없이 크롤링만 수행합니다.
//
// Register wires all US news crawlers and registers them with the provided Registry.
// If repo is nil, articles are crawled but not persisted to the database.
func Register(registry *handler.Registry, config core.Config, repo storage.NewsArticleRepository, log *logger.Logger) {
	registerCNN(registry, config, repo, log)
}

// registerCNN은 CNN 뉴스 핸들러를 조립하고 등록합니다.
// 체인: RSS(주) → GoQuery(HTML 폴백) → Browser(최종 폴백)
// RSS는 기사 목록 수집에 사용되고, GoQuery/Browser는 전체 기사 본문 수집에 사용됩니다.
func registerCNN(registry *handler.Registry, config core.Config, repo storage.NewsArticleRepository, log *logger.Logger) {
	cnnCfg := cnn.DefaultCNNConfig()
	cnnCfg.CrawlerConfig = config
	cnnCfg.CrawlerConfig.SourceInfo = core.SourceInfo{
		Country:  "US",
		Type:     core.SourceTypeNews,
		Name:     "cnn",
		BaseURL:  "https://www.cnn.com",
		Language: "en",
	}

	// RSS fetcher (주 전략: 기사 목록)
	rssSource := cnnCfg.CrawlerConfig.SourceInfo
	rssFetcher := fetcher.NewRSSFetcher(rssSource, log)

	// goquery fetcher (기사 본문 수집)
	gqSource := cnnCfg.CrawlerConfig.SourceInfo
	gqSource.Name = "cnn-goquery"
	gqCrawler := goquery.NewGoqueryCrawler("cnn-goquery", gqSource, config)
	gqFetcher := fetcher.NewGoqueryFetcher(gqCrawler)

	// chromedp fetcher (최종 폴백: JavaScript 렌더링 필요 시)
	cdpSource := cnnCfg.CrawlerConfig.SourceInfo
	cdpSource.Name = "cnn-browser"
	cdpCrawler := cdp.NewChromedpCrawlerWithOptions(
		"cnn-browser",
		cdpSource,
		config,
		cdp.DefaultRemoteOptions(),
	)
	brFetcher := fetcher.NewBrowserFetcher(cdpCrawler, config)

	// 파서 & 크롤러 생성
	parser := cnn.NewCNNParser(cnnCfg)
	crawler := cnn.NewCNNCrawler(cnnCfg, gqFetcher, parser, log)

	// 체인 조립: RSS → GoQuery → Browser
	// CNN은 RSS로 목록을 수집하고, GoQuery로 전체 기사를 파싱합니다.
	// lazy loading 감지 시 GoQuery에서 Browser로 자동 위임합니다.
	chain := news.BuildChain(rssFetcher, gqFetcher, brFetcher, log,
		"data-lazy-src",
		"lazyload",
		"data-lazy",
	)

	registry.Register("cnn", news.NewChainHandler(crawler, chain, parser, repo, log))

	log.WithField("crawler", "cnn").Info("cnn news crawler registered")
}
