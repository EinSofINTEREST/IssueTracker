package news

import (
	"context"
	"fmt"

	"issuetracker/internal/crawler/core"
	newshandler "issuetracker/internal/crawler/domain/news/handler"
	"issuetracker/internal/storage"
	"issuetracker/pkg/logger"
)

const (
	// maxChainedURLs는 단일 카테고리 페이지에서 발행할 수 있는 최대 기사 URL 수입니다.
	// 광고/외부 링크가 혼재된 목록에서 Kafka 토픽 폭증을 방지합니다.
	maxChainedURLs = 200
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
// chain과 log는 nil이 허용되지 않습니다. nil 전달 시 Handle 호출에서 에러를 반환합니다.
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
	if h.Chain == nil || h.Log == nil {
		return nil, fmt.Errorf("ChainHandler is not properly initialized: chain or log is nil")
	}

	raw, err := h.Chain.Handle(ctx, job)
	if err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, fmt.Errorf("chain returned nil raw content for %s", job.Target.URL)
	}

	// 1. RSS result: fetch_strategy가 "rss"이면 newshandler가 metadata에서 기사 목록 변환
	if strategy, ok := raw.Metadata["fetch_strategy"].(string); ok && strategy == "rss" {
		return newshandler.ConvertRSSArticles(raw), nil
	}

	// 2. Category/List page: 기사 URL 목록을 추출하여 다음 CrawlJob으로 연결
	if job.Target.Type == core.TargetTypeCategory {
		if h.ListParser == nil || h.Publisher == nil {
			h.Log.WithFields(map[string]interface{}{
				"crawler":    job.CrawlerName,
				"url":        job.Target.URL,
				"has_parser": h.ListParser != nil,
				"has_pub":    h.Publisher != nil,
			}).Debug("category job skipped: list_parser or publisher not configured")
			return nil, nil
		}
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
// ParseList 실패는 에러로 반환하여 worker의 재시도/DLQ 경로를 활성화합니다.
// maxChainedURLs 초과분은 잘라내고 중복 URL은 제거합니다.
func (h *ChainHandler) publishArticleJobs(ctx context.Context, job *core.CrawlJob, raw *core.RawContent) error {
	items, err := h.ListParser.ParseList(raw)
	if err != nil {
		// ParseList 실패는 에러로 전파하여 worker가 재시도/DLQ 경로로 처리할 수 있게 합니다.
		// warn 후 nil 반환(silent drop)은 해당 주기 데이터 영구 손실로 이어집니다.
		return fmt.Errorf("parse list for %s: %w", job.Target.URL, err)
	}

	if len(items) == 0 {
		h.Log.WithFields(map[string]interface{}{
			"crawler": job.CrawlerName,
			"url":     job.Target.URL,
		}).Debug("no article links found in category page")
		return nil
	}

	// 중복 제거 후 상한 적용
	urls := uniqueURLs(items, maxChainedURLs)

	if err := h.Publisher.Publish(ctx, job.CrawlerName, urls, core.TargetTypeArticle, job.Timeout); err != nil {
		return fmt.Errorf("publish chained jobs for %s: %w", job.Target.URL, err)
	}

	h.Log.WithFields(map[string]interface{}{
		"crawler":     job.CrawlerName,
		"url":         job.Target.URL,
		"url_count":   len(urls),
		"target_type": string(core.TargetTypeArticle),
	}).Info("chained article jobs published from category page")

	return nil
}

// uniqueURLs는 NewsItem 목록에서 빈 URL을 제거하고 고유 URL만 limit 개수만큼 반환합니다.
// 입력 순서를 유지하며, 먼저 등장한 URL이 우선됩니다.
func uniqueURLs(items []NewsItem, limit int) []string {
	seen := make(map[string]struct{}, len(items))
	urls := make([]string, 0, min(len(items), limit))

	for _, item := range items {
		if item.URL == "" {
			continue
		}
		if _, dup := seen[item.URL]; dup {
			continue
		}
		seen[item.URL] = struct{}{}
		urls = append(urls, item.URL)
		if len(urls) >= limit {
			break
		}
	}

	return urls
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
