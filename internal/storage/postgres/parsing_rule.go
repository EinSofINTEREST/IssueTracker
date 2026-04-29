package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"issuetracker/internal/storage"
	"issuetracker/pkg/logger"
)

// pgParsingRuleRepository 는 pgx/v5 기반 ParsingRuleRepository 구현체입니다 (이슈 #100).
type pgParsingRuleRepository struct {
	pool *pgxpool.Pool
}

// NewParsingRuleRepository 는 pgxpool 을 사용하는 ParsingRuleRepository 를 생성합니다.
//
// log 인자는 향후 query latency / error log 등 운영 가시성 추가를 대비해 시그니처에 유지하되,
// 현재 구현에서는 사용하지 않습니다 (Gemini code review #8 — 미사용 필드 정리).
// 다른 Repository 들 (NewContentRepository 등) 의 시그니처와 일관성을 위해 인자는 보존.
func NewParsingRuleRepository(pool *pgxpool.Pool, log *logger.Logger) storage.ParsingRuleRepository {
	_ = log
	return &pgParsingRuleRepository{pool: pool}
}

const sqlInsertParsingRule = `
INSERT INTO parsing_rules (
  source_name, host_pattern, target_type, version, enabled, selectors, description
) VALUES (
  $1, $2, $3, $4, $5, $6, $7
)
RETURNING id, created_at, updated_at
`

// Insert 는 새 규칙을 저장합니다. 자연키 (source_name, host_pattern, target_type, version) 충돌 시
// storage.ErrDuplicate 를 반환합니다. 성공 시 r.ID / CreatedAt / UpdatedAt 가 채워집니다.
func (r *pgParsingRuleRepository) Insert(ctx context.Context, rec *storage.ParsingRuleRecord) error {
	selectors, err := json.Marshal(rec.Selectors)
	if err != nil {
		return fmt.Errorf("marshal selectors: %w", err)
	}
	if rec.Version == 0 {
		rec.Version = 1
	}
	row := r.pool.QueryRow(ctx, sqlInsertParsingRule,
		rec.SourceName, rec.HostPattern, string(rec.TargetType), rec.Version,
		rec.Enabled, selectors, rec.Description,
	)
	if err := row.Scan(&rec.ID, &rec.CreatedAt, &rec.UpdatedAt); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgerrcode.UniqueViolation {
			return storage.ErrDuplicate
		}
		return fmt.Errorf("insert parsing rule: %w", err)
	}
	return nil
}

const sqlUpdateParsingRule = `
UPDATE parsing_rules
SET selectors = $2, enabled = $3, description = $4
WHERE id = $1
RETURNING updated_at
`

// Update 는 ID 로 규칙을 갱신합니다. 자연키 (source/host/type/version) 는 변경 불가 —
// 규칙 진화는 새 row 를 INSERT 후 enabled flip 으로 표현 권장.
func (r *pgParsingRuleRepository) Update(ctx context.Context, rec *storage.ParsingRuleRecord) error {
	selectors, err := json.Marshal(rec.Selectors)
	if err != nil {
		return fmt.Errorf("marshal selectors: %w", err)
	}
	row := r.pool.QueryRow(ctx, sqlUpdateParsingRule, rec.ID, selectors, rec.Enabled, rec.Description)
	if err := row.Scan(&rec.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return storage.ErrNotFound
		}
		return fmt.Errorf("update parsing rule %d: %w", rec.ID, err)
	}
	return nil
}

const sqlGetParsingRuleByID = `
SELECT id, source_name, host_pattern, target_type, version, enabled, selectors, description, created_at, updated_at
FROM parsing_rules
WHERE id = $1
`

// GetByID 는 ID 로 규칙을 조회합니다.
func (r *pgParsingRuleRepository) GetByID(ctx context.Context, id int64) (*storage.ParsingRuleRecord, error) {
	row := r.pool.QueryRow(ctx, sqlGetParsingRuleByID, id)
	rec, err := scanParsingRule(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, storage.ErrNotFound
		}
		return nil, fmt.Errorf("get parsing rule %d: %w", id, err)
	}
	return rec, nil
}

const sqlFindActiveParsingRule = `
SELECT id, source_name, host_pattern, target_type, version, enabled, selectors, description, created_at, updated_at
FROM parsing_rules
WHERE host_pattern = $1
  AND target_type  = $2
  AND enabled      = TRUE
ORDER BY version DESC
LIMIT 1
`

// FindActive 는 host + target_type 매칭 활성 규칙을 반환합니다 (RuleResolver 핫패스).
// 같은 (host, type) 에 여러 활성 row 가 있다면 version DESC 순으로 첫 항목.
func (r *pgParsingRuleRepository) FindActive(ctx context.Context, host string, targetType storage.TargetType) (*storage.ParsingRuleRecord, error) {
	row := r.pool.QueryRow(ctx, sqlFindActiveParsingRule, host, string(targetType))
	rec, err := scanParsingRule(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, storage.ErrNotFound
		}
		return nil, fmt.Errorf("find active parsing rule (%s, %s): %w", host, targetType, err)
	}
	return rec, nil
}

// List 는 필터 조건에 맞는 규칙들을 반환합니다.
func (r *pgParsingRuleRepository) List(ctx context.Context, f storage.ParsingRuleFilter) ([]*storage.ParsingRuleRecord, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}

	query := `
SELECT id, source_name, host_pattern, target_type, version, enabled, selectors, description, created_at, updated_at
FROM parsing_rules
WHERE 1=1`
	args := make([]any, 0, 4)
	idx := 1

	if f.SourceName != "" {
		query += fmt.Sprintf(" AND source_name = $%d", idx)
		args = append(args, f.SourceName)
		idx++
	}
	if f.HostPattern != "" {
		query += fmt.Sprintf(" AND host_pattern = $%d", idx)
		args = append(args, f.HostPattern)
		idx++
	}
	if f.TargetType != "" {
		query += fmt.Sprintf(" AND target_type = $%d", idx)
		args = append(args, string(f.TargetType))
		idx++
	}
	if f.OnlyEnabled {
		query += " AND enabled = TRUE"
	}

	query += fmt.Sprintf(" ORDER BY source_name, host_pattern, target_type, version DESC LIMIT $%d OFFSET $%d", idx, idx+1)
	args = append(args, limit, f.Offset)

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list parsing rules: %w", err)
	}
	defer rows.Close()

	var out []*storage.ParsingRuleRecord
	for rows.Next() {
		rec, err := scanParsingRule(rows)
		if err != nil {
			return nil, fmt.Errorf("scan parsing rule row: %w", err)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate parsing rule rows: %w", err)
	}
	return out, nil
}

const sqlDeleteParsingRule = `DELETE FROM parsing_rules WHERE id = $1`

// Delete 는 ID 로 규칙을 삭제합니다 (idempotent — 미존재여도 nil).
func (r *pgParsingRuleRepository) Delete(ctx context.Context, id int64) error {
	if _, err := r.pool.Exec(ctx, sqlDeleteParsingRule, id); err != nil {
		return fmt.Errorf("delete parsing rule %d: %w", id, err)
	}
	return nil
}

// scanParsingRule 은 Row/Rows 에서 ParsingRuleRecord 를 스캔합니다.
// selectors 는 raw JSONB → SelectorMap 으로 unmarshal.
func scanParsingRule(s scanner) (*storage.ParsingRuleRecord, error) {
	rec := &storage.ParsingRuleRecord{}
	var selectorsRaw []byte
	var targetType string
	if err := s.Scan(
		&rec.ID, &rec.SourceName, &rec.HostPattern, &targetType, &rec.Version,
		&rec.Enabled, &selectorsRaw, &rec.Description, &rec.CreatedAt, &rec.UpdatedAt,
	); err != nil {
		return nil, err
	}
	rec.TargetType = storage.TargetType(targetType)
	if len(selectorsRaw) > 0 {
		if err := json.Unmarshal(selectorsRaw, &rec.Selectors); err != nil {
			return nil, fmt.Errorf("unmarshal selectors for rule %d: %w", rec.ID, err)
		}
	}
	return rec, nil
}
