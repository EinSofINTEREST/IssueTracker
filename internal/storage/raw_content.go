package storage

import (
	"context"
	"time"

	"issuetracker/internal/crawler/core"
)

// RawContentRepository는 크롤링된 원본 데이터 CRUD 연산을 위한 인터페이스입니다.
// 모든 구현체는 goroutine-safe해야 합니다.
// pgx/v5 구현체: internal/storage/postgres/raw_content.go
type RawContentRepository interface {
	// Save는 RawContent를 저장합니다.
	// 동일 URL이 이미 존재하면 ErrDuplicate를 반환합니다.
	Save(ctx context.Context, raw *core.RawContent) error

	// GetByID는 ID로 RawContent를 조회합니다.
	// 존재하지 않으면 ErrNotFound를 반환합니다.
	GetByID(ctx context.Context, id string) (*core.RawContent, error)

	// GetByURL은 URL로 RawContent를 조회합니다.
	// 존재하지 않으면 ErrNotFound를 반환합니다.
	GetByURL(ctx context.Context, url string) (*core.RawContent, error)

	// List는 필터 조건에 맞는 RawContent 목록을 fetched_at DESC 순으로 반환합니다.
	List(ctx context.Context, filter RawContentFilter) ([]*core.RawContent, error)

	// Delete는 ID로 RawContent를 삭제합니다. 존재하지 않아도 에러를 반환하지 않습니다.
	Delete(ctx context.Context, id string) error

	// DeleteBefore는 cutoff 이전에 수집된 원본 데이터를 일괄 삭제합니다.
	// 원본 데이터 보존 정책(기본 90일) 적용에 사용됩니다.
	// 삭제된 레코드 수를 반환합니다.
	DeleteBefore(ctx context.Context, before time.Time) (int64, error)
}
