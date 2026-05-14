package model

import "time"

// RawContentFilter는 RawContent 조회 조건을 나타냅니다.
// 제로값 필드는 WHERE 조건에서 제외됩니다.
type RawContentFilter struct {
	Country    string
	SourceName string

	FetchedAfter  *time.Time
	FetchedBefore *time.Time
	StatusCode    int // 0이면 무시

	Pagination Pagination
}
