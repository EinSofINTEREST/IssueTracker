package chromedp

import (
	"context"
	"sync"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"

	"issuetracker/internal/processor/fetcher/core"
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

		// 이슈 #146: timeout = 정상 종료 시그널로 취급. 캡처 실패/검증 실패해도
		// 에러로 끌어올리지 않고 partial_load=true 의 (가능하면 빈 HTML) 응답으로
		// downgrade. 호출자(parser)는 빈 HTML 을 받으면 자연스럽게 링크 0건 처리하므로
		// 시스템이 안정적으로 흘러간다. Navigate 자체가 한 번도 응답을 못 받은 케이스
		// (statusCode == 0 이면서 capturedHTML 도 빈 상태) 만 진짜 실패로 분류한다.
		partialHTML, captureErr := captureOuterHTML(browserCtx, c.opts.GracefulCaptureTimeout)
		if captureErr != nil {
			log.WithFields(map[string]interface{}{
				"url":        target.URL,
				"timeout_ms": c.config.Timeout.Milliseconds(),
			}).WithError(captureErr).Warn("graceful capture failed, proceeding with empty partial body")
			partialHTML = ""
		} else if !IsValidPartialDOM(partialHTML) {
			log.WithFields(map[string]interface{}{
				"url":    target.URL,
				"length": len(partialHTML),
			}).Warn("partial DOM did not meet validation threshold, retaining anyway")
		}

		// Navigate 자체 실패 가드 (CodeRabbit 피드백): main-document 응답이 한 번도
		// 도착하지 않았고 (statusCode == 0) 캡처도 빈 결과면 페이지가 사실상 미응답.
		// 이슈 #146 acceptance criteria "Navigate 자체 실패 (네트워크 오류) 는 기존대로
		// CDP_002 에러" 를 충족하기 위해 명시적으로 CDP_002 에러로 분류한다.
		statusMu.Lock()
		preCheckStatus := statusCode
		statusMu.Unlock()
		if preCheckStatus == 0 && partialHTML == "" {
			return nil, &core.CrawlerError{
				Category:  core.ErrCategoryNetwork,
				Code:      "CDP_002",
				Message:   "failed to render page: navigation produced no main-document response",
				Source:    c.name,
				URL:       target.URL,
				Retryable: true,
				Err:       runErr,
			}
		}

		html = partialHTML
		partialLoad = true
		log.WithFields(map[string]interface{}{
			"url":  target.URL,
			"size": len(html),
		}).Info("graceful timeout capture completed")
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

	// HTTP 상태코드 검사 (이슈 #75: core 공통 분기)
	if err := core.CheckHTTPStatus(target.URL, capturedStatus); err != nil {
		return nil, err
	}

	// RawContent 조립 (이슈 #75: core 공통 생성자)
	// chromedp 는 raw HTTP header 에 접근하지 않으므로 nil 전달 → 빈 map 으로 보정됨.
	rawContent := core.NewRawContent(c.name, c.sourceInfo, target, html, capturedStatus, nil)

	// 부분 로드인 경우 metadata 를 변형 적용 (호출자 원본 보존을 위한 복사 + 플래그)
	// NewRawContent 의 단순 대입 (target.Metadata 그대로) 이후 덮어쓰기.
	if partialLoad {
		rawContent.Metadata = metadataWithPartialLoad(target.Metadata)
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
