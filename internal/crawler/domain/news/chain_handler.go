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
//
// ChainHandler adapts a NewsHandler chain to the handler.Handler interface.
// RSS feeds yield multiple Content items; HTML article pages yield one.
type ChainHandler struct {
	Crawler NewsCrawler
	Chain   NewsHandler
	Parser  NewsArticleParser             // nil 허용: 파서 없으면 파싱 건너뜀
	Repo    storage.NewsArticleRepository // nil 허용: repo 없으면 DB 저장 건너뜀
	Log     *logger.Logger
}

// NewChainHandler는 새로운 ChainHandler를 생성합니다.
func NewChainHandler(
	crawler NewsCrawler,
	chain NewsHandler,
	parser NewsArticleParser,
	repo storage.NewsArticleRepository,
	log *logger.Logger,
) *ChainHandler {
	return &ChainHandler{
		Crawler: crawler,
		Chain:   chain,
		Parser:  parser,
		Repo:    repo,
		Log:     log,
	}
}

// Handle은 CrawlJob을 chain을 통해 처리하고 파싱된 Content(s)를 반환합니다.
// RSS 피드는 newshandler.ConvertRSSArticles로 다건 변환합니다.
// HTML 기사 페이지는 파싱 후 newshandler.ConvertArticle로 단건 변환하며,
// Repo가 설정된 경우 DB에도 저장합니다.
func (h *ChainHandler) Handle(ctx context.Context, job *core.CrawlJob) ([]*core.Content, error) {
	raw, err := h.Chain.Handle(ctx, job)
	if err != nil {
		return nil, err
	}

	// RSS result: fetch_strategy가 "rss"이면 newshandler가 metadata에서 기사 목록 변환
	if strategy, ok := raw.Metadata["fetch_strategy"].(string); ok && strategy == "rss" {
		return newshandler.ConvertRSSArticles(raw), nil
	}

	// HTML article result: 기사 페이지에서 단일 Content 파싱
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
