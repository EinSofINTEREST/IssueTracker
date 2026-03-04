// Package fetcher는 NewsFetcher와 NewsRSSFetcher의 구체 구현을 제공합니다.
//
// Package fetcher provides concrete implementations of NewsFetcher and NewsRSSFetcher.
// Chain handlers depend only on the interfaces defined in the news package.
package fetcher

import (
	"context"
	"time"

	"github.com/mmcdole/gofeed"

	"issuetracker/internal/crawler/core"
	"issuetracker/internal/crawler/domain/news"
	"issuetracker/pkg/logger"
)

// RSSFetcher는 gofeed 라이브러리를 사용한 NewsRSSFetcher 구현입니다.
//
// RSSFetcher implements news.NewsRSSFetcher using the gofeed library.
type RSSFetcher struct {
	parser *gofeed.Parser
	source core.SourceInfo
	log    *logger.Logger
}

// NewRSSFetcher는 새로운 RSSFetcher를 생성합니다.
func NewRSSFetcher(source core.SourceInfo, log *logger.Logger) *RSSFetcher {
	return &RSSFetcher{
		parser: gofeed.NewParser(),
		source: source,
		log:    log,
	}
}

// FetchFeed는 RSS/Atom 피드 URL에서 기사 목록을 가져오고 파싱합니다.
// context 취소를 지원하며, 네트워크 오류 시 core.NewNetworkError를 반환합니다.
func (f *RSSFetcher) FetchFeed(ctx context.Context, feedURL string) ([]*news.NewsArticle, error) {
	feed, err := f.parser.ParseURLWithContext(feedURL, ctx)
	if err != nil {
		return nil, core.NewNetworkError("NET_001", "failed to fetch rss feed", feedURL, err)
	}

	articles := make([]*news.NewsArticle, 0, len(feed.Items))
	for _, item := range feed.Items {
		article := f.convertItem(item)
		if article == nil {
			continue
		}
		articles = append(articles, article)
	}

	f.log.WithFields(map[string]interface{}{
		"feed_url": feedURL,
		"total":    len(feed.Items),
		"parsed":   len(articles),
	}).Info("rss 피드 fetch 완료")

	return articles, nil
}

// convertItem은 gofeed.Item을 NewsArticle로 변환합니다.
// Title 또는 Link가 없으면 nil을 반환하여 해당 항목을 건너뜁니다.
func (f *RSSFetcher) convertItem(item *gofeed.Item) *news.NewsArticle {
	if item.Title == "" || item.Link == "" {
		return nil
	}

	article := &news.NewsArticle{
		Title:   item.Title,
		URL:     item.Link,
		Body:    item.Description,
		Summary: item.Description,
	}

	if item.PublishedParsed != nil {
		article.PublishedAt = *item.PublishedParsed
	} else {
		article.PublishedAt = time.Now()
	}

	if item.Author != nil {
		article.Author = item.Author.Name
	}

	return article
}
