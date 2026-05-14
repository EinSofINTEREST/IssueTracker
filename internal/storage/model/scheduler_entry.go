package model

import "time"

// SchedulerCategory 는 scheduler_entries.category 컬럼의 enum 입니다.
type SchedulerCategory string

const (
	SchedulerCategoryNews      SchedulerCategory = "news"
	SchedulerCategoryCommunity SchedulerCategory = "community"
	SchedulerCategorySearch    SchedulerCategory = "search"
)

// SchedulerEntryRecord 는 scheduler_entries 테이블의 단일 행입니다.
//
// 크롤러 진입 URL 의 source-of-truth (이슈 #328). 기존 hardcoded sourceCategoryURLs map 대체.
type SchedulerEntryRecord struct {
	ID         int64
	Category   SchedulerCategory
	SourceName string
	URL        string
	TargetType string // 'category' | 'search_results' | 향후 확장
	Interval   time.Duration
	Priority   int // 1=high / 2=normal / 3=low
	Enabled    bool
	Metadata   []byte // JSONB raw — 카테고리별 확장 슬롯 (search 의 cse_id 등)
	Notes      string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}
