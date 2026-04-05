package chromedp

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"

	"issuetracker/internal/crawler/core"
	"issuetracker/pkg/logger"
)

// Fetch: 헤드리스 브라우저로 페이지를 렌더링하고 HTML 가져오기
// JS 렌더링 완료 후의 DOM을 반환
func (c *ChromedpCrawler) Fetch(ctx context.Context, target core.Target) (*core.RawContent, error) {
	log := logger.FromContext(ctx)

	if c.allocCtx == nil {
		return nil, &core.CrawlerError{
			Category: core.ErrCategoryInternal,
			Code:     "CDP_001",
			Message:  "browser not initialized, call Initialize() first",
			Source:   c.name,
			URL:      target.URL,
		}
	}

	// 탭 생성 (요청별 격리)
	tabCtx, cancel := chromedp.NewContext(c.allocCtx)
	defer cancel()

	// Timeout 적용
	tabCtx, timeoutCancel := context.WithTimeout(tabCtx, c.config.Timeout)
	defer timeoutCancel()

	// 메인 document의 HTTP 상태코드 캡처
	var (
		statusCode int64 = 200
		statusMu   sync.Mutex
	)
	chromedp.ListenTarget(tabCtx, func(ev interface{}) {
		if e, ok := ev.(*network.EventResponseReceived); ok {
			if e.Type == network.ResourceTypeDocument {
				statusMu.Lock()
				statusCode = int64(e.Response.Status)
				statusMu.Unlock()
			}
		}
	})

	// 렌더링 액션 구성 (network 이벤트 활성화 포함)
	actions := []chromedp.Action{network.Enable()}
	actions = append(actions, c.buildFetchActions(target.URL)...)

	var html string
	actions = append(actions, chromedp.OuterHTML("html", &html))

	start := time.Now()

	// 브라우저 실행
	if err := chromedp.Run(tabCtx, actions...); err != nil {
		return nil, &core.CrawlerError{
			Category:  core.ErrCategoryNetwork,
			Code:      "CDP_002",
			Message:   "failed to render page",
			Source:    c.name,
			URL:       target.URL,
			Retryable: true,
			Err:       err,
		}
	}

	elapsed := time.Since(start)

	statusMu.Lock()
	capturedStatus := int(statusCode)
	statusMu.Unlock()

	// HTTP 상태코드 검사
	if capturedStatus == 404 {
		return nil, core.NewNotFoundError(target.URL)
	}
	if capturedStatus == 429 {
		return nil, core.NewRateLimitError("HTTP_429", "rate limited", target.URL, capturedStatus)
	}
	if capturedStatus >= 500 {
		return nil, &core.CrawlerError{
			Category:   core.ErrCategoryNetwork,
			Code:       fmt.Sprintf("HTTP_%d", capturedStatus),
			Message:    "server error",
			Source:     c.name,
			URL:        target.URL,
			StatusCode: capturedStatus,
			Retryable:  true,
		}
	}
	if capturedStatus >= 400 {
		return nil, &core.CrawlerError{
			Category:   core.ErrCategoryInternal,
			Code:       fmt.Sprintf("HTTP_%d", capturedStatus),
			Message:    "client error",
			Source:     c.name,
			URL:        target.URL,
			StatusCode: capturedStatus,
			Retryable:  false,
		}
	}

	rawContent := &core.RawContent{
		ID:         fmt.Sprintf("%s-%d", c.name, time.Now().UnixNano()),
		SourceInfo: c.sourceInfo,
		FetchedAt:  time.Now(),
		URL:        target.URL,
		HTML:       html,
		StatusCode: capturedStatus,
		Headers:    make(map[string]string),
		Metadata:   target.Metadata,
	}

	log.WithFields(map[string]interface{}{
		"url":         target.URL,
		"size":        len(html),
		"duration_ms": elapsed.Milliseconds(),
	}).Info("page rendered successfully with chromedp")

	return rawContent, nil
}

// buildFetchActions: 크롤링 옵션에 따라 chromedp 액션 목록 생성
func (c *ChromedpCrawler) buildFetchActions(url string) []chromedp.Action {
	actions := []chromedp.Action{
		chromedp.Navigate(url),
	}

	// 특정 selector 대기
	if c.opts.WaitSelector != "" {
		actions = append(actions,
			chromedp.WaitVisible(c.opts.WaitSelector, chromedp.ByQuery),
		)
	}

	// 페이지 안정화 대기 (DOM 변경이 멈출 때까지)
	if c.opts.WaitStable {
		actions = append(actions, chromedp.Sleep(2*time.Second))
	}

	return actions
}
