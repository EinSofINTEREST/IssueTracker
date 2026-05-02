// Package kr 는 한국 사이트 크롤러를 조립하고 Registry 에 등록합니다.
//
// Package kr assembles Korean site crawlers and registers them with the handler Registry.
// 본 패키지가 구체 구현 패키지 (goquery, chromedp, fetcher, naver, daum, yonhap) 를 모두 import 하는 단일 지점.
package kr

import (
	"fmt"

	"issuetracker/internal/processor/fetcher/core"
	"issuetracker/internal/processor/fetcher/domain/general"
	"issuetracker/internal/processor/fetcher/domain/general/fetcher"
	"issuetracker/internal/processor/fetcher/domain/general/sources/kr/daum"
	"issuetracker/internal/processor/fetcher/domain/general/sources/kr/naver"
	"issuetracker/internal/processor/fetcher/domain/general/sources/kr/yonhap"
	"issuetracker/internal/processor/fetcher/handler"
	cdp "issuetracker/internal/processor/fetcher/implementation/chromedp"
	"issuetracker/internal/processor/fetcher/implementation/goquery"
	"issuetracker/internal/processor/fetcher/rule"
	"issuetracker/internal/storage/service"
	"issuetracker/pkg/logger"
	"issuetracker/pkg/queue"
)

// Register 는 모든 한국 사이트 크롤러를 registry 에 등록합니다 (이슈 #134 — fetcher/parser 분리 후).
//
// rawSvc + producer 는 ChainHandler (fetcher 측) 가 raw_contents 저장 + RawContentRef 발행에 사용.
// resolver 는 호스트 단위 fetcher 룰 (이슈 #175) 적용. nil 이면 항상 default chain.
// parser worker 는 본 함수 외부에서 별도 wiring (cmd/issuetracker/main.go).
//
// 등록 중 wiring 실패 (BuildChain misconfig 등) 시 error 반환 — 호출자가 fail-fast 결정.
func Register(
	registry *handler.Registry,
	config core.Config,
	rawSvc service.RawContentService,
	producer queue.Producer,
	resolver rule.Resolver,
	log *logger.Logger,
) error {
	if err := registerNaver(registry, config, rawSvc, producer, resolver, log); err != nil {
		return err
	}
	if err := registerYonhap(registry, config, rawSvc, producer, resolver, log); err != nil {
		return err
	}
	if err := registerDaum(registry, config, rawSvc, producer, resolver, log); err != nil {
		return err
	}
	return nil
}

// 사이트별 등록의 공통 패턴 (이슈 #134 분리 후):
//   - cfg := xxx.Default() 로 사이트 기본값 (RequestsPerHour 등) 보유
//   - 호출자가 넘긴 config 는 사용하지 않음 (사이트별 디테일 덮어쓰기 회피)
//   - ChainHandler 는 fetch + raw store + RawContentRef publish 만 수행 — 파싱은 parser worker 책임

func registerNaver(registry *handler.Registry, _ core.Config, rawSvc service.RawContentService, producer queue.Producer, resolver rule.Resolver, log *logger.Logger) error {
	cfg := naver.Default()

	gqCrawler := goquery.NewGoqueryCrawler("naver-goquery", cfg.CrawlerConfig.SourceInfo, cfg.CrawlerConfig)
	gqFetcher := fetcher.NewGoqueryFetcher(gqCrawler)

	cdpCrawler := cdp.NewChromedpCrawlerWithOptions("naver-browser", cfg.CrawlerConfig.SourceInfo, cfg.CrawlerConfig, cdp.DefaultRemoteOptions())
	brFetcher := fetcher.NewBrowserFetcher(cdpCrawler, cfg.CrawlerConfig)

	crawler := general.NewGenericCrawler("naver", cfg.CrawlerConfig.SourceInfo, gqFetcher, cfg.BaseURL, cfg.CrawlerConfig)
	defaultChain, err := general.BuildChain(gqFetcher, brFetcher, log,
		"data-lazy-src", "lazyload", "data-lazy",
	)
	if err != nil {
		return fmt.Errorf("naver default chain wiring: %w", err)
	}
	chromedpChain, err := general.BuildChain(nil, brFetcher, log)
	if err != nil {
		return fmt.Errorf("naver chromedp chain wiring: %w", err)
	}

	registry.Register("naver", general.NewChainHandler(crawler, defaultChain, chromedpChain, resolver, rawSvc, producer, log))
	log.WithField("crawler", "naver").Info("naver crawler registered")
	return nil
}

func registerYonhap(registry *handler.Registry, _ core.Config, rawSvc service.RawContentService, producer queue.Producer, resolver rule.Resolver, log *logger.Logger) error {
	cfg := yonhap.Default()

	gqCrawler := goquery.NewGoqueryCrawler("yonhap-goquery", cfg.CrawlerConfig.SourceInfo, cfg.CrawlerConfig)
	gqFetcher := fetcher.NewGoqueryFetcher(gqCrawler)

	crawler := general.NewGenericCrawler("yonhap", cfg.CrawlerConfig.SourceInfo, gqFetcher, cfg.BaseURL, cfg.CrawlerConfig)
	defaultChain, err := general.BuildChain(gqFetcher, nil, log)
	if err != nil {
		return fmt.Errorf("yonhap chain wiring: %w", err)
	}

	// yonhap 은 browser fetcher 미설정 — chromedpChain=nil. 룰=chromedp 매칭 시 ChainHandler 가
	// warn 로그 + DefaultChain 으로 fallback.
	registry.Register("yonhap", general.NewChainHandler(crawler, defaultChain, nil, resolver, rawSvc, producer, log))
	log.WithField("crawler", "yonhap").Info("yonhap crawler registered")
	return nil
}

func registerDaum(registry *handler.Registry, _ core.Config, rawSvc service.RawContentService, producer queue.Producer, resolver rule.Resolver, log *logger.Logger) error {
	cfg := daum.Default()

	gqCrawler := goquery.NewGoqueryCrawler("daum-goquery", cfg.CrawlerConfig.SourceInfo, cfg.CrawlerConfig)
	gqFetcher := fetcher.NewGoqueryFetcher(gqCrawler)

	cdpCrawler := cdp.NewChromedpCrawlerWithOptions("daum-browser", cfg.CrawlerConfig.SourceInfo, cfg.CrawlerConfig, cdp.DefaultRemoteOptions())
	brFetcher := fetcher.NewBrowserFetcher(cdpCrawler, cfg.CrawlerConfig)

	crawler := general.NewGenericCrawler("daum", cfg.CrawlerConfig.SourceInfo, gqFetcher, cfg.BaseURL, cfg.CrawlerConfig)
	defaultChain, err := general.BuildChain(gqFetcher, brFetcher, log,
		"data-lazy-src", "lazyload", "data-lazy",
	)
	if err != nil {
		return fmt.Errorf("daum default chain wiring: %w", err)
	}
	chromedpChain, err := general.BuildChain(nil, brFetcher, log)
	if err != nil {
		return fmt.Errorf("daum chromedp chain wiring: %w", err)
	}

	registry.Register("daum", general.NewChainHandler(crawler, defaultChain, chromedpChain, resolver, rawSvc, producer, log))
	log.WithField("crawler", "daum").Info("daum crawler registered")
	return nil
}
