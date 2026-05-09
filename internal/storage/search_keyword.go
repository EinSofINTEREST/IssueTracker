package storage

import (
	"context"
	"time"
)

// SearchKeywordSource 는 search_keywords.source 컬럼의 enum.
//
// 'manual' — 운영자 직접 등록.
// 'auto'   — entity / 트렌드 자동 추출 인입 (#331 후속 이슈에서 implement).
type SearchKeywordSource string

const (
	SearchKeywordSourceManual SearchKeywordSource = "manual"
	SearchKeywordSourceAuto   SearchKeywordSource = "auto"
)

// SearchKeywordRecord 는 search_keywords 테이블의 단일 행입니다.
//
// Google CSE 기반 search exploration (이슈 #331) 의 keyword set source-of-truth.
type SearchKeywordRecord struct {
	ID             int64
	Keyword        string
	Enabled        bool
	Source         SearchKeywordSource
	Language       string // '' | 'ko' | 'en' (Google CSE lr 파라미터)
	Region         string // '' | 'kr' | 'us' (Google CSE gl 파라미터)
	Notes          string
	LastSearchedAt *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// SearchKeywordRepository 는 search_keywords 테이블에 대한 데이터 접근 인터페이스입니다.
//
// 모든 구현체는 goroutine-safe 해야 합니다 — search handler 가 핫패스에서 ListEnabled 를 호출.
type SearchKeywordRepository interface {
	// ListEnabled 는 enabled=TRUE keyword 를 반환합니다.
	//
	// language / region 이 빈 문자열이면 그 컬럼은 전체 매치, 비어있지 않으면 정확 매치.
	// 매칭 없으면 빈 슬라이스 + nil error.
	ListEnabled(ctx context.Context, language, region string) ([]*SearchKeywordRecord, error)

	// Insert 는 새 keyword 를 INSERT 합니다. UNIQUE keyword 충돌 시 ErrDuplicate.
	Insert(ctx context.Context, rec *SearchKeywordRecord) error

	// Update 는 ID 기준으로 row 를 갱신합니다 (keyword 자체는 자연키로 변경 불가).
	// 미존재 시 ErrNotFound.
	Update(ctx context.Context, rec *SearchKeywordRecord) error

	// Delete 는 ID 로 row 를 제거합니다 (idempotent).
	Delete(ctx context.Context, id int64) error

	// MarkSearched 는 last_searched_at 을 t 로 갱신합니다. 미존재 시 ErrNotFound.
	MarkSearched(ctx context.Context, id int64, t time.Time) error
}
