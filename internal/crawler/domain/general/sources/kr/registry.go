// Package kr 는 한국 사이트 크롤러를 조립하고 Registry 에 등록합니다.
//
// Package kr assembles Korean site crawlers and registers them with the handler Registry.
// 본 패키지가 구체 구현 패키지 (goquery, chromedp, fetcher, naver, daum, yonhap) 를 모두 import 하는 단일 지점.
package kr

import (
	"issuetracker/internal/crawler/core"
	"issuetracker/internal/crawler/domain/general"
	"issuetracker/internal/crawler/domain/general/fetcher"
	"issuetracker/internal/crawler/domain/general/sources/kr/daum"
	"issuetracker/internal/crawler/domain/general/sources/kr/naver"
	"issuetracker/internal/crawler/domain/general/sources/kr/yonhap"
	"issuetracker/internal/crawler/handler"
	cdp "issuetracker/internal/crawler/implementation/chromedp"
	"issuetracker/internal/crawler/implementation/goquery"
	"issuetracker/internal/crawler/parser/rule"
	"issuetracker/internal/storage"
	"issuetracker/pkg/logger"
)

// Register 는 모든 한국 사이트 크롤러를 registry 에 등록합니다.
// repo 가 nil 이면 DB 저장 건너뜀, publisher 가 nil 이면 카테고리 체이닝 건너뜀.
// parser 는 rule 기반 단일 인스턴스 — 모든 사이트가 공유 (parsing_rules row 가 사이트 동작 결정).
func Register(
	registry *handler.Registry,
	config core.Config,
	parser *rule.Parser,
	repo storage.NewsArticleRepository,
	publisher general.JobPublisher,
	log *logger.Logger,
) {
	registerNaver(registry, config, parser, repo, publisher, log)
	registerYonhap(registry, config, parser, repo, publisher, log)
	registerDaum(registry, config, parser, repo, publisher, log)
}

func registerNaver(registry *handler.Registry, config core.Config, parser *rule.Parser, repo storage.NewsArticleRepository, publisher general.JobPublisher, log *logger.Logger) {
	cfg := naver.Default()
	cfg.CrawlerConfig = config
	cfg.CrawlerConfig.SourceInfo = core.SourceInfo{
		Country: "KR", Type: core.SourceTypeNews, Name: "naver",
		BaseURL: "https://news.naver.com", Language: "ko",
	}

	gqCrawler := goquery.NewGoqueryCrawler("naver-goquery", cfg.CrawlerConfig.SourceInfo, config)
	gqFetcher := fetcher.NewGoqueryFetcher(gqCrawler)

	cdpSource := cfg.CrawlerConfig.SourceInfo
	cdpSource.Name = "naver-browser"
	cdpCrawler := cdp.NewChromedpCrawlerWithOptions("naver-browser", cdpSource, config, cdp.DefaultRemoteOptions())
	brFetcher := fetcher.NewBrowserFetcher(cdpCrawler, config)

	crawler := general.NewGenericCrawler("naver", cfg.CrawlerConfig.SourceInfo, gqFetcher, cfg.BaseURL, config)
	chain := general.BuildChain(nil, gqFetcher, brFetcher, log,
		"data-lazy-src", "lazyload", "data-lazy",
	)

	registry.Register("naver", general.NewChainHandler(crawler, chain, parser, publisher, repo, log))
	log.WithField("crawler", "naver").Info("naver crawler registered")
}

func registerYonhap(registry *handler.Registry, config core.Config, parser *rule.Parser, repo storage.NewsArticleRepository, publisher general.JobPublisher, log *logger.Logger) {
	cfg := yonhap.Default()
	cfg.CrawlerConfig = config
	cfg.CrawlerConfig.SourceInfo = core.SourceInfo{
		Country: "KR", Type: core.SourceTypeNews, Name: "yonhap",
		BaseURL: "https://www.yna.co.kr", Language: "ko",
	}

	gqCrawler := goquery.NewGoqueryCrawler("yonhap-goquery", cfg.CrawlerConfig.SourceInfo, config)
	gqFetcher := fetcher.NewGoqueryFetcher(gqCrawler)

	crawler := general.NewGenericCrawler("yonhap", cfg.CrawlerConfig.SourceInfo, gqFetcher, cfg.BaseURL, config)
	chain := general.BuildChain(nil, gqFetcher, nil, log)

	registry.Register("yonhap", general.NewChainHandler(crawler, chain, parser, publisher, repo, log))
	log.WithField("crawler", "yonhap").Info("yonhap crawler registered")
}

func registerDaum(registry *handler.Registry, config core.Config, parser *rule.Parser, repo storage.NewsArticleRepository, publisher general.JobPublisher, log *logger.Logger) {
	cfg := daum.Default()
	cfg.CrawlerConfig = config
	cfg.CrawlerConfig.SourceInfo = core.SourceInfo{
		Country: "KR", Type: core.SourceTypeNews, Name: "daum",
		BaseURL: "https://news.daum.net", Language: "ko",
	}

	gqCrawler := goquery.NewGoqueryCrawler("daum-goquery", cfg.CrawlerConfig.SourceInfo, config)
	gqFetcher := fetcher.NewGoqueryFetcher(gqCrawler)

	cdpSource := cfg.CrawlerConfig.SourceInfo
	cdpCrawler := cdp.NewChromedpCrawlerWithOptions("daum-browser", cdpSource, config, cdp.DefaultRemoteOptions())
	brFetcher := fetcher.NewBrowserFetcher(cdpCrawler, config)

	crawler := general.NewGenericCrawler("daum", cfg.CrawlerConfig.SourceInfo, gqFetcher, cfg.BaseURL, config)
	chain := general.BuildChain(nil, gqFetcher, brFetcher, log,
		"data-lazy-src", "lazyload", "data-lazy",
	)

	registry.Register("daum", general.NewChainHandler(crawler, chain, parser, publisher, repo, log))
	log.WithField("crawler", "daum").Info("daum crawler registered")
}
