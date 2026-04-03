package news

import (
	"context"

	"issuetracker/internal/crawler/core"
	newshandler "issuetracker/internal/crawler/domain/news/handler"
	"issuetracker/internal/storage"
	"issuetracker/pkg/logger"
)

// ChainHandler는 NewsHandler 체인과 DB 저장을 결합한 handler.Handler 어댑터입니다.
// worker.KafkaConsumerPool이 요구하는 handler.Handler 인터페이스를 구현하며,
// 크롤링 및 파싱 결과를 []*core.Content로 반환합니다.
// RSS 피드는 여러 Content를 반환하고, 기사 페이지는 단일 Content를 반환합니다.
// 카테고리/목록 페이지는 기사 URL을 추출하여 Publisher를 통해 다음 CrawlJob으로 연결합니다.
//
// ChainHandler adapts a NewsHandler chain to the handler.Handler interface.
// RSS feeds yield multiple Content items; HTML article pages yield one.
// Category/list pages dispatch article CrawlJobs via the JobPublisher.
type ChainHandler struct {
	Crawler    NewsCrawler
	Chain      NewsHandler
	Parser     NewsArticleParser             // nil 허용: 파서 없으면 파싱 건너뜀
	ListParser NewsListParser                // nil 허용: nil이면 카테고리 페이지 체이닝 건너뜀
	Publisher  JobPublisher                  // nil 허용: nil이면 체이닝 발행 건너뜀
	Repo       storage.NewsArticleRepository // nil 허용: repo 없으면 DB 저장 건너뜀
	Log        *logger.Logger
}

// NewChainHandler는 새로운 ChainHandler를 생성합니다.
func NewChainHandler(
	crawler NewsCrawler,
	chain NewsHandler,
	parser NewsArticleParser,
	listParser NewsListParser,
	publisher JobPublisher,
	repo storage.NewsArticleRepository,
	log *logger.Logger,
) *ChainHandler {
	return &ChainHandler{
		Crawler:    crawler,
		Chain:      chain,
		Parser:     parser,
		ListParser: listParser,
		Publisher:  publisher,
		Repo:       repo,
		Log:        log,
	}
}

// Handle은 CrawlJob을 chain을 통해 처리하고 파싱된 Content(s)를 반환합니다.
//
// 처리 흐름:
//  1. RSS 피드 (fetch_strategy=rss): newshandler.ConvertRSSArticles로 다건 변환하여 반환
//  2. 카테고리/목록 페이지 (TargetTypeCategory): ParseList → Publisher.Publish → nil 반환
//  3. 기사 페이지 (TargetTypeArticle): ParseArticle → ConvertArticle → 단건 반환
func (h *ChainHandler) Handle(ctx context.Context, job *core.CrawlJob) ([]*core.Content, error) {
	raw, err := h.Chain.Handle(ctx, job)
	if err != nil {
		return nil, err
	}

	// 1. RSS result: fetch_strategy가 "rss"이면 newshandler가 metadata에서 기사 목록 변환
	if strategy, ok := raw.Metadata["fetch_strategy"].(string); ok && strategy == "rss" {
		return newshandler.ConvertRSSArticles(raw), nil
	}

	// 2. Category/List page: 기사 URL 목록을 추출하여 다음 CrawlJob으로 연결
	if job.Target.Type == core.TargetTypeCategory && h.ListParser != nil && h.Publisher != nil {
		return nil, h.publishArticleJobs(ctx, job, raw)
	}

	// 3. HTML article result: 기사 페이지에서 단일 Content 파싱
	isArticle := job.Target.Type == core.TargetTypeArticle
	canParse := raw.HTML != "" && h.Parser != nil

	if !isArticle || !canParse {
		h.Log.WithFields(map[string]interface{}{
			"is_article": isArticle,
			"has_html":   raw.HTML != "",
			"has_parser": h.Parser != nil,
		}).Debug("skipping parse: conditions not met")
		return nil, nil
	}

	parsed, err := h.Parser.ParseArticle(raw)
	if err != nil {
		h.Log.WithError(err).Warn("article parse failed, skipping")
		return nil, nil
	}

	article := newshandler.Article{
		Title:       parsed.Title,
		Body:        parsed.Body,
		Summary:     parsed.Summary,
		Author:      parsed.Author,
		URL:         parsed.URL,
		Category:    parsed.Category,
		Tags:        parsed.Tags,
		ImageURLs:   parsed.ImageURLs,
		PublishedAt: parsed.PublishedAt,
	}

	if h.Repo != nil {
		if err := h.Repo.Insert(ctx, newshandler.ArticleToRecord(article, raw)); err != nil {
			h.Log.WithError(err).Warn("failed to save news article to db")
		}
	}

	return []*core.Content{newshandler.ConvertArticle(article, raw)}, nil
}

// publishArticleJobs는 카테고리/목록 페이지에서 기사 URL을 추출하여 CrawlJob으로 발행합니다.
// 발견된 기사 URL은 TargetTypeArticle로 변환되어 Kafka crawl 토픽에 발행됩니다.
func (h *ChainHandler) publishArticleJobs(ctx context.Context, job *core.CrawlJob, raw *core.RawContent) error {
	items, err := h.ListParser.ParseList(raw)
	if err != nil {
		h.Log.WithFields(map[string]interface{}{
			"crawler": job.CrawlerName,
			"url":     job.Target.URL,
		}).WithError(err).Warn("list parse failed, skipping chained jobs")
		return nil
	}

	if len(items) == 0 {
		h.Log.WithFields(map[string]interface{}{
			"crawler": job.CrawlerName,
			"url":     job.Target.URL,
		}).Debug("no article links found in category page")
		return nil
	}

	urls := make([]string, 0, len(items))
	for _, item := range items {
		if item.URL != "" {
			urls = append(urls, item.URL)
		}
	}

	if err := h.Publisher.Publish(ctx, job.CrawlerName, urls, core.TargetTypeArticle, job.Timeout); err != nil {
		h.Log.WithFields(map[string]interface{}{
			"crawler":   job.CrawlerName,
			"url":       job.Target.URL,
			"job_count": len(urls),
		}).WithError(err).Error("failed to publish chained article jobs")
		return err
	}

	h.Log.WithFields(map[string]interface{}{
		"crawler":   job.CrawlerName,
		"url":       job.Target.URL,
		"job_count": len(urls),
	}).Info("chained article jobs published from category page")

	return nil
}
