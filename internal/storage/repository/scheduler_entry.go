package repository

import (
	"context"

	"issuetracker/internal/storage/model"
)

// SchedulerEntryRepository 는 scheduler_entries 테이블에 대한 데이터 접근 인터페이스입니다.
//
// 모든 구현체는 goroutine-safe 해야 합니다 — Resolver 가 핫패스에서 ListEnabled 를 호출.
type SchedulerEntryRepository interface {
	// ListEnabled 는 enabled=TRUE 인 모든 entry 를 반환합니다 (운영 hot-path).
	// category 가 빈 문자열이면 전체 반환.
	ListEnabled(ctx context.Context, category model.SchedulerCategory) ([]*model.SchedulerEntryRecord, error)

	// Insert 는 새 entry 를 INSERT 합니다. (category, source_name, url) UNIQUE 충돌 시 ErrDuplicate.
	Insert(ctx context.Context, rec *model.SchedulerEntryRecord) error

	// Update 는 ID 기준으로 row 를 갱신합니다 (자연키는 변경 불가). 미존재 시 ErrNotFound.
	Update(ctx context.Context, rec *model.SchedulerEntryRecord) error

	// Delete 는 ID 로 row 를 제거합니다 (idempotent).
	Delete(ctx context.Context, id int64) error
}
