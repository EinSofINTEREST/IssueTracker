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
// publisher가 nil이면 카테고리 페이지에서의 체이닝 발행을 건너뜁니다.
//
// Register wires all US news crawlers and registers them with the provided Registry.
// If repo is nil, articles are crawled but not persisted to the database.
func Register(registry *handler.Registry, config core.Config, repo storage.NewsArticleRepository, publisher news.JobPublisher, log *logger.Logger) {
	registerCNN(registry, config, repo, publisher, log)
}

// registerCNN은 CNN 뉴스 핸들러를 조립하고 등록합니다.
// 체인: GoQuery(주) → Browser(폴백)
// CNN RSS 피드가 지원 중단되어 HTML 기반 크롤링을 기본 전략으로 사용합니다.
func registerCNN(registry *handler.Registry, config core.Config, repo storage.NewsArticleRepository, publisher news.JobPublisher, log *logger.Logger) {
	cnnCfg := cnn.DefaultCNNConfig()
	cnnCfg.CrawlerConfig = config
	cnnCfg.CrawlerConfig.SourceInfo = core.SourceInfo{
		Country:  "US",
		Type:     core.SourceTypeNews,
		Name:     "cnn",
		BaseURL:  "https://www.cnn.com",
		Language: "en",
	}

	// goquery fetcher (주 전략: 카테고리 목록 + 기사 본문 수집)
	// SourceInfo.Name은 논리 소스명("cnn")을 유지하여 DB/Kafka 소스 식별자 일관성 보장
	gqCrawler := goquery.NewGoqueryCrawler("cnn-goquery", cnnCfg.CrawlerConfig.SourceInfo, config)
	gqFetcher := fetcher.NewGoqueryFetcher(gqCrawler)

	// chromedp fetcher (폴백: JavaScript 렌더링 필요 시)
	cdpCrawler := cdp.NewChromedpCrawlerWithOptions(
		"cnn-browser",
		cnnCfg.CrawlerConfig.SourceInfo,
		config,
		cdp.DefaultRemoteOptions(),
	)
	brFetcher := fetcher.NewBrowserFetcher(cdpCrawler, config)

	// 파서 & 크롤러 생성
	parser := cnn.NewCNNParser(cnnCfg)
	crawler := cnn.NewCNNCrawler(cnnCfg, gqFetcher, parser, log)

	// 체인 조립: GoQuery → Browser (RSS 제외)
	// lazy loading 감지 시 GoQuery에서 Browser로 자동 위임합니다.
	chain := news.BuildChain(nil, gqFetcher, brFetcher, log,
		"data-lazy-src",
		"lazyload",
		"data-lazy",
	)

	registry.Register("cnn", news.NewChainHandler(crawler, chain, parser, parser, publisher, repo, log))

	log.WithField("crawler", "cnn").Info("cnn news crawler registered")
}
