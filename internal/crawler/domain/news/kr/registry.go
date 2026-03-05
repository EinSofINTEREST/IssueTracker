// Package kr는 한국 뉴스 소스 크롤러를 조립하고 Registry에 등록합니다.
// 이 패키지만이 구체 구현 패키지(goquery, chromedp, fetcher, naver, yonhap, daum)를 모두 import합니다.
//
// Package kr assembles Korean news crawlers and registers them with the handler Registry.
// This is the only package that imports all concrete implementation packages.
package kr

import (
	"issuetracker/internal/crawler/core"
	"issuetracker/internal/crawler/domain/news"
	"issuetracker/internal/crawler/domain/news/fetcher"
	"issuetracker/internal/crawler/domain/news/kr/daum"
	"issuetracker/internal/crawler/domain/news/kr/naver"
	"issuetracker/internal/crawler/domain/news/kr/yonhap"
	"issuetracker/internal/crawler/handler"
	cdp "issuetracker/internal/crawler/implementation/chromedp"
	"issuetracker/internal/crawler/implementation/goquery"
	"issuetracker/internal/storage"
	"issuetracker/pkg/logger"
)

// Register는 모든 한국 뉴스 크롤러를 registry에 등록합니다.
// cmd/crawler/main.go에서 이 함수를 호출하여 KR 뉴스 크롤러를 활성화합니다.
// repo가 nil이면 DB 저장 없이 크롤링만 수행합니다.
//
// Register wires all Korean news crawlers and registers them with the provided Registry.
// If repo is nil, articles are crawled but not persisted to the database.
func Register(registry *handler.Registry, config core.Config, repo storage.NewsArticleRepository, log *logger.Logger) {
	registerNaver(registry, config, repo, log)
	registerYonhap(registry, config, repo, log)
	registerDaum(registry, config, repo, log)
}

// registerNaver는 네이버 뉴스 핸들러를 조립하고 등록합니다.
// 체인: GoQuery(주) → Browser(폴백)
// 네이버는 RSS 공식 지원이 제한적이므로 goquery를 주 전략으로 사용합니다.
func registerNaver(registry *handler.Registry, config core.Config, repo storage.NewsArticleRepository, log *logger.Logger) {
	naverCfg := naver.DefaultNaverConfig()
	naverCfg.CrawlerConfig = config
	naverCfg.CrawlerConfig.SourceInfo = core.SourceInfo{
		Country:  "KR",
		Type:     core.SourceTypeNews,
		Name:     "naver",
		BaseURL:  "https://news.naver.com",
		Language: "ko",
	}

	// goquery 크롤러 (주 전략)
	gqCrawler := goquery.NewGoqueryCrawler("naver-goquery", naverCfg.CrawlerConfig.SourceInfo, config)
	gqFetcher := fetcher.NewGoqueryFetcher(gqCrawler)

	// chromedp 크롤러 (폴백 전략) — 첫 Fetch 호출 시 지연 초기화
	cdpSource := naverCfg.CrawlerConfig.SourceInfo
	cdpSource.Name = "naver-browser"
	cdpCrawler := cdp.NewChromedpCrawlerWithOptions(
		"naver-browser",
		cdpSource,
		config,
		cdp.DefaultRemoteOptions(),
	)
	brFetcher := fetcher.NewBrowserFetcher(cdpCrawler, config)

	// 파서 & 크롤러 생성
	parser := naver.NewNaverParser(naverCfg)
	crawler := naver.NewNaverCrawler(naverCfg, gqFetcher, parser, log)

	// 체인 조립: GoQuery → Browser (네이버 RSS 미지원)
	// lazy loading 키워드 감지 시 GoQuery에서 Browser로 자동 위임
	chain := news.BuildChain(nil, gqFetcher, brFetcher, log,
		"data-lazy-src",
		"lazyload",
		"data-lazy",
	)

	registry.Register("naver", news.NewChainHandler(crawler, chain, parser, repo, log))

	log.WithField("crawler", "naver").Info("naver news crawler registered")
}

// registerYonhap는 연합뉴스 핸들러를 조립하고 등록합니다.
// 체인: GoQuery 단독 (연합뉴스는 정적 HTML이므로 RSS·Browser 불필요)
func registerYonhap(registry *handler.Registry, config core.Config, repo storage.NewsArticleRepository, log *logger.Logger) {
	yonhapCfg := yonhap.DefaultYonhapConfig()
	yonhapCfg.CrawlerConfig = config
	yonhapCfg.CrawlerConfig.SourceInfo = core.SourceInfo{
		Country:  "KR",
		Type:     core.SourceTypeNews,
		Name:     "yonhap",
		BaseURL:  "https://www.yna.co.kr",
		Language: "ko",
	}

	// goquery fetcher (단독 전략)
	gqSource := yonhapCfg.CrawlerConfig.SourceInfo
	gqSource.Name = "yonhap"
	gqCrawler := goquery.NewGoqueryCrawler("yonhap-goquery", gqSource, config)
	gqFetcher := fetcher.NewGoqueryFetcher(gqCrawler)

	// 파서 & 크롤러 생성
	parser := yonhap.NewYonhapParser(yonhapCfg)
	crawler := yonhap.NewYonhapCrawler(yonhapCfg, gqFetcher, parser, log)

	// 체인 조립: GoQuery 단독
	chain := news.BuildChain(nil, gqFetcher, nil, log)

	registry.Register("yonhap", news.NewChainHandler(crawler, chain, parser, repo, log))

	log.WithField("crawler", "yonhap").Info("yonhap news crawler registered")
}

// registerDaum는 다음 뉴스 핸들러를 조립하고 등록합니다.
// 체인: GoQuery(주) → Browser(폴백)
// 다음 뉴스는 일부 페이지에서 lazy loading을 사용하므로 browser 폴백을 포함합니다.
func registerDaum(registry *handler.Registry, config core.Config, repo storage.NewsArticleRepository, log *logger.Logger) {
	daumCfg := daum.DefaultDaumConfig()
	daumCfg.CrawlerConfig = config
	daumCfg.CrawlerConfig.SourceInfo = core.SourceInfo{
		Country:  "KR",
		Type:     core.SourceTypeNews,
		Name:     "daum",
		BaseURL:  "https://news.daum.net",
		Language: "ko",
	}

	// goquery 크롤러 (주 전략)
	gqSource := daumCfg.CrawlerConfig.SourceInfo
	gqSource.Name = "daum"
	gqCrawler := goquery.NewGoqueryCrawler("daum-goquery", gqSource, config)
	gqFetcher := fetcher.NewGoqueryFetcher(gqCrawler)

	// chromedp 크롤러 (폴백 전략) — 첫 Fetch 호출 시 지연 초기화
	cdpSource := daumCfg.CrawlerConfig.SourceInfo
	cdpSource.Name = "daum"
	cdpCrawler := cdp.NewChromedpCrawlerWithOptions(
		"daum-browser",
		cdpSource,
		config,
		cdp.DefaultRemoteOptions(),
	)
	brFetcher := fetcher.NewBrowserFetcher(cdpCrawler, config)

	// 파서 & 크롤러 생성
	parser := daum.NewDaumParser(daumCfg)
	crawler := daum.NewDaumCrawler(daumCfg, gqFetcher, parser, log)

	// 체인 조립: GoQuery → Browser
	// lazy loading 키워드 감지 시 GoQuery에서 Browser로 자동 위임
	chain := news.BuildChain(nil, gqFetcher, brFetcher, log,
		"data-lazy-src",
		"lazyload",
		"data-lazy",
	)

	registry.Register("daum", news.NewChainHandler(crawler, chain, parser, repo, log))

	log.WithField("crawler", "daum").Info("daum news crawler registered")
}
