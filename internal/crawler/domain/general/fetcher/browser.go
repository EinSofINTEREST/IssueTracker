package fetcher

import (
	"context"
	"sync"

	"issuetracker/internal/crawler/core"
	cdp "issuetracker/internal/crawler/implementation/chromedp"
)

// BrowserFetcher 는 ChromedpCrawler 를 general.Fetcher 인터페이스로 감싸는 어댑터입니다.
// 첫 Fetch 호출 시 브라우저를 lazy initialize.
type BrowserFetcher struct {
	crawler *cdp.ChromedpCrawler
	config  core.Config
	once    sync.Once
	initErr error
}

func NewBrowserFetcher(crawler *cdp.ChromedpCrawler, config core.Config) *BrowserFetcher {
	return &BrowserFetcher{crawler: crawler, config: config}
}

func (f *BrowserFetcher) Fetch(ctx context.Context, target core.Target) (*core.RawContent, error) {
	f.once.Do(func() {
		f.initErr = f.crawler.Initialize(ctx, f.config)
	})
	if f.initErr != nil {
		return nil, f.initErr
	}
	return f.crawler.Fetch(ctx, target)
}
