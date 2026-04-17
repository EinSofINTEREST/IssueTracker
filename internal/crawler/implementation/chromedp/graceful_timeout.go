package chromedp

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/chromedp/chromedp"
)

// MetadataKeyPartialLoad: RawContent.Metadata에 부분 로드 여부를 표시하는 키
// timeout 발생으로 graceful capture가 적용된 경우 true가 설정되어,
// 다운스트림 파이프라인에서 데이터의 완전성 수준을 판단할 수 있다.
const MetadataKeyPartialLoad = "partial_load"

// gracefulCaptureTimeout: timeout 발생 시 부분 DOM 캡처에 허용하는 최대 시간
// 너무 길면 전체 응답 시간이 늘어나고, 너무 짧으면 캡처 실패 빈도가 증가하므로
// 실험적으로 3초가 적정 수준임을 가정한다 (chromedp OuterHTML 평균 < 1초).
const gracefulCaptureTimeout = 3 * time.Second

// minPartialDOMLength: 부분 로드 DOM으로 인정할 최소 HTML 길이 (바이트)
// 빈 페이지(`<html><head></head><body></body></html>`)는 약 45바이트이므로
// 이를 충분히 상회하는 값으로 설정하여 의미 있는 컨텐츠 존재를 보장한다.
const minPartialDOMLength = 512

// IsTimeoutError: 에러가 chromedp 작업의 timeout(deadline exceeded)인지 판별
// chromedp.Run이 timeout으로 종료되면 context.DeadlineExceeded를 wrap하여 반환한다.
// 사용자 취소(context.Canceled)는 별개로 취급하여 graceful capture를 시도하지 않는다.
func IsTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, context.DeadlineExceeded)
}

// IsValidPartialDOM: 부분 로드된 HTML이 다운스트림 처리에 사용 가능한지 검증
// 최소 조건:
//  1. minPartialDOMLength 이상의 길이 (빈 페이지/스켈레톤 페이지 배제)
//  2. <body> 태그 존재 (DOM 구조의 최소 완전성 보장)
func IsValidPartialDOM(html string) bool {
	if len(html) < minPartialDOMLength {
		return false
	}
	return strings.Contains(strings.ToLower(html), "<body")
}

// captureOuterHTML: timeout 이후 별도 context로 OuterHTML 추출 시도
// browserCtx는 timeout으로 cancel되지 않은 tab context여야 하며,
// chromedp가 동일 탭에 새 명령을 발행할 수 있어야 한다.
func captureOuterHTML(browserCtx context.Context) (string, error) {
	rescueCtx, cancel := context.WithTimeout(browserCtx, gracefulCaptureTimeout)
	defer cancel()

	var html string
	if err := chromedp.Run(rescueCtx, chromedp.OuterHTML("html", &html)); err != nil {
		return "", err
	}
	return html, nil
}

// metadataWithPartialLoad: 원본 metadata를 복사하고 partial_load 플래그 설정
// 호출자의 원본 map을 직접 변형하지 않아 부수효과를 방지한다.
// src가 nil이어도 안전하게 새 map을 생성한다.
func metadataWithPartialLoad(src map[string]interface{}) map[string]interface{} {
	dst := make(map[string]interface{}, len(src)+1)
	for k, v := range src {
		dst[k] = v
	}
	dst[MetadataKeyPartialLoad] = true
	return dst
}
