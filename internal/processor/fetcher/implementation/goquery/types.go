package goquery

import (
	"net/http"

	"issuetracker/internal/processor/fetcher/core"
)

// GoqueryCrawler: goquery 라이브러리 기반 크롤러
// goquery를 사용하여 HTML 파싱과 크롤링을 동시에 처리
//
// urlRateLimiter 가 nil 이 아니면 매 fetch 직전 Wait 를 호출해 사이트별 RPH 정책을 강제합니다.
// nil 이면 기존 동작과 동일 (제한 없음) — 호출자 (sources/registry) 가 fetcher_rules 의
// requests_per_hour 가 0 이거나 wiring 단계에서 limiter 없는 환경에 대해 nil 을 주입.
type GoqueryCrawler struct {
	name           string
	sourceInfo     core.SourceInfo
	config         core.Config
	httpClient     *http.Client
	urlRateLimiter core.URLRateLimiter
}
