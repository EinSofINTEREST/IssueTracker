package storage

import (
	"context"
	"time"
)

// SchedulerCategory 는 scheduler_entries.category 컬럼의 enum 입니다.
//
// DB 레벨 CHECK 제약과 동일 set 을 application 측에서 정의 — 새 카테고리 추가 시 본 상수
// 추가 + migration 의 CHECK 제약 갱신 (또는 application 검증으로 cover, 본 패키지는 후자
// 채택 — DB ENUM 회피하여 확장 친화).
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

// SchedulerEntryRepository 는 scheduler_entries 테이블에 대한 데이터 접근 인터페이스입니다.
//
// 모든 구현체는 goroutine-safe 해야 합니다 — Resolver 가 핫패스에서 ListEnabled 를 호출.
type SchedulerEntryRepository interface {
	// ListEnabled 는 enabled=TRUE 인 모든 entry 를 반환합니다 (운영 hot-path).
	//
	// category 가 빈 문자열이면 전체 반환, 비어있지 않으면 그 카테고리만.
	// 매칭 없으면 빈 슬라이스 + nil error.
	ListEnabled(ctx context.Context, category SchedulerCategory) ([]*SchedulerEntryRecord, error)

	// Insert 는 새 entry 를 INSERT 합니다. (category, source_name, url) UNIQUE 충돌 시
	// ErrDuplicate 반환.
	Insert(ctx context.Context, rec *SchedulerEntryRecord) error

	// Update 는 ID 기준으로 row 를 갱신합니다 (자연키는 변경 불가). 미존재 시 ErrNotFound.
	Update(ctx context.Context, rec *SchedulerEntryRecord) error

	// Delete 는 ID 로 row 를 제거합니다. 존재하지 않아도 nil (idempotent).
	Delete(ctx context.Context, id int64) error
}
