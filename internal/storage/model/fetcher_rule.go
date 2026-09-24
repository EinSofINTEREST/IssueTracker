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

	// PriorityConfig 는 host 단위 priority override + weight 입니다 (이슈 #383).
	// nil 이면 override 없음 — chain 이 기존 경로 (RuleBased → Scoring) 로 흐릅니다.
	PriorityConfig *PriorityConfig

	// PriorityConfigError 는 priority_config 파싱이 실패한 경우의 사유입니다.
	//
	// 목록 조회에서 한 host 의 잘못된 설정이 전체를 실패시키면 운영 도구가 통째로 멈추므로
	// 그 host 만 override 없이 둔다. 그런데 **nil 인 이유가 "설정 없음" 인지 "설정 오류" 인지
	// 구별되지 않으면 조용히 무시되는 설정** 이 된다 — 운영자는 적용된 줄 알지만 동작은
	// 그대로다. 사유를 함께 실어 로드하는 쪽이 경고할 수 있게 한다.
	PriorityConfigError string

	CreatedAt time.Time
	UpdatedAt time.Time
}
