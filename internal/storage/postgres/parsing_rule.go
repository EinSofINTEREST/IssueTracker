package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

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
  source_name, host_pattern, path_pattern, target_type, version, enabled, selectors, description
) VALUES (
  $1, $2, $3, $4, $5, $6, $7, $8
)
RETURNING id, created_at, updated_at
`

// Insert 는 새 규칙을 저장합니다. 자연키 (source_name, host_pattern, path_pattern, target_type, version)
// 충돌 시 storage.ErrDuplicate 를 반환합니다.
//
// path_pattern 이 비어있지 않으면 RE2 컴파일 검증 — 실패 시 storage.ErrInvalid (이슈 #173).
// DB write 전 마지막 방어선 — Resolver 가 매 호출마다 컴파일 실패한 패턴을 스킵하도록 두기보다,
// INSERT 시점에 거부하는 것이 운영 가시성 ↑ + DB 에 잘못된 row 진입 차단.
//
// 성공 시 r.ID / CreatedAt / UpdatedAt 가 채워집니다.
func (r *pgParsingRuleRepository) Insert(ctx context.Context, rec *storage.ParsingRuleRecord) error {
	if rec.PathPattern != "" {
		if _, reErr := regexp.Compile(rec.PathPattern); reErr != nil {
			return fmt.Errorf("%w: path_pattern %q is not a valid regex: %v", storage.ErrInvalid, rec.PathPattern, reErr)
		}
	}
	selectors, err := json.Marshal(rec.Selectors)
	if err != nil {
		return fmt.Errorf("marshal selectors: %w", err)
	}
	if rec.Version == 0 {
		rec.Version = 1
	}
	row := r.pool.QueryRow(ctx, sqlInsertParsingRule,
		rec.SourceName, rec.HostPattern, rec.PathPattern, string(rec.TargetType), rec.Version,
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

// Update 는 ID 로 규칙을 갱신합니다. 자연키 (source/host/path/type/version) 는 변경 불가 —
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

// sqlUpdatePathPattern 은 catch-all + llm-auto + enabled 상태 가드를 포함한 optimistic update 입니다.
//
// PR #191 CodeRabbit 피드백 — 단순 `WHERE id=$1` 은 lost-update 윈도우 발생:
//   - 다중 issuetracker 인스턴스에서 동시 정밀화 시 마지막 writer 만 적용
//   - 운영자가 List 직후 rule 의 source_name / enabled / path_pattern 을 수동 변경했을 때 덮어씀
//
// 가드 조건이 false 면 `pgx.ErrNoRows` → storage.ErrNotFound 반환 — 호출자 (refiner) 는 이미 ErrNotFound
// 분기에서 Invalidate / Purge 모두 skip 하므로 stale 변경 사이드이펙트 없음.
const sqlUpdatePathPattern = `
UPDATE parsing_rules
SET path_pattern = $2, description = $3
WHERE id = $1
  AND source_name = 'llm-auto'
  AND enabled = TRUE
  AND path_pattern = ''
RETURNING updated_at
`

// UpdatePathPattern 은 정밀화 워크플로 (이슈 #173 단계 4-2) 에서 호출 — path_pattern + description 갱신.
//
// pattern != "" 이면 RE2 컴파일 검증 (Insert 와 동일 정책) — 실패 시 storage.ErrInvalid.
// rule 이 미존재하거나 catch-all + llm-auto + enabled 상태가 아니면 storage.ErrNotFound.
func (r *pgParsingRuleRepository) UpdatePathPattern(ctx context.Context, id int64, pattern, description string) error {
	if pattern != "" {
		if _, reErr := regexp.Compile(pattern); reErr != nil {
			return fmt.Errorf("%w: path_pattern %q is not a valid regex: %v", storage.ErrInvalid, pattern, reErr)
		}
	}
	var updatedAt time.Time
	if err := r.pool.QueryRow(ctx, sqlUpdatePathPattern, id, pattern, description).Scan(&updatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return storage.ErrNotFound
		}
		return fmt.Errorf("update path_pattern (id=%d): %w", id, err)
	}
	return nil
}

const sqlGetParsingRuleByID = `
SELECT id, source_name, host_pattern, path_pattern, target_type, version, enabled, selectors, description, created_at, updated_at
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

const sqlFindParsingRuleByNaturalKey = `
SELECT id, source_name, host_pattern, path_pattern, target_type, version, enabled, selectors, description, created_at, updated_at
FROM parsing_rules
WHERE source_name  = $1
  AND host_pattern = $2
  AND path_pattern = $3
  AND target_type  = $4
  AND version      = $5
`

// FindByNaturalKey 는 자연키로 단일 rule 을 조회합니다 (이슈 #274).
//
// Insert 의 unique constraint 와 동일한 키 — enabled 필터 없음.
// 매칭 없으면 storage.ErrNotFound.
//
// 용도: llmgen.Generator 가 LLM 호출 전 사전 lookup 으로 사용. 이미 같은 자연키 룰이
// 존재하면 LLM 호출 (~55s sonnet) 을 회피하여 비용 절감.
func (r *pgParsingRuleRepository) FindByNaturalKey(ctx context.Context, sourceName, hostPattern, pathPattern string, targetType storage.TargetType, version int) (*storage.ParsingRuleRecord, error) {
	row := r.pool.QueryRow(ctx, sqlFindParsingRuleByNaturalKey,
		sourceName, hostPattern, pathPattern, string(targetType), version,
	)
	rec, err := scanParsingRule(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, storage.ErrNotFound
		}
		return nil, fmt.Errorf("find parsing rule by natural key: %w", err)
	}
	return rec, nil
}

// sqlFindActiveCandidates 는 host + target_type 매칭 활성 rule 들을 후보 슬라이스로 반환합니다 (이슈 #173).
//
// 정렬:
//   - LENGTH(path_pattern) DESC : 더 구체적인 (긴) regex 패턴 우선 (path_pattern=” 은 길이 0 으로 마지막)
//   - version DESC             : 같은 패턴 안에서 최신 버전 우선
const sqlFindActiveCandidates = `
SELECT id, source_name, host_pattern, path_pattern, target_type, version, enabled, selectors, description, created_at, updated_at
FROM parsing_rules
WHERE host_pattern = $1
  AND target_type  = $2
  AND enabled      = TRUE
ORDER BY LENGTH(path_pattern) DESC, version DESC
`

// FindActive 는 host + target_type 매칭 활성 규칙 1건을 반환합니다 (후방 호환).
//
// Deprecated (이슈 #173): path_pattern 도입 후 후보 슬라이스를 받아 application 측에서 path 매칭하는
// FindActiveCandidates 사용 권장. 본 메소드는 호출자 호환을 위해 유지 — 내부적으로 후보 슬라이스의
// 첫 항목 (가장 구체적 또는 최신) 을 반환. ErrNotFound 시 storage.ErrNotFound.
func (r *pgParsingRuleRepository) FindActive(ctx context.Context, host string, targetType storage.TargetType) (*storage.ParsingRuleRecord, error) {
	candidates, err := r.FindActiveCandidates(ctx, host, targetType)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, storage.ErrNotFound
	}
	return candidates[0], nil
}

// FindActiveCandidates 는 host + target_type 매칭 활성 rule 들을 LENGTH(path_pattern) DESC,
// version DESC 정렬로 반환합니다 (이슈 #173).
//
// 매칭 없으면 빈 슬라이스 + nil 에러 (호출자가 빈 슬라이스로 분기).
func (r *pgParsingRuleRepository) FindActiveCandidates(ctx context.Context, host string, targetType storage.TargetType) ([]*storage.ParsingRuleRecord, error) {
	rows, err := r.pool.Query(ctx, sqlFindActiveCandidates, host, string(targetType))
	if err != nil {
		return nil, fmt.Errorf("find active candidates (%s, %s): %w", host, targetType, err)
	}
	defer rows.Close()

	var out []*storage.ParsingRuleRecord
	for rows.Next() {
		rec, scanErr := scanParsingRule(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan candidate: %w", scanErr)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate candidates: %w", err)
	}
	return out, nil
}

// List 는 필터 조건에 맞는 규칙들을 반환합니다.
func (r *pgParsingRuleRepository) List(ctx context.Context, f storage.ParsingRuleFilter) ([]*storage.ParsingRuleRecord, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}

	query := `
SELECT id, source_name, host_pattern, path_pattern, target_type, version, enabled, selectors, description, created_at, updated_at
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
		&rec.ID, &rec.SourceName, &rec.HostPattern, &rec.PathPattern, &targetType, &rec.Version,
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
