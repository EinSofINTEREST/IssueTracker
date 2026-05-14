package model

import "time"

// FetcherKind 는 fetch 단계에서 사용할 도구 식별자입니다.
type FetcherKind string

const (
	// FetcherGoQuery: 정적 HTML fetch (가벼움, 기본). lazy-load / SPA 가 아닌 일반 페이지에 적합.
	FetcherGoQuery FetcherKind = "goquery"
	// FetcherChromedp: 헤드리스 브라우저 fetch (무거움). SPA / dynamic content 사이트에 적용.
	FetcherChromedp FetcherKind = "chromedp"
)

// IsValid 는 fetcher_rules.fetcher CHECK 제약과 동일한 검증을 application 측에서 수행합니다.
func (k FetcherKind) IsValid() bool {
	return k == FetcherGoQuery || k == FetcherChromedp
}

// FetcherRuleRecord 는 fetcher_rules 테이블의 단일 행입니다.
type FetcherRuleRecord struct {
	ID          int64
	HostPattern string
	Fetcher     FetcherKind
	Reason      string

	SourceName      string
	SourceType      string
	Country         string
	Language        string
	BaseURL         string
	RequestsPerHour int

	CreatedAt time.Time
	UpdatedAt time.Time
}
