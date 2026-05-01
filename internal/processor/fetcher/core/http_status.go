package core

import "net/http"

// CheckHTTPStatus 는 HTTP 응답 상태 코드를 검사하여 4xx/5xx 에러를 적절한
// CrawlerError 로 변환합니다. 정상(2xx/3xx) 인 경우 nil 을 반환합니다.
//
// 분기 정책 (이슈 #75 — fetcher 공통 추출):
//   - 404 (Not Found)            → NewNotFoundError    (retry 불가)
//   - 429 (Too Many Requests)    → NewRateLimitError   (retry 가능, code "HTTP_429")
//   - 5xx (Server Error)         → NewHTTPServerError  (retry 가능)
//   - 4xx 기타 (Client Error)    → NewHTTPClientError  (retry 불가)
//   - 그 외 (성공/리다이렉트 등) → nil
//
// 본 함수는 goquery/chromedp/기타 fetcher 들이 동일한 정책으로 분기하도록
// 단일 진실 소스(SoT) 역할을 합니다. 새 분기(예: 451 법적 차단) 추가 시
// 본 함수만 수정하면 모든 fetcher 에 일관 반영됩니다.
//
// chromedp 처럼 0 (응답 미수신) 같은 fetcher 고유 sentinel 값은 호출자가
// 사전 처리한 뒤 정상 코드로 보정해서 본 함수를 호출해야 합니다.
func CheckHTTPStatus(url string, statusCode int) error {
	switch {
	case statusCode == http.StatusNotFound:
		return NewNotFoundError(url)
	case statusCode == http.StatusTooManyRequests:
		return NewRateLimitError("HTTP_429", "rate limited", url, statusCode)
	case statusCode >= 500:
		return NewHTTPServerError(url, statusCode)
	case statusCode >= 400:
		return NewHTTPClientError(url, statusCode)
	default:
		return nil
	}
}
