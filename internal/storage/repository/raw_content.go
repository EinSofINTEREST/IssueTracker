package repository

import (
	"context"
	"time"

	"issuetracker/internal/processor/fetcher/core"
	"issuetracker/internal/storage/model"
)

// RawContentRepository는 크롤링된 원본 데이터 CRUD 연산을 위한 인터페이스입니다.
// 모든 구현체는 goroutine-safe해야 합니다.
// pgx/v5 구현체: internal/storage/postgres/raw_content.go
type RawContentRepository interface {
	// Save는 RawContent를 저장합니다. 동일 URL이 이미 존재하면 ErrDuplicate.
	Save(ctx context.Context, raw *core.RawContent) error

	// GetByID는 ID로 RawContent를 조회합니다. 존재하지 않으면 ErrNotFound.
	GetByID(ctx context.Context, id string) (*core.RawContent, error)

	// GetByURL은 URL로 RawContent를 조회합니다. 존재하지 않으면 ErrNotFound.
	GetByURL(ctx context.Context, url string) (*core.RawContent, error)

	// List는 필터 조건에 맞는 RawContent 목록을 fetched_at DESC 순으로 반환합니다.
	List(ctx context.Context, filter model.RawContentFilter) ([]*core.RawContent, error)

	// Delete는 ID로 RawContent를 삭제합니다 (idempotent).
	Delete(ctx context.Context, id string) error

	// DeleteBefore는 cutoff 이전에 수집된 원본 데이터를 일괄 삭제합니다.
	DeleteBefore(ctx context.Context, before time.Time) (int64, error)
}
