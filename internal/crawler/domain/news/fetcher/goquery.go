package fetcher

import (
	"context"

	"issuetracker/internal/crawler/core"
	"issuetracker/internal/crawler/implementation/goquery"
)

// GoqueryFetcher는 GoqueryCrawler를 news.NewsFetcher 인터페이스로 감싸는 어댑터입니다.
// Chain of Responsibility 핸들러는 GoqueryCrawler 구체 타입이 아닌
// news.NewsFetcher 인터페이스에만 의존합니다.
//
// GoqueryFetcher adapts GoqueryCrawler to the news.NewsFetcher interface.
// Chain handlers depend only on NewsFetcher, never on GoqueryCrawler directly.
type GoqueryFetcher struct {
	crawler *goquery.GoqueryCrawler
}

// NewGoqueryFetcher는 새로운 GoqueryFetcher를 생성합니다.
func NewGoqueryFetcher(crawler *goquery.GoqueryCrawler) *GoqueryFetcher {
	return &GoqueryFetcher{crawler: crawler}
}

// Fetch는 GoqueryCrawler.Fetch를 호출하고 결과를 반환합니다.
func (f *GoqueryFetcher) Fetch(ctx context.Context, target core.Target) (*core.RawContent, error) {
	return f.crawler.Fetch(ctx, target)
}
