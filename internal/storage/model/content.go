package model

import "time"

// ContentFilter는 Content 조회 조건을 나타냅니다.
// 제로값 필드는 WHERE 조건에서 제외됩니다.
type ContentFilter struct {
	Country    string // ISO 3166-1 alpha-2 (예: "US", "KR")
	Language   string // ISO 639-1 (예: "en", "ko")
	Category   string
	Source     string // source_id 기반 필터
	SourceType string // "news" | "community" | "social" (빈 문자열이면 무시)

	PublishedAfter  *time.Time
	PublishedBefore *time.Time

	Tags           []string // 해당 태그를 모두 포함하는 기사 (AND 조건)
	MinReliability *float32 // 최소 신뢰도 (nil이면 무시)

	Pagination Pagination
}
