// Package kr 는 한국 사이트 크롤러를 조립하고 Registry 에 등록합니다.
//
// Package kr assembles Korean site crawlers and registers them with the handler Registry.
// 본 패키지가 구체 구현 패키지 (goquery, chromedp, fetcher, naver, daum, yonhap) 를 모두 import 하는 단일 지점.
package kr

import (
	"fmt"

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
//
// 등록 중 wiring 실패 (BuildChain misconfig 등) 시 error 반환 — 호출자가 fail-fast 결정.
func Register(
	registry *handler.Registry,
	config core.Config,
	parser *rule.Parser,
	repo storage.NewsArticleRepository,
	publisher general.JobPublisher,
	log *logger.Logger,
) error {
	if err := registerNaver(registry, config, parser, repo, publisher, log); err != nil {
		return err
	}
	if err := registerYonhap(registry, config, parser, repo, publisher, log); err != nil {
		return err
	}
	if err := registerDaum(registry, config, parser, repo, publisher, log); err != nil {
		return err
	}
	return nil
}

// 사이트별 등록의 공통 패턴:
//   - cfg := xxx.Default() 로 사이트 기본값 (RequestsPerHour 등) 보유
//   - 호출자가 넘긴 config 는 사용하지 않음 (사이트별 디테일을 덮어쓰는 버그 회피 — Gemini 피드백)
//   - chromedp/goquery 등 모든 컴포넌트가 cfg.CrawlerConfig 를 참조하여 사이트별 설정 일관 적용
//
// 호출자가 넘긴 config 의 의미: 향후 전역 default 가 필요할 때를 위한 시그니처 보존 — 현재는 무시.

func registerNaver(registry *handler.Registry, _ core.Config, parser *rule.Parser, repo storage.NewsArticleRepository, publisher general.JobPublisher, log *logger.Logger) error {
	cfg := naver.Default()

	gqCrawler := goquery.NewGoqueryCrawler("naver-goquery", cfg.CrawlerConfig.SourceInfo, cfg.CrawlerConfig)
	gqFetcher := fetcher.NewGoqueryFetcher(gqCrawler)

	// chromedp 인스턴스 식별자만 "naver-browser" — SourceInfo.Name 은 "naver" 그대로 (source_name 일관성, Coderabbit 피드백)
	cdpCrawler := cdp.NewChromedpCrawlerWithOptions("naver-browser", cfg.CrawlerConfig.SourceInfo, cfg.CrawlerConfig, cdp.DefaultRemoteOptions())
	brFetcher := fetcher.NewBrowserFetcher(cdpCrawler, cfg.CrawlerConfig)

	crawler := general.NewGenericCrawler("naver", cfg.CrawlerConfig.SourceInfo, gqFetcher, cfg.BaseURL, cfg.CrawlerConfig)
	chain, err := general.BuildChain(nil, gqFetcher, brFetcher, log,
		"data-lazy-src", "lazyload", "data-lazy",
	)
	if err != nil {
		return fmt.Errorf("naver chain wiring: %w", err)
	}

	registry.Register("naver", general.NewChainHandler(crawler, chain, parser, publisher, repo, log))
	log.WithField("crawler", "naver").Info("naver crawler registered")
	return nil
}

func registerYonhap(registry *handler.Registry, _ core.Config, parser *rule.Parser, repo storage.NewsArticleRepository, publisher general.JobPublisher, log *logger.Logger) error {
	cfg := yonhap.Default()

	gqCrawler := goquery.NewGoqueryCrawler("yonhap-goquery", cfg.CrawlerConfig.SourceInfo, cfg.CrawlerConfig)
	gqFetcher := fetcher.NewGoqueryFetcher(gqCrawler)

	crawler := general.NewGenericCrawler("yonhap", cfg.CrawlerConfig.SourceInfo, gqFetcher, cfg.BaseURL, cfg.CrawlerConfig)
	chain, err := general.BuildChain(nil, gqFetcher, nil, log)
	if err != nil {
		return fmt.Errorf("yonhap chain wiring: %w", err)
	}

	registry.Register("yonhap", general.NewChainHandler(crawler, chain, parser, publisher, repo, log))
	log.WithField("crawler", "yonhap").Info("yonhap crawler registered")
	return nil
}

func registerDaum(registry *handler.Registry, _ core.Config, parser *rule.Parser, repo storage.NewsArticleRepository, publisher general.JobPublisher, log *logger.Logger) error {
	cfg := daum.Default()

	gqCrawler := goquery.NewGoqueryCrawler("daum-goquery", cfg.CrawlerConfig.SourceInfo, cfg.CrawlerConfig)
	gqFetcher := fetcher.NewGoqueryFetcher(gqCrawler)

	cdpCrawler := cdp.NewChromedpCrawlerWithOptions("daum-browser", cfg.CrawlerConfig.SourceInfo, cfg.CrawlerConfig, cdp.DefaultRemoteOptions())
	brFetcher := fetcher.NewBrowserFetcher(cdpCrawler, cfg.CrawlerConfig)

	crawler := general.NewGenericCrawler("daum", cfg.CrawlerConfig.SourceInfo, gqFetcher, cfg.BaseURL, cfg.CrawlerConfig)
	chain, err := general.BuildChain(nil, gqFetcher, brFetcher, log,
		"data-lazy-src", "lazyload", "data-lazy",
	)
	if err != nil {
		return fmt.Errorf("daum chain wiring: %w", err)
	}

	registry.Register("daum", general.NewChainHandler(crawler, chain, parser, publisher, repo, log))
	log.WithField("crawler", "daum").Info("daum crawler registered")
	return nil
}
