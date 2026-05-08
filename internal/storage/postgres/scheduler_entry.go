package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"issuetracker/internal/storage"
	"issuetracker/pkg/logger"
)

// pgSchedulerEntryRepository 는 pgx/v5 기반 SchedulerEntryRepository 구현체입니다.
type pgSchedulerEntryRepository struct {
	pool *pgxpool.Pool
}

// NewSchedulerEntryRepository 는 pgxpool 을 사용하는 SchedulerEntryRepository 를 생성합니다.
//
// log 인자는 다른 Repository 들의 시그니처 일관성 유지용 — 현재 구현은 미사용.
func NewSchedulerEntryRepository(pool *pgxpool.Pool, log *logger.Logger) (storage.SchedulerEntryRepository, error) {
	if pool == nil {
		return nil, errors.New("postgres: NewSchedulerEntryRepository requires non-nil pool")
	}
	_ = log
	return &pgSchedulerEntryRepository{pool: pool}, nil
}

const sqlListEnabledAll = `
SELECT id, category, source_name, url, target_type, interval_seconds, priority,
       enabled, metadata, notes, created_at, updated_at
FROM scheduler_entries
WHERE enabled = TRUE
ORDER BY id
`

const sqlListEnabledByCategory = `
SELECT id, category, source_name, url, target_type, interval_seconds, priority,
       enabled, metadata, notes, created_at, updated_at
FROM scheduler_entries
WHERE enabled = TRUE AND category = $1
ORDER BY id
`

// ListEnabled 는 enabled=TRUE row 를 반환합니다. category 가 빈 문자열이면 전체.
func (r *pgSchedulerEntryRepository) ListEnabled(ctx context.Context, category storage.SchedulerCategory) ([]*storage.SchedulerEntryRecord, error) {
	var (
		rows pgx.Rows
		err  error
	)
	if category == "" {
		rows, err = r.pool.Query(ctx, sqlListEnabledAll)
	} else {
		rows, err = r.pool.Query(ctx, sqlListEnabledByCategory, string(category))
	}
	if err != nil {
		return nil, fmt.Errorf("query scheduler_entries: %w", err)
	}
	defer rows.Close()

	var out []*storage.SchedulerEntryRecord
	for rows.Next() {
		rec, err := scanSchedulerEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate scheduler_entries: %w", err)
	}
	return out, nil
}

const sqlInsertSchedulerEntry = `
INSERT INTO scheduler_entries (
  category, source_name, url, target_type, interval_seconds, priority, enabled, metadata, notes
) VALUES (
  $1, $2, $3, $4, $5, $6, $7, $8, $9
)
RETURNING id, created_at, updated_at
`

// Insert 는 새 entry 를 INSERT 합니다. (category, source_name, url) UNIQUE 충돌 시 ErrDuplicate.
func (r *pgSchedulerEntryRepository) Insert(ctx context.Context, rec *storage.SchedulerEntryRecord) error {
	if rec.Interval <= 0 {
		return fmt.Errorf("%w: interval must be positive", storage.ErrInvalid)
	}
	intervalSec := int(rec.Interval / time.Second)
	metadata := rec.Metadata
	if len(metadata) == 0 {
		metadata = []byte("{}")
	}
	row := r.pool.QueryRow(ctx, sqlInsertSchedulerEntry,
		string(rec.Category), rec.SourceName, rec.URL, rec.TargetType,
		intervalSec, rec.Priority, rec.Enabled, metadata, rec.Notes,
	)
	if err := row.Scan(&rec.ID, &rec.CreatedAt, &rec.UpdatedAt); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgerrcode.UniqueViolation {
			return storage.ErrDuplicate
		}
		return fmt.Errorf("insert scheduler_entry: %w", err)
	}
	return nil
}

const sqlUpdateSchedulerEntry = `
UPDATE scheduler_entries
SET target_type = $2, interval_seconds = $3, priority = $4, enabled = $5,
    metadata = $6, notes = $7, updated_at = NOW()
WHERE id = $1
RETURNING updated_at
`

// Update 는 ID 기준으로 row 를 갱신합니다 (자연키 변경 불가).
func (r *pgSchedulerEntryRepository) Update(ctx context.Context, rec *storage.SchedulerEntryRecord) error {
	if rec.Interval <= 0 {
		return fmt.Errorf("%w: interval must be positive", storage.ErrInvalid)
	}
	intervalSec := int(rec.Interval / time.Second)
	metadata := rec.Metadata
	if len(metadata) == 0 {
		metadata = []byte("{}")
	}
	row := r.pool.QueryRow(ctx, sqlUpdateSchedulerEntry,
		rec.ID, rec.TargetType, intervalSec, rec.Priority, rec.Enabled, metadata, rec.Notes,
	)
	if err := row.Scan(&rec.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return storage.ErrNotFound
		}
		return fmt.Errorf("update scheduler_entry %d: %w", rec.ID, err)
	}
	return nil
}

const sqlDeleteSchedulerEntry = `DELETE FROM scheduler_entries WHERE id = $1`

// Delete 는 ID 로 row 를 제거합니다 (idempotent).
func (r *pgSchedulerEntryRepository) Delete(ctx context.Context, id int64) error {
	if _, err := r.pool.Exec(ctx, sqlDeleteSchedulerEntry, id); err != nil {
		return fmt.Errorf("delete scheduler_entry %d: %w", id, err)
	}
	return nil
}

// scanSchedulerEntry 는 Row/Rows 에서 SchedulerEntryRecord 를 스캔합니다.
func scanSchedulerEntry(s scanner) (*storage.SchedulerEntryRecord, error) {
	rec := &storage.SchedulerEntryRecord{}
	var category string
	var intervalSec int
	if err := s.Scan(
		&rec.ID, &category, &rec.SourceName, &rec.URL, &rec.TargetType,
		&intervalSec, &rec.Priority, &rec.Enabled, &rec.Metadata, &rec.Notes,
		&rec.CreatedAt, &rec.UpdatedAt,
	); err != nil {
		return nil, err
	}
	rec.Category = storage.SchedulerCategory(category)
	rec.Interval = time.Duration(intervalSec) * time.Second
	return rec, nil
}
