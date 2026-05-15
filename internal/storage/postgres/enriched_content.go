package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"issuetracker/internal/storage"
	"issuetracker/internal/storage/model"
	"issuetracker/internal/storage/repository"
	"issuetracker/pkg/logger"
)

// pgEnrichedContentRepository 는 pgx/v5 기반 EnrichedContentRepository 구현체입니다 (이슈 #450).
type pgEnrichedContentRepository struct {
	pool *pgxpool.Pool
}

// NewEnrichedContentRepository 는 pgxpool 기반 EnrichedContentRepository 를 생성합니다.
// log 인자는 시그니처 일관성용 — 현재 미사용.
func NewEnrichedContentRepository(pool *pgxpool.Pool, log *logger.Logger) repository.EnrichedContentRepository {
	_ = log
	return &pgEnrichedContentRepository{pool: pool}
}

// Upsert SQL — content_id UNIQUE 충돌 시 ON CONFLICT 로 UPDATE (재처리 안전).
//
// enriched_at 은 항상 NOW() 로 refresh — 마지막 enrich 시점 추적.
// trust_score / facts / verifications / context 는 caller 가 매번 전달한 값으로 덮어쓰기.
const sqlUpsertEnrichedContent = `
INSERT INTO enriched_contents (content_id, trust_score, facts, verifications, context)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (content_id) DO UPDATE
SET trust_score   = EXCLUDED.trust_score,
    facts         = EXCLUDED.facts,
    verifications = EXCLUDED.verifications,
    context       = EXCLUDED.context,
    enriched_at   = NOW()
RETURNING id, enriched_at
`

func (r *pgEnrichedContentRepository) Upsert(ctx context.Context, rec *model.EnrichedContentRecord) error {
	if rec == nil {
		return errors.New("enriched_contents upsert: rec must not be nil")
	}
	facts := nonNilJSONB(rec.Facts, "{}")
	verifications := nonNilJSONB(rec.Verifications, "[]")
	contextJSON := nonNilJSONB(rec.Context, "{}")

	row := r.pool.QueryRow(ctx, sqlUpsertEnrichedContent,
		rec.ContentID, rec.TrustScore, facts, verifications, contextJSON,
	)
	if err := row.Scan(&rec.ID, &rec.EnrichedAt); err != nil {
		return fmt.Errorf("upsert enriched_content: %w", err)
	}
	return nil
}

const sqlGetEnrichedContentByContentID = `
SELECT id, content_id, trust_score, facts, verifications, context, enriched_at
FROM enriched_contents
WHERE content_id = $1
`

func (r *pgEnrichedContentRepository) GetByContentID(ctx context.Context, contentID string) (*model.EnrichedContentRecord, error) {
	var rec model.EnrichedContentRecord
	row := r.pool.QueryRow(ctx, sqlGetEnrichedContentByContentID, contentID)
	if err := row.Scan(
		&rec.ID, &rec.ContentID, &rec.TrustScore,
		&rec.Facts, &rec.Verifications, &rec.Context,
		&rec.EnrichedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, storage.ErrNotFound
		}
		return nil, fmt.Errorf("get enriched_content by content_id %q: %w", contentID, err)
	}
	return &rec, nil
}

// nonNilJSONB 는 JSONB 필드의 nil 입력을 fallback 으로 대체합니다.
// 호출자가 JSON marshal 실패 등으로 nil 을 전달해도 DB schema (NOT NULL) 위반 회피.
func nonNilJSONB(v []byte, fallback string) []byte {
	if len(v) == 0 {
		return []byte(fallback)
	}
	return v
}
