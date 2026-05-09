package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"issuetracker/internal/storage"
	"issuetracker/pkg/logger"
)

// pgSearchKeywordRepository 는 pgx/v5 기반 SearchKeywordRepository 구현체입니다.
type pgSearchKeywordRepository struct {
	pool *pgxpool.Pool
}

// NewSearchKeywordRepository 는 pgxpool 을 사용하는 SearchKeywordRepository 를 생성합니다.
//
// log 는 다른 Repository 들과 시그니처 일관성 유지용 — 현재 미사용.
func NewSearchKeywordRepository(pool *pgxpool.Pool, log *logger.Logger) (storage.SearchKeywordRepository, error) {
	if pool == nil {
		return nil, errors.New("postgres: NewSearchKeywordRepository requires non-nil pool")
	}
	_ = log
	return &pgSearchKeywordRepository{pool: pool}, nil
}

// ListEnabled 의 SQL 은 language / region 빈 문자열 매치를 OR 로 cover.
// 빈 문자열 = 전체 매치, 비어있지 않으면 정확 매치 (language=” 자체도 정확 매치 대상).
const sqlListEnabledSearchKeywords = `
SELECT id, keyword, enabled, source, language, region, notes,
       last_searched_at, created_at, updated_at
FROM search_keywords
WHERE enabled = TRUE
  AND ($1 = '' OR language = $1)
  AND ($2 = '' OR region = $2)
ORDER BY id
`

// ListEnabled 는 enabled=TRUE keyword 를 반환합니다.
func (r *pgSearchKeywordRepository) ListEnabled(ctx context.Context, language, region string) ([]*storage.SearchKeywordRecord, error) {
	rows, err := r.pool.Query(ctx, sqlListEnabledSearchKeywords, language, region)
	if err != nil {
		return nil, fmt.Errorf("query search_keywords: %w", err)
	}
	defer rows.Close()

	var out []*storage.SearchKeywordRecord
	for rows.Next() {
		rec, err := scanSearchKeyword(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate search_keywords: %w", err)
	}
	return out, nil
}

const sqlInsertSearchKeyword = `
INSERT INTO search_keywords (keyword, enabled, source, language, region, notes)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id, created_at, updated_at
`

// Insert 는 새 keyword 를 INSERT 합니다. UNIQUE 충돌 시 ErrDuplicate.
func (r *pgSearchKeywordRepository) Insert(ctx context.Context, rec *storage.SearchKeywordRecord) error {
	if rec.Keyword == "" {
		return fmt.Errorf("%w: keyword must be non-empty", storage.ErrInvalid)
	}
	if rec.Source == "" {
		rec.Source = storage.SearchKeywordSourceManual
	}
	row := r.pool.QueryRow(ctx, sqlInsertSearchKeyword,
		rec.Keyword, rec.Enabled, string(rec.Source), rec.Language, rec.Region, rec.Notes,
	)
	if err := row.Scan(&rec.ID, &rec.CreatedAt, &rec.UpdatedAt); err != nil {
		if isPgUniqueViolation(err) {
			return storage.ErrDuplicate
		}
		return fmt.Errorf("insert search_keyword: %w", err)
	}
	return nil
}

const sqlUpdateSearchKeyword = `
UPDATE search_keywords
SET enabled = $2, source = $3, language = $4, region = $5, notes = $6, updated_at = NOW()
WHERE id = $1
RETURNING updated_at
`

// Update 는 ID 기준으로 row 를 갱신합니다 (keyword 자체는 변경 불가).
func (r *pgSearchKeywordRepository) Update(ctx context.Context, rec *storage.SearchKeywordRecord) error {
	if rec.Source == "" {
		rec.Source = storage.SearchKeywordSourceManual
	}
	row := r.pool.QueryRow(ctx, sqlUpdateSearchKeyword,
		rec.ID, rec.Enabled, string(rec.Source), rec.Language, rec.Region, rec.Notes,
	)
	if err := row.Scan(&rec.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return storage.ErrNotFound
		}
		return fmt.Errorf("update search_keyword %d: %w", rec.ID, err)
	}
	return nil
}

const sqlDeleteSearchKeyword = `DELETE FROM search_keywords WHERE id = $1`

// Delete 는 ID 로 row 를 제거합니다 (idempotent).
func (r *pgSearchKeywordRepository) Delete(ctx context.Context, id int64) error {
	if _, err := r.pool.Exec(ctx, sqlDeleteSearchKeyword, id); err != nil {
		return fmt.Errorf("delete search_keyword %d: %w", id, err)
	}
	return nil
}

const sqlMarkSearched = `
UPDATE search_keywords
SET last_searched_at = $2, updated_at = NOW()
WHERE id = $1
`

// MarkSearched 는 last_searched_at 을 갱신합니다. 미존재 시 ErrNotFound.
func (r *pgSearchKeywordRepository) MarkSearched(ctx context.Context, id int64, t time.Time) error {
	tag, err := r.pool.Exec(ctx, sqlMarkSearched, id, t)
	if err != nil {
		return fmt.Errorf("mark searched %d: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return storage.ErrNotFound
	}
	return nil
}

// scanSearchKeyword 는 Row/Rows 에서 SearchKeywordRecord 를 스캔합니다.
func scanSearchKeyword(s scanner) (*storage.SearchKeywordRecord, error) {
	rec := &storage.SearchKeywordRecord{}
	var source string
	if err := s.Scan(
		&rec.ID, &rec.Keyword, &rec.Enabled, &source, &rec.Language, &rec.Region,
		&rec.Notes, &rec.LastSearchedAt, &rec.CreatedAt, &rec.UpdatedAt,
	); err != nil {
		return nil, err
	}
	rec.Source = storage.SearchKeywordSource(source)
	return rec, nil
}
