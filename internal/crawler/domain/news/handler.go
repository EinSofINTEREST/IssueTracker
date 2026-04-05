package news

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"issuetracker/internal/crawler/core"
	"issuetracker/pkg/logger"
)

// NewsHandler는 Chain of Responsibility 체인에서 하나의 링크를 나타냅니다.
// Handle 실패 시 SetNext로 연결된 다음 핸들러로 위임합니다.
//
// NewsHandler is a single link in the Chain of Responsibility.
// On failure, it delegates to the next handler set via SetNext.
type NewsHandler interface {
	Handle(ctx context.Context, job *core.CrawlJob) (*core.RawContent, error)

	// SetNext는 이 핸들러가 실패했을 때 위임할 다음 핸들러를 설정합니다.
	SetNext(h NewsHandler)
}

// baseNewsHandler는 체인 위임 메커니즘을 제공하는 공통 구조체입니다.
// 모든 구체 뉴스 핸들러가 이 구조체를 임베드합니다.
type baseNewsHandler struct {
	next NewsHandler
	log  *logger.Logger
}

func (b *baseNewsHandler) SetNext(h NewsHandler) {
	b.next = h
}

// delegateToNext는 next 핸들러가 있으면 위임하고, 없으면 에러를 반환합니다.
func (b *baseNewsHandler) delegateToNext(ctx context.Context, job *core.CrawlJob, reason error) (*core.RawContent, error) {
	if b.next == nil {
		return nil, fmt.Errorf("all fetch strategies exhausted for %s: %w", job.Target.URL, reason)
	}
	return b.next.Handle(ctx, job)
}

// RSSFetchHandler는 RSS 피드로 기사를 가져옵니다.
// Target.Type이 feed이거나 metadata["feed_url"]이 있을 때만 RSS를 시도합니다.
// 실패하거나 RSS 대상이 아니면 다음 핸들러(GoQueryFetchHandler)로 위임합니다.
//
// RSSFetchHandler fetches articles from an RSS feed.
// It only attempts RSS when the target is a feed or metadata["feed_url"] is set.
type RSSFetchHandler struct {
	baseNewsHandler
	fetcher NewsRSSFetcher
}

// NewRSSFetchHandler는 새로운 RSSFetchHandler를 생성합니다.
func NewRSSFetchHandler(fetcher NewsRSSFetcher, log *logger.Logger) *RSSFetchHandler {
	return &RSSFetchHandler{
		baseNewsHandler: baseNewsHandler{log: log},
		fetcher:         fetcher,
	}
}

// Handle은 RSS 피드에서 기사를 가져옵니다.
// RSS 대상이 아닌 경우 즉시 다음 핸들러로 위임합니다.
func (h *RSSFetchHandler) Handle(ctx context.Context, job *core.CrawlJob) (*core.RawContent, error) {
	feedURL, ok := h.rssFeedURL(job)
	if !ok {
		// RSS를 지원하지 않는 target은 바로 다음으로 위임
		return h.delegateToNext(ctx, job, fmt.Errorf("not a feed target"))
	}

	articles, err := h.fetcher.FetchFeed(ctx, feedURL)
	if err != nil {
		h.log.WithFields(map[string]interface{}{
			"handler":  "rss",
			"feed_url": feedURL,
		}).WithError(err).Warn("rss fetch failed, delegating to next handler")

		return h.delegateToNext(ctx, job, err)
	}

	h.log.WithFields(map[string]interface{}{
		"handler":       "rss",
		"feed_url":      feedURL,
		"article_count": len(articles),
	}).Info("rss feed fetched successfully")

	return buildRSSRawContent(job, articles), nil
}

// rssFeedURL은 job에서 RSS 피드 URL을 추출합니다.
// Target.Type이 feed면 Target.URL을, metadata["feed_url"]이 있으면 그 값을 사용합니다.
func (h *RSSFetchHandler) rssFeedURL(job *core.CrawlJob) (string, bool) {
	if job.Target.Type == core.TargetTypeFeed {
		return job.Target.URL, true
	}
	if val, ok := job.Target.Metadata["feed_url"]; ok {
		if url, ok := val.(string); ok && url != "" {
			return url, true
		}
	}
	return "", false
}

// buildRSSRawContent는 RSS 기사 목록을 RawContent로 변환합니다.
// HTML이 없으므로 Metadata["rss_articles"]에 기사 정보를 직렬화합니다.
func buildRSSRawContent(job *core.CrawlJob, articles []*NewsArticle) *core.RawContent {
	items := make([]map[string]interface{}, 0, len(articles))
	for _, a := range articles {
		items = append(items, map[string]interface{}{
			"title":        a.Title,
			"url":          a.URL,
			"body":         a.Body,
			"author":       a.Author,
			"published_at": a.PublishedAt.Format(time.RFC3339),
		})
	}

	return &core.RawContent{
		ID:         fmt.Sprintf("rss-%d", time.Now().UnixNano()),
		FetchedAt:  time.Now(),
		URL:        job.Target.URL,
		HTML:       "",
		StatusCode: 200,
		Headers:    make(map[string]string),
		Metadata: map[string]interface{}{
			"rss_articles":   items,
			"fetch_strategy": "rss",
		},
	}
}

// GoQueryFetchHandler는 goquery를 사용하여 HTML을 정적으로 크롤링합니다.
// 실패하면 다음 핸들러(BrowserFetchHandler)로 위임합니다.
//
// GoQueryFetchHandler fetches HTML via static scraping with goquery.
// On failure, delegates to the next handler (typically BrowserFetchHandler).
type GoQueryFetchHandler struct {
	baseNewsHandler
	fetcher      NewsFetcher
	lazyKeywords []string // HTML에 이 키워드가 있으면 lazy loading으로 판단, browser로 위임
}

// NewGoQueryFetchHandler는 새로운 GoQueryFetchHandler를 생성합니다.
// lazyKeywords가 지정되면, 응답 HTML에 해당 키워드가 포함될 때 browser로 위임합니다.
func NewGoQueryFetchHandler(fetcher NewsFetcher, log *logger.Logger, lazyKeywords ...string) *GoQueryFetchHandler {
	return &GoQueryFetchHandler{
		baseNewsHandler: baseNewsHandler{log: log},
		fetcher:         fetcher,
		lazyKeywords:    lazyKeywords,
	}
}

// Handle은 goquery로 HTML 페이지를 가져옵니다.
// lazyKeywords가 설정된 경우, 응답 HTML에 해당 키워드가 포함되면 lazy loading 페이지로 판단하여
// browser 핸들러로 위임합니다.
func (h *GoQueryFetchHandler) Handle(ctx context.Context, job *core.CrawlJob) (*core.RawContent, error) {
	raw, err := h.fetcher.Fetch(ctx, job.Target)
	if err != nil {
		// URL 자체가 존재하지 않는 경우 다른 전략도 동일하게 실패하므로 즉시 반환
		var crawlerErr *core.CrawlerError
		if errors.As(err, &crawlerErr) && crawlerErr.Category == core.ErrCategoryNotFound {
			h.log.WithFields(map[string]interface{}{
				"handler": "goquery",
				"url":     job.Target.URL,
			}).WithError(err).Error("resource not found, aborting chain")
			return nil, err
		}

		h.log.WithFields(map[string]interface{}{
			"handler": "goquery",
			"url":     job.Target.URL,
		}).WithError(err).Warn("goquery fetch failed, delegating to next handler")

		return h.delegateToNext(ctx, job, err)
	}

	if h.hasLazyContent(raw.HTML) {
		h.log.WithFields(map[string]interface{}{
			"handler": "goquery",
			"url":     job.Target.URL,
		}).Warn("lazy loading detected, delegating to browser handler")
		return h.delegateToNext(ctx, job, fmt.Errorf("lazy loading content detected"))
	}

	h.log.WithFields(map[string]interface{}{
		"handler": "goquery",
		"url":     job.Target.URL,
	}).Info("goquery fetch succeeded")
	return raw, nil
}

// hasLazyContent는 HTML에 lazy loading 관련 키워드가 포함되어 있는지 확인합니다.
func (h *GoQueryFetchHandler) hasLazyContent(html string) bool {
	if len(h.lazyKeywords) == 0 {
		return false
	}
	lower := strings.ToLower(html)
	for _, kw := range h.lazyKeywords {
		if strings.Contains(lower, strings.ToLower(kw)) {
			return true
		}
	}
	return false
}

// BrowserFetchHandler는 헤드리스 브라우저(chromedp)로 크롤링합니다.
// 체인의 마지막 핸들러이므로 실패 시 에러를 직접 반환합니다.
//
// BrowserFetchHandler fetches pages via headless browser (chromedp).
// As the terminal handler in the chain, it returns errors directly.
type BrowserFetchHandler struct {
	baseNewsHandler
	fetcher NewsFetcher
}

// NewBrowserFetchHandler는 새로운 BrowserFetchHandler를 생성합니다.
func NewBrowserFetchHandler(fetcher NewsFetcher, log *logger.Logger) *BrowserFetchHandler {
	return &BrowserFetchHandler{
		baseNewsHandler: baseNewsHandler{log: log},
		fetcher:         fetcher,
	}
}

// Handle은 헤드리스 브라우저로 페이지를 가져옵니다.
func (h *BrowserFetchHandler) Handle(ctx context.Context, job *core.CrawlJob) (*core.RawContent, error) {
	raw, err := h.fetcher.Fetch(ctx, job.Target)
	if err != nil {
		h.log.WithFields(map[string]interface{}{
			"handler": "browser",
			"url":     job.Target.URL,
		}).WithError(err).Error("browser fetch failed, all strategies exhausted")

		return nil, err
	}

	h.log.WithFields(map[string]interface{}{
		"handler": "browser",
		"url":     job.Target.URL,
	}).Info("browser fetch succeeded")
	return raw, nil
}

// BuildChain은 RSS → GoQuery → Browser 순서의 체인을 조립합니다.
// nil fetcher는 체인에서 제외되므로 소스별로 지원하는 전략만 포함할 수 있습니다.
// 최소 하나의 fetcher가 non-nil이어야 합니다.
// lazyKeywords가 지정되면, GoQuery 응답 HTML에 해당 키워드가 포함될 때 browser로 위임합니다.
//
// BuildChain assembles the chain: RSS → GoQuery → Browser.
// Nil fetchers are skipped, allowing per-source strategy configuration.
// Panics if no fetcher is provided.
func BuildChain(
	rssFetcher NewsRSSFetcher,
	goqueryFetcher NewsFetcher,
	browserFetcher NewsFetcher,
	log *logger.Logger,
	lazyKeywords ...string,
) NewsHandler {
	var handlers []NewsHandler

	if rssFetcher != nil {
		handlers = append(handlers, NewRSSFetchHandler(rssFetcher, log))
	}
	if goqueryFetcher != nil {
		handlers = append(handlers, NewGoQueryFetchHandler(goqueryFetcher, log, lazyKeywords...))
	}
	if browserFetcher != nil {
		handlers = append(handlers, NewBrowserFetchHandler(browserFetcher, log))
	}

	if len(handlers) == 0 {
		panic("BuildChain: at least one fetcher must be non-nil")
	}

	// 체인 연결: 각 핸들러의 next를 다음 핸들러로 설정
	for i := 0; i < len(handlers)-1; i++ {
		handlers[i].SetNext(handlers[i+1])
	}

	return handlers[0]
}
