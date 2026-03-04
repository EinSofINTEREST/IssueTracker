package fetcher

import (
	"context"
	"sync"

	"issuetracker/internal/crawler/core"
	cdp "issuetracker/internal/crawler/implementation/chromedp"
)

// BrowserFetcher는 ChromedpCrawler를 news.NewsFetcher 인터페이스로 감싸는 어댑터입니다.
// JavaScript 렌더링이 필요한 페이지에 사용되며, 처음 Fetch 호출 시 브라우저를 초기화합니다.
//
// BrowserFetcher adapts ChromedpCrawler to the news.NewsFetcher interface.
// Browser initialization is deferred until the first Fetch call (lazy init).
type BrowserFetcher struct {
	crawler *cdp.ChromedpCrawler
	config  core.Config
	once    sync.Once
	initErr error
}

// NewBrowserFetcher는 새로운 BrowserFetcher를 생성합니다.
// 브라우저 초기화는 첫 번째 Fetch 호출 시 지연 실행됩니다.
func NewBrowserFetcher(crawler *cdp.ChromedpCrawler, config core.Config) *BrowserFetcher {
	return &BrowserFetcher{
		crawler: crawler,
		config:  config,
	}
}

// Fetch는 헤드리스 브라우저로 페이지를 가져옵니다.
// 첫 호출 시 ChromedpCrawler.Initialize를 실행하여 브라우저를 준비합니다.
func (f *BrowserFetcher) Fetch(ctx context.Context, target core.Target) (*core.RawContent, error) {
	// 첫 Fetch 호출 시만 Initialize 실행 (브라우저 비용 지연).
	// once.Do 완료 후 happens-before 관계가 성립하므로 f.initErr 읽기는 잠금 없이 안전합니다.
	f.once.Do(func() {
		f.initErr = f.crawler.Initialize(ctx, f.config)
	})

	if f.initErr != nil {
		return nil, f.initErr
	}

	return f.crawler.Fetch(ctx, target)
}
