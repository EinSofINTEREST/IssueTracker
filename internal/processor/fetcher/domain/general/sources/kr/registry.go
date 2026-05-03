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
// chromedpRemoteURLs 는 worker_id 별 Chrome RemoteURL slice (이슈 #230). 길이 = chromedp pool 의
// WorkerCount. empty 면 chromedp 사용 사이트도 chromedp chain 미wiring (운영자 명시적 비활성화).
// parser worker 는 본 함수 외부에서 별도 wiring (cmd/issuetracker/main.go).
//
// 등록 중 wiring 실패 (BuildChain misconfig 등) 시 error 반환 — 호출자가 fail-fast 결정.
func Register(
	registry *handler.Registry,
	config core.Config,
	rawSvc service.RawContentService,
	producer queue.Producer,
	resolver rule.Resolver,
	chromedpRemoteURLs []string,
	log *logger.Logger,
) error {
	if err := registerNaver(registry, config, rawSvc, producer, resolver, chromedpRemoteURLs, log); err != nil {
		return err
	}
	if err := registerYonhap(registry, config, rawSvc, producer, resolver, log); err != nil {
		return err
	}
	if err := registerDaum(registry, config, rawSvc, producer, resolver, chromedpRemoteURLs, log); err != nil {
		return err
	}
	return nil
}

// 사이트별 등록의 공통 패턴 (이슈 #134 분리 후):
//   - cfg := xxx.Default() 로 사이트 기본값 (RequestsPerHour 등) 보유
//   - 호출자가 넘긴 config 는 사용하지 않음 (사이트별 디테일 덮어쓰기 회피)
//   - ChainHandler 는 fetch + raw store + RawContentRef publish 만 수행 — 파싱은 parser worker 책임

// buildChromedpChainsForSite 는 site 의 chromedp chain 을 worker_id 별 N 개 build 합니다 (이슈 #230).
//
// remoteURLs 길이만큼 ChromedpCrawler / BrowserFetcher / Chain 생성 — 각 인스턴스가 자기 RemoteURL
// 에 연결. nameBase 는 로깅·식별용 prefix (예: "naver-browser" → "naver-browser-0", "naver-browser-1").
//
// remoteURLs 가 empty 면 nil slice 반환 — 호출자는 chromedpChains nil 로 ChainHandler 생성 (chromedp 미사용 사이트와 동일 fallback 흐름).
func buildChromedpChainsForSite(
	nameBase string,
	sourceInfo core.SourceInfo,
	cfg core.Config,
	remoteURLs []string,
	log *logger.Logger,
) ([]general.Handler, error) {
	if len(remoteURLs) == 0 {
		return nil, nil
	}
	chains := make([]general.Handler, len(remoteURLs))
	for i, url := range remoteURLs {
		opts := cdp.DefaultRemoteOptions()
		opts.RemoteURL = url
		cdpCrawler := cdp.NewChromedpCrawlerWithOptions(fmt.Sprintf("%s-%d", nameBase, i), sourceInfo, cfg, opts)
		brFetcher := fetcher.NewBrowserFetcher(cdpCrawler, cfg)
		chain, err := general.BuildChain(nil, brFetcher, log)
		if err != nil {
			// CodeRabbit 피드백: error message 에 RemoteURL 포함 시 ws://user:pass@... 같은
			// 인증 토큰이 로그에 누출. worker_id 만으로 triage 충분.
			return nil, fmt.Errorf("%s chromedp chain wiring (worker_id=%d): %w", nameBase, i, err)
		}
		chains[i] = chain
	}
	return chains, nil
}

func registerNaver(registry *handler.Registry, _ core.Config, rawSvc service.RawContentService, producer queue.Producer, resolver rule.Resolver, chromedpRemoteURLs []string, log *logger.Logger) error {
	cfg := naver.Default()

	gqCrawler := goquery.NewGoqueryCrawler("naver-goquery", cfg.CrawlerConfig.SourceInfo, cfg.CrawlerConfig)
	gqFetcher := fetcher.NewGoqueryFetcher(gqCrawler)

	crawler := general.NewGenericCrawler("naver", cfg.CrawlerConfig.SourceInfo, gqFetcher, cfg.BaseURL, cfg.CrawlerConfig)
	// 이슈 #218: DefaultChain 은 goquery only — lazy detect 시 sentinel 반환 → ChainHandler 가 chromedp pool 로 republish.
	defaultChain, err := general.BuildChain(gqFetcher, nil, log,
		"data-lazy-src", "lazyload", "data-lazy",
	)
	if err != nil {
		return fmt.Errorf("naver default chain wiring: %w", err)
	}
	chromedpChains, err := buildChromedpChainsForSite("naver-browser", cfg.CrawlerConfig.SourceInfo, cfg.CrawlerConfig, chromedpRemoteURLs, log)
	if err != nil {
		return err
	}

	registry.Register("naver", general.NewChainHandler(crawler, defaultChain, chromedpChains, resolver, rawSvc, producer, log))
	log.WithFields(map[string]interface{}{
		"crawler":          "naver",
		"chromedp_workers": len(chromedpChains),
	}).Info("naver crawler registered")
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

	// yonhap 은 browser fetcher 미설정 — chromedpChains=nil. 룰=chromedp 매칭 시 ChainHandler 가
	// warn 로그 + DefaultChain 으로 fallback.
	registry.Register("yonhap", general.NewChainHandler(crawler, defaultChain, nil, resolver, rawSvc, producer, log))
	log.WithField("crawler", "yonhap").Info("yonhap crawler registered")
	return nil
}

func registerDaum(registry *handler.Registry, _ core.Config, rawSvc service.RawContentService, producer queue.Producer, resolver rule.Resolver, chromedpRemoteURLs []string, log *logger.Logger) error {
	cfg := daum.Default()

	gqCrawler := goquery.NewGoqueryCrawler("daum-goquery", cfg.CrawlerConfig.SourceInfo, cfg.CrawlerConfig)
	gqFetcher := fetcher.NewGoqueryFetcher(gqCrawler)

	crawler := general.NewGenericCrawler("daum", cfg.CrawlerConfig.SourceInfo, gqFetcher, cfg.BaseURL, cfg.CrawlerConfig)
	// 이슈 #218: DefaultChain = goquery only.
	defaultChain, err := general.BuildChain(gqFetcher, nil, log,
		"data-lazy-src", "lazyload", "data-lazy",
	)
	if err != nil {
		return fmt.Errorf("daum default chain wiring: %w", err)
	}
	chromedpChains, err := buildChromedpChainsForSite("daum-browser", cfg.CrawlerConfig.SourceInfo, cfg.CrawlerConfig, chromedpRemoteURLs, log)
	if err != nil {
		return err
	}

	registry.Register("daum", general.NewChainHandler(crawler, defaultChain, chromedpChains, resolver, rawSvc, producer, log))
	log.WithFields(map[string]interface{}{
		"crawler":          "daum",
		"chromedp_workers": len(chromedpChains),
	}).Info("daum crawler registered")
	return nil
}
