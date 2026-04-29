package fetcher

import (
	"context"
	"sync"

	"issuetracker/internal/crawler/core"
	cdp "issuetracker/internal/crawler/implementation/chromedp"
)

// BrowserFetcher 는 ChromedpCrawler 를 general.Fetcher 인터페이스로 감싸는 어댑터입니다.
// 첫 Fetch 호출 시 브라우저를 lazy initialize.
//
// Retry-on-call 정책 (Coderabbit 피드백):
//   - 기존 sync.Once 는 Initialize 가 transient (네트워크/리소스 일시 장애) 로 실패해도
//     영구 cache 되어 모든 후속 Fetch 가 실패. 프로세스 재기동 외에는 회복 불가.
//   - Mutex + initialized flag 로 변경 — 실패 시 다음 Fetch 호출에서 재시도.
//   - Initialize 와 flag 갱신은 mutex 보호 (race 회피).
type BrowserFetcher struct {
	crawler     *cdp.ChromedpCrawler
	config      core.Config
	mu          sync.Mutex
	initialized bool
}

func NewBrowserFetcher(crawler *cdp.ChromedpCrawler, config core.Config) *BrowserFetcher {
	return &BrowserFetcher{crawler: crawler, config: config}
}

func (f *BrowserFetcher) Fetch(ctx context.Context, target core.Target) (*core.RawContent, error) {
	if err := f.ensureInitialized(ctx); err != nil {
		return nil, err
	}
	return f.crawler.Fetch(ctx, target)
}

// ensureInitialized 는 Initialize 가 아직 성공하지 못했으면 본 호출에서 재시도합니다.
// 실패 시 initialized 가 false 로 남아 다음 호출에서 다시 시도 — transient 장애 회복 가능.
func (f *BrowserFetcher) ensureInitialized(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.initialized {
		return nil
	}
	if err := f.crawler.Initialize(ctx, f.config); err != nil {
		return err
	}
	f.initialized = true
	return nil
}
