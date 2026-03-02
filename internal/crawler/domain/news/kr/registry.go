// Package kr는 한국 뉴스 소스 크롤러를 조립하고 Registry에 등록합니다.
// 이 패키지만이 구체 구현 패키지(goquery, chromedp, fetcher, naver, yonhap)를 모두 import합니다.
//
// Package kr assembles Korean news crawlers and registers them with the handler Registry.
// This is the only package that imports all concrete implementation packages.
package kr

import (
	"context"

	"issuetracker/internal/crawler/core"
	"issuetracker/internal/crawler/domain/news"
	"issuetracker/internal/crawler/domain/news/fetcher"
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

	registry.Register("naver", newNewsChainHandler(crawler, chain, parser, repo, log))

	log.WithField("crawler", "naver").Info("naver 뉴스 크롤러 등록 완료")
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

	registry.Register("yonhap", newNewsChainHandler(crawler, chain, parser, repo, log))

	log.WithField("crawler", "yonhap").Info("yonhap 뉴스 크롤러 등록 완료")
}

// newsChainHandler는 news.NewsHandler를 handler.Handler로 어댑팅합니다.
// worker.KafkaConsumerPool이 handler.Handler를 요구하므로 이 어댑터가 필요합니다.
// 패키지 내부 구현 세부 사항이므로 비공개(unexported)로 유지합니다.
type newsChainHandler struct {
	crawler news.NewsCrawler
	chain   news.NewsHandler
	parser  news.NewsArticleParser        // nil 허용: 파서 없으면 DB 저장 건너뜀
	repo    storage.NewsArticleRepository // nil 허용: repo 없으면 DB 저장 건너뜀
	log     *logger.Logger
}

func newNewsChainHandler(
	crawler news.NewsCrawler,
	chain news.NewsHandler,
	parser news.NewsArticleParser,
	repo storage.NewsArticleRepository,
	log *logger.Logger,
) *newsChainHandler {
	return &newsChainHandler{
		crawler: crawler,
		chain:   chain,
		parser:  parser,
		repo:    repo,
		log:     log,
	}
}

// Handle은 CrawlJob을 받아 chain을 통해 RawContent를 가져옵니다.
// TargetTypeArticle이고 HTML이 있으면 파싱 후 DB에 저장합니다.
// DB 저장 실패는 warn 로그만 남기고 크롤 결과에는 영향을 주지 않습니다.
func (h *newsChainHandler) Handle(ctx context.Context, job *core.CrawlJob) (*core.RawContent, error) {
	raw, err := h.chain.Handle(ctx, job)
	if err != nil {
		return nil, err
	}

	// DB 저장 조건 체크 및 디버그 로그
	h.log.WithFields(map[string]interface{}{
		"target_type": string(job.Target.Type),
		"has_html":    raw.HTML != "",
		"html_length": len(raw.HTML),
		"has_parser":  h.parser != nil,
		"has_repo":    h.repo != nil,
	}).Debug("DB 저장 조건 확인")

	if job.Target.Type == core.TargetTypeArticle && raw.HTML != "" &&
		h.parser != nil && h.repo != nil {
		h.saveArticle(ctx, raw)
	} else {
		h.log.WithFields(map[string]interface{}{
			"is_article": job.Target.Type == core.TargetTypeArticle,
			"has_html":   raw.HTML != "",
			"has_parser": h.parser != nil,
			"has_repo":   h.repo != nil,
		}).Warn("조건 불충족: saveArticle 건너뜀")
	}

	return raw, nil
}

// saveArticle은 RawContent를 파싱하여 news_articles 테이블에 저장합니다.
// 에러는 warn 로그로만 기록하며 호출자에게 전파되지 않습니다.
func (h *newsChainHandler) saveArticle(ctx context.Context, raw *core.RawContent) {
	h.log.WithField("url", raw.URL).Debug("기사 파싱 시작")

	article, err := h.parser.ParseArticle(raw)
	if err != nil {
		h.log.WithError(err).Warn("기사 파싱 실패, DB 저장 건너뜀")
		return
	}

	h.log.WithFields(map[string]interface{}{
		"title":  article.Title,
		"author": article.Author,
		"url":    article.URL,
	}).Debug("기사 파싱 성공, DB 저장 시작")

	record := newsArticleToRecord(article, raw)

	if err := h.repo.Insert(ctx, record); err != nil {
		h.log.WithError(err).Warn("뉴스 기사 DB 저장 실패")
		return
	}

	h.log.WithField("url", raw.URL).Debug("뉴스 기사 DB 저장 성공")
}

// newsArticleToRecord는 NewsArticle과 RawContent를 NewsArticleRecord로 변환합니다.
// URL은 chain이 확정한 raw.URL을 우선 사용합니다.
func newsArticleToRecord(article *news.NewsArticle, raw *core.RawContent) *storage.NewsArticleRecord {
	record := &storage.NewsArticleRecord{
		SourceName: raw.SourceInfo.Name,
		SourceType: string(raw.SourceInfo.Type),
		Country:    raw.SourceInfo.Country,
		Language:   raw.SourceInfo.Language,
		URL:        raw.URL,
		Title:      article.Title,
		Body:       article.Body,
		Summary:    article.Summary,
		Author:     article.Author,
		Category:   article.Category,
		Tags:       article.Tags,
		ImageURLs:  article.ImageURLs,
		FetchedAt:  raw.FetchedAt,
	}

	if !article.PublishedAt.IsZero() {
		t := article.PublishedAt
		record.PublishedAt = &t
	}

	return record
}
