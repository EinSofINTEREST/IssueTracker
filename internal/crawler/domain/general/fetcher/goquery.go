// Package fetcher 는 general.Fetcher 구현체들을 제공합니다.
//
// Package fetcher provides concrete general.Fetcher adapters.
// chain handlers depend only on the interfaces defined in the general package.
package fetcher

import (
	"context"

	"issuetracker/internal/crawler/core"
	"issuetracker/internal/crawler/implementation/goquery"
)

// GoqueryFetcher 는 GoqueryCrawler 를 general.Fetcher 인터페이스로 감싸는 어댑터입니다.
type GoqueryFetcher struct {
	crawler *goquery.GoqueryCrawler
}

func NewGoqueryFetcher(crawler *goquery.GoqueryCrawler) *GoqueryFetcher {
	return &GoqueryFetcher{crawler: crawler}
}

func (f *GoqueryFetcher) Fetch(ctx context.Context, target core.Target) (*core.RawContent, error) {
	return f.crawler.Fetch(ctx, target)
}
