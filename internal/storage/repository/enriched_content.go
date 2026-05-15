package repository

import (
	"context"

	"issuetracker/internal/storage/model"
)

// EnrichedContentRepository 는 enriched_contents 테이블 CRUD 인터페이스입니다 (이슈 #450).
//
// 모든 구현체는 goroutine-safe 해야 합니다.
// pgx/v5 구현체: internal/storage/postgres/enriched_content.go
type EnrichedContentRepository interface {
	// Upsert 는 enriched_contents row 를 저장합니다. content_id 충돌 시 UPDATE (재처리 안전).
	// rec.ID / rec.EnrichedAt 는 호출 후 DB 값으로 갱신됩니다.
	Upsert(ctx context.Context, rec *model.EnrichedContentRecord) error

	// GetByContentID 는 content_id 로 row 를 조회합니다. 미존재 시 storage.ErrNotFound.
	GetByContentID(ctx context.Context, contentID string) (*model.EnrichedContentRecord, error)
}
