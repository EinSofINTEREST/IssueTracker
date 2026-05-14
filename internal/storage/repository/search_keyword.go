package repository

import (
	"context"
	"time"

	"issuetracker/internal/storage/model"
)

// SearchKeywordRepository 는 search_keywords 테이블에 대한 데이터 접근 인터페이스입니다.
//
// 모든 구현체는 goroutine-safe 해야 합니다 — search handler 가 핫패스에서 ListEnabled 를 호출.
type SearchKeywordRepository interface {
	// ListEnabled 는 enabled=TRUE keyword 를 반환합니다.
	ListEnabled(ctx context.Context, language, region string) ([]*model.SearchKeywordRecord, error)

	// Insert 는 새 keyword 를 INSERT 합니다. UNIQUE keyword 충돌 시 ErrDuplicate.
	Insert(ctx context.Context, rec *model.SearchKeywordRecord) error

	// Update 는 ID 기준으로 row 를 갱신합니다. 미존재 시 ErrNotFound.
	Update(ctx context.Context, rec *model.SearchKeywordRecord) error

	// Delete 는 ID 로 row 를 제거합니다 (idempotent).
	Delete(ctx context.Context, id int64) error

	// MarkSearched 는 last_searched_at 을 t 로 갱신합니다. 미존재 시 ErrNotFound.
	MarkSearched(ctx context.Context, id int64, t time.Time) error
}
