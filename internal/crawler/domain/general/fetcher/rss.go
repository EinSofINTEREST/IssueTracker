package fetcher

import (
	"context"
	"time"

	"github.com/mmcdole/gofeed"

	"issuetracker/internal/crawler/core"
	"issuetracker/internal/crawler/parser"
	"issuetracker/pkg/logger"
)

// RSSFetcher 는 gofeed 기반 general.RSSFetcher 구현입니다.
// Feed item → parser.Page 로 변환 (도메인 중립).
type RSSFetcher struct {
	parser *gofeed.Parser
	source core.SourceInfo
	log    *logger.Logger
}

func NewRSSFetcher(source core.SourceInfo, log *logger.Logger) *RSSFetcher {
	return &RSSFetcher{
		parser: gofeed.NewParser(),
		source: source,
		log:    log,
	}
}

// FetchFeed 는 RSS/Atom 피드를 가져와 parser.Page 슬라이스로 반환합니다.
func (f *RSSFetcher) FetchFeed(ctx context.Context, feedURL string) ([]*parser.Page, error) {
	feed, err := f.parser.ParseURLWithContext(feedURL, ctx)
	if err != nil {
		return nil, core.NewNetworkError("NET_001", "failed to fetch rss feed", feedURL, err)
	}

	pages := make([]*parser.Page, 0, len(feed.Items))
	for _, item := range feed.Items {
		p := f.convertItem(item)
		if p == nil {
			continue
		}
		pages = append(pages, p)
	}

	f.log.WithFields(map[string]interface{}{
		"feed_url": feedURL,
		"total":    len(feed.Items),
		"parsed":   len(pages),
	}).Info("rss feed fetched successfully")

	return pages, nil
}

// convertItem 은 gofeed.Item 을 parser.Page 로 변환합니다.
// Title 또는 Link 누락 시 nil 반환 (해당 item skip).
func (f *RSSFetcher) convertItem(item *gofeed.Item) *parser.Page {
	if item.Title == "" || item.Link == "" {
		return nil
	}
	page := &parser.Page{
		Title:       item.Title,
		URL:         item.Link,
		MainContent: item.Description,
		Summary:     item.Description,
	}
	if item.PublishedParsed != nil {
		page.PublishedAt = *item.PublishedParsed
	} else {
		page.PublishedAt = time.Now()
	}
	if item.Author != nil {
		page.Author = item.Author.Name
	}
	return page
}
