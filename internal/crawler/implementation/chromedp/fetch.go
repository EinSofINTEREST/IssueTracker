package chromedp

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"

	"issuetracker/internal/crawler/core"
	"issuetracker/pkg/logger"
)

// Fetch: 헤드리스 브라우저로 페이지를 렌더링하고 HTML 가져오기
// JS 렌더링 완료 후의 DOM을 반환한다.
//
// timeout 처리:
//   - 액션 실행은 runCtx(timeout 적용)로 수행하지만, 탭 자체는 browserCtx(timeout 미적용)로 유지
//   - timeout 발생 시 browserCtx로 OuterHTML 재캡처를 시도하는 graceful fallback 적용
//   - 캡처 결과가 IsValidPartialDOM 검증을 통과하면 partial_load 플래그와 함께 반환
//   - 캡처 자체 실패/검증 실패 시에는 timeout 카테고리 에러로 분류
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
	// browserCtx는 timeout이 적용되지 않은 tab context로, graceful capture 시 재사용된다.
	browserCtx, browserCancel := chromedp.NewContext(c.allocCtx)
	defer browserCancel()

	// 액션 실행 전용 timeout context
	// timeout 발생 시 runCtx만 cancel되고 browserCtx와 탭은 유효 상태로 유지되어
	// 동일 탭에 OuterHTML 재명령을 발행할 수 있다.
	runCtx, runCancel := context.WithTimeout(browserCtx, c.config.Timeout)
	defer runCancel()

	// 메인 네비게이션의 LoaderID를 추적하여 iframe/서브프레임 응답과 구분
	// - 메인 프레임과 리다이렉트 체인은 동일한 LoaderID를 공유
	// - iframe/서브프레임은 별도의 LoaderID를 가지므로 필터링됨
	// - statusCode 초기값 0: 이벤트를 한 번도 수신하지 못한 경우와 명시적 구분
	// - listener를 browserCtx에 등록하여 timeout 이후 graceful capture 단계에서도
	//   추가로 도착하는 응답 이벤트를 수신할 수 있도록 한다.
	var (
		statusCode       int64
		statusMu         sync.Mutex
		mainLoaderID     cdp.LoaderID
		loaderIDCaptured bool
	)
	chromedp.ListenTarget(browserCtx, func(ev interface{}) {
		switch e := ev.(type) {
		case *network.EventRequestWillBeSent:
			// 첫 번째 Document 요청의 LoaderID를 메인 네비게이션으로 고정
			if e.Type == network.ResourceTypeDocument {
				statusMu.Lock()
				if !loaderIDCaptured {
					mainLoaderID = e.LoaderID
					loaderIDCaptured = true
				}
				statusMu.Unlock()
			}
		case *network.EventResponseReceived:
			// 메인 네비게이션 LoaderID와 일치하는 Document 응답만 채택
			// 리다이렉트 체인은 같은 LoaderID를 공유하므로 최종 상태코드가 올바르게 갱신됨
			if e.Type == network.ResourceTypeDocument {
				statusMu.Lock()
				if loaderIDCaptured && e.LoaderID == mainLoaderID {
					statusCode = int64(e.Response.Status)
				}
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
	partialLoad := false

	// 브라우저 실행
	if runErr := chromedp.Run(runCtx, actions...); runErr != nil {
		// timeout 외 다른 에러는 즉시 실패 처리 (기존 CDP_002 동작 유지)
		if !IsTimeoutError(runErr) {
			return nil, &core.CrawlerError{
				Category:  core.ErrCategoryNetwork,
				Code:      "CDP_002",
				Message:   "failed to render page",
				Source:    c.name,
				URL:       target.URL,
				Retryable: true,
				Err:       runErr,
			}
		}

		// timeout 발생: 별도 context로 부분 DOM 캡처 시도
		log.WithFields(map[string]interface{}{
			"url":        target.URL,
			"timeout_ms": c.config.Timeout.Milliseconds(),
		}).Warn("page render timed out, attempting graceful capture")

		partialHTML, captureErr := captureOuterHTML(browserCtx)
		if captureErr != nil {
			// 부분 캡처 자체가 실패 → 빈 페이지/네트워크 불가와 동등한 완전 실패로 분류
			// runErr(원인 timeout)와 captureErr(캡처 실패 사유 — target closed,
			// context canceled, CDP 오류 등)를 모두 보존하여 디버깅 가능성 유지
			return nil, &core.CrawlerError{
				Category:  core.ErrCategoryTimeout,
				Code:      "CDP_006",
				Message:   "render timeout and graceful capture failed",
				Source:    c.name,
				URL:       target.URL,
				Retryable: true,
				Err:       errors.Join(runErr, captureErr),
			}
		}

		// 부분 로드 DOM 유효성 검증 (최소 body + 길이 충족)
		if !IsValidPartialDOM(partialHTML) {
			// runErr(timeout) + DOM 검증 실패 사유(길이 정보 포함)를 함께 보존하여
			// 에러 체인에서 "어떤 검증이 실패했는지"를 추적할 수 있도록 한다
			validationErr := fmt.Errorf("partial DOM failed validation: length=%d", len(partialHTML))
			return nil, &core.CrawlerError{
				Category:  core.ErrCategoryTimeout,
				Code:      "CDP_007",
				Message:   "render timeout and partial DOM invalid",
				Source:    c.name,
				URL:       target.URL,
				Retryable: true,
				Err:       errors.Join(runErr, validationErr),
			}
		}

		html = partialHTML
		partialLoad = true
		log.WithFields(map[string]interface{}{
			"url":  target.URL,
			"size": len(html),
		}).Info("graceful timeout capture succeeded")
	}

	elapsed := time.Since(start)

	statusMu.Lock()
	capturedStatus := int(statusCode)
	statusMu.Unlock()

	// 네비게이션이 중간에 취소되는 등의 이유로 응답 이벤트를 수신하지 못한 경우
	if capturedStatus == 0 {
		log.WithField("url", target.URL).Warn("no HTTP status code captured from navigation, treating as success")
		capturedStatus = 200
	}

	// HTTP 상태코드 검사
	if capturedStatus == 404 {
		return nil, core.NewNotFoundError(target.URL)
	}
	if capturedStatus == 429 {
		return nil, core.NewRateLimitError("HTTP_429", "rate limited", target.URL, capturedStatus)
	}
	if capturedStatus >= 500 {
		return nil, core.NewHTTPServerError(target.URL, capturedStatus)
	}
	if capturedStatus >= 400 {
		return nil, core.NewHTTPClientError(target.URL, capturedStatus)
	}

	// metadata: 부분 로드인 경우 호출자 원본을 보존하기 위해 복사 후 플래그 추가
	metadata := target.Metadata
	if partialLoad {
		metadata = metadataWithPartialLoad(target.Metadata)
	}

	rawContent := &core.RawContent{
		ID:         fmt.Sprintf("%s-%d", c.name, time.Now().UnixNano()),
		SourceInfo: c.sourceInfo,
		FetchedAt:  time.Now(),
		URL:        target.URL,
		HTML:       html,
		StatusCode: capturedStatus,
		Headers:    make(map[string]string),
		Metadata:   metadata,
	}

	log.WithFields(map[string]interface{}{
		"url":          target.URL,
		"size":         len(html),
		"duration_ms":  elapsed.Milliseconds(),
		"partial_load": partialLoad,
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
