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

// pgParserRuleRepository 는 pgx/v5 기반 ParserRuleRepository 구현체입니다.
type pgParserRuleRepository struct {
	pool *pgxpool.Pool
}

// NewParserRuleRepository 는 pgxpool 을 사용하는 ParserRuleRepository 를 생성합니다.
//
// log 인자는 향후 query latency / error log 등 운영 가시성 추가를 대비해 시그니처에 유지하되,
// 현재 구현에서는 사용하지 않습니다 (Gemini code review #8 — 미사용 필드 정리).
// 다른 Repository 들 (NewContentRepository 등) 의 시그니처와 일관성을 위해 인자는 보존.
func NewParserRuleRepository(pool *pgxpool.Pool, log *logger.Logger) storage.ParserRuleRepository {
	_ = log
	return &pgParserRuleRepository{pool: pool}
}

const sqlInsertParserRule = `
INSERT INTO parser_rules (
  source_name, host_pattern, path_pattern, target_type, version, enabled, selectors, confidence, description, page_type
) VALUES (
  $1, $2, $3, $4, $5, $6, $7, $8, $9, $10
)
RETURNING id, created_at, updated_at
`

// Insert 는 새 규칙을 저장합니다. 자연키 (source_name, host_pattern, path_pattern, target_type, version)
// 충돌 시 storage.ErrDuplicate 를 반환합니다.
//
// path_pattern 이 비어있지 않으면 RE2 컴파일 검증 — 실패 시 storage.ErrInvalid.
// DB write 전 마지막 방어선 — Resolver 가 매 호출마다 컴파일 실패한 패턴을 스킵하도록 두기보다,
// INSERT 시점에 거부하는 것이 운영 가시성 ↑ + DB 에 잘못된 row 진입 차단.
//
// 성공 시 r.ID / CreatedAt / UpdatedAt 가 채워집니다.
func (r *pgParserRuleRepository) Insert(ctx context.Context, rec *storage.ParserRuleRecord) error {
	if rec.PathPattern != "" {
		if _, reErr := regexp.Compile(rec.PathPattern); reErr != nil {
			return fmt.Errorf("%w: path_pattern %q is not a valid regex: %v", storage.ErrInvalid, rec.PathPattern, reErr)
		}
	}
	selectors, err := json.Marshal(rec.Selectors)
	if err != nil {
		return fmt.Errorf("marshal selectors: %w", err)
	}
	confidence, err := marshalConfidence(rec.Confidence)
	if err != nil {
		return fmt.Errorf("marshal confidence: %w", err)
	}
	if rec.Version == 0 {
		rec.Version = 1
	}
	row := r.pool.QueryRow(ctx, sqlInsertParserRule,
		rec.SourceName, rec.HostPattern, rec.PathPattern, string(rec.TargetType), rec.Version,
		rec.Enabled, selectors, confidence, rec.Description, rec.PageType,
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

const sqlUpdateParserRule = `
UPDATE parser_rules
SET selectors = $2, confidence = $3, enabled = $4, description = $5
WHERE id = $1
RETURNING updated_at
`

// Update 는 ID 로 규칙을 갱신합니다. 자연키 (source/host/path/type/version) 는 변경 불가 —
// 규칙 진화는 새 row 를 INSERT 후 enabled flip 으로 표현 권장.
//
// confidence 도 함께 갱신 — selectors 만 바뀌고 confidence 가 stale
// 로 남으면 하류 validator 가 잘못된 신뢰도로 판단. 호출자는 selectors 변경 시 confidence 도
// 같이 채워서 전달 (또는 nil/빈 map 으로 reset).
func (r *pgParserRuleRepository) Update(ctx context.Context, rec *storage.ParserRuleRecord) error {
	selectors, err := json.Marshal(rec.Selectors)
	if err != nil {
		return fmt.Errorf("marshal selectors: %w", err)
	}
	confidence, err := marshalConfidence(rec.Confidence)
	if err != nil {
		return fmt.Errorf("marshal confidence: %w", err)
	}
	row := r.pool.QueryRow(ctx, sqlUpdateParserRule, rec.ID, selectors, confidence, rec.Enabled, rec.Description)
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
// 단순 `WHERE id=$1` 은 lost-update 윈도우 발생:
//   - 다중 issuetracker 인스턴스에서 동시 정밀화 시 마지막 writer 만 적용
//   - 운영자가 List 직후 rule 의 source_name / enabled / path_pattern 을 수동 변경했을 때 덮어씀
//
// 가드 조건이 false 면 `pgx.ErrNoRows` → storage.ErrNotFound 반환 — 호출자 (refiner) 는 이미 ErrNotFound
// 분기에서 Invalidate / Purge 모두 skip 하므로 stale 변경 사이드이펙트 없음.
const sqlUpdatePathPattern = `
UPDATE parser_rules
SET path_pattern = $2, description = $3
WHERE id = $1
  AND source_name = 'llm-auto'
  AND enabled = TRUE
  AND path_pattern = ''
RETURNING updated_at
`

// UpdatePathPattern 은 정밀화 워크플로 에서 호출 — path_pattern + description 갱신.
//
// pattern != "" 이면 RE2 컴파일 검증 (Insert 와 동일 정책) — 실패 시 storage.ErrInvalid.
// rule 이 미존재하거나 catch-all + llm-auto + enabled 상태가 아니면 storage.ErrNotFound.
func (r *pgParserRuleRepository) UpdatePathPattern(ctx context.Context, id int64, pattern, description string) error {
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

const sqlGetParserRuleByID = `
SELECT id, source_name, host_pattern, path_pattern, target_type, version, enabled, selectors, confidence, description, page_type, created_at, updated_at
FROM parser_rules
WHERE id = $1
`

// GetByID 는 ID 로 규칙을 조회합니다.
func (r *pgParserRuleRepository) GetByID(ctx context.Context, id int64) (*storage.ParserRuleRecord, error) {
	row := r.pool.QueryRow(ctx, sqlGetParserRuleByID, id)
	rec, err := scanParserRule(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, storage.ErrNotFound
		}
		return nil, fmt.Errorf("get parsing rule %d: %w", id, err)
	}
	return rec, nil
}

const sqlFindParserRuleByNaturalKey = `
SELECT id, source_name, host_pattern, path_pattern, target_type, version, enabled, selectors, confidence, description, page_type, created_at, updated_at
FROM parser_rules
WHERE source_name  = $1
  AND host_pattern = $2
  AND path_pattern = $3
  AND target_type  = $4
  AND version      = $5
`

// FindByNaturalKey 는 자연키로 단일 rule 을 조회합니다.
//
// Insert 의 unique constraint 와 동일한 키 — enabled 필터 없음.
// 매칭 없으면 storage.ErrNotFound.
//
// 용도: llmgen.Generator 가 LLM 호출 전 사전 lookup 으로 사용. 이미 같은 자연키 룰이
// 존재하면 LLM 호출 (~55s sonnet) 을 회피하여 비용 절감.
func (r *pgParserRuleRepository) FindByNaturalKey(ctx context.Context, sourceName, hostPattern, pathPattern string, targetType storage.TargetType, version int) (*storage.ParserRuleRecord, error) {
	row := r.pool.QueryRow(ctx, sqlFindParserRuleByNaturalKey,
		sourceName, hostPattern, pathPattern, string(targetType), version,
	)
	rec, err := scanParserRule(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, storage.ErrNotFound
		}
		return nil, fmt.Errorf("find parsing rule by natural key: %w", err)
	}
	return rec, nil
}

const sqlMaxVersionByNaturalKey = `
SELECT COALESCE(MAX(version), 0)
FROM parser_rules
WHERE source_name = $1
  AND host_pattern = $2
  AND path_pattern = $3
  AND target_type  = $4
`

// InsertNextVersion 은 자연키의 max(version)+1 로 rec 을 INSERT 합니다.
//
// race window: MAX 조회와 INSERT 사이에 다른 인스턴스가 같은 version 을 INSERT 할 수 있음.
// 자연키 unique 제약이 ErrDuplicate 로 반응 — 호출자가 retry 또는 흡수 책임.
func (r *pgParserRuleRepository) InsertNextVersion(ctx context.Context, rec *storage.ParserRuleRecord) error {
	if rec.PathPattern != "" {
		if _, reErr := regexp.Compile(rec.PathPattern); reErr != nil {
			return fmt.Errorf("%w: path_pattern %q is not a valid regex: %v", storage.ErrInvalid, rec.PathPattern, reErr)
		}
	}
	var maxVersion int
	if err := r.pool.QueryRow(ctx, sqlMaxVersionByNaturalKey,
		rec.SourceName, rec.HostPattern, rec.PathPattern, string(rec.TargetType),
	).Scan(&maxVersion); err != nil {
		return fmt.Errorf("query max version: %w", err)
	}
	rec.Version = maxVersion + 1
	return r.Insert(ctx, rec)
}

// sqlHasAnyRule 은 (host_pattern, target_type) 에 대한 enabled / disabled 무관 존재 여부를
// 1 row 로 반환합니다.
//
// 단일 aggregate query — host_pattern + target_type 인덱스 스캔 1회로 exists / has_enabled 동시 산출.
// 매칭 row 없으면 COUNT(*)=0 / bool_or NULL → COALESCE 로 (FALSE, FALSE) 보장.
const sqlHasAnyRule = `
SELECT
  COUNT(*) > 0                        AS exists_any,
  COALESCE(bool_or(enabled), FALSE)   AS has_enabled
FROM parser_rules
WHERE host_pattern=$1 AND target_type=$2
`

// HasAnyRule 은 (host_pattern, target_type) 룰 존재 여부 + enabled 여부를 1 round-trip 으로 반환합니다.
//
// FindActiveCandidates 와 달리 enabled 필터 없음 — disabled 룰도 \"존재함\" 으로 카운트.
// 결과는 (exists, hasEnabled).
func (r *pgParserRuleRepository) HasAnyRule(ctx context.Context, hostPattern string, targetType storage.TargetType) (bool, bool, error) {
	row := r.pool.QueryRow(ctx, sqlHasAnyRule, hostPattern, string(targetType))
	var exists, hasEnabled bool
	if err := row.Scan(&exists, &hasEnabled); err != nil {
		return false, false, fmt.Errorf("has any rule (%s, %s): %w", hostPattern, targetType, err)
	}
	return exists, hasEnabled, nil
}

// sqlFindActiveCandidates 는 host + target_type 매칭 활성 rule 들을 후보 슬라이스로 반환합니다.
//
// 정렬:
//   - LENGTH(path_pattern) DESC : 더 구체적인 (긴) regex 패턴 우선 (path_pattern=” 은 길이 0 으로 마지막)
//   - version DESC             : 같은 패턴 안에서 최신 버전 우선
const sqlFindActiveCandidates = `
SELECT id, source_name, host_pattern, path_pattern, target_type, version, enabled, selectors, confidence, description, page_type, created_at, updated_at
FROM parser_rules
WHERE host_pattern = $1
  AND target_type  = $2
  AND enabled      = TRUE
ORDER BY LENGTH(path_pattern) DESC, version DESC
`

// FindActive 는 host + target_type 매칭 활성 규칙 1건을 반환합니다 (후방 호환).
//
// Deprecated: path_pattern 도입 후 후보 슬라이스를 받아 application 측에서 path 매칭하는
// FindActiveCandidates 사용 권장. 본 메소드는 호출자 호환을 위해 유지 — 내부적으로 후보 슬라이스의
// 첫 항목 (가장 구체적 또는 최신) 을 반환. ErrNotFound 시 storage.ErrNotFound.
func (r *pgParserRuleRepository) FindActive(ctx context.Context, host string, targetType storage.TargetType) (*storage.ParserRuleRecord, error) {
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
// version DESC 정렬로 반환합니다.
//
// 매칭 없으면 빈 슬라이스 + nil 에러 (호출자가 빈 슬라이스로 분기).
func (r *pgParserRuleRepository) FindActiveCandidates(ctx context.Context, host string, targetType storage.TargetType) ([]*storage.ParserRuleRecord, error) {
	rows, err := r.pool.Query(ctx, sqlFindActiveCandidates, host, string(targetType))
	if err != nil {
		return nil, fmt.Errorf("find active candidates (%s, %s): %w", host, targetType, err)
	}
	defer rows.Close()

	var out []*storage.ParserRuleRecord
	for rows.Next() {
		rec, scanErr := scanParserRule(rows)
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
func (r *pgParserRuleRepository) List(ctx context.Context, f storage.ParserRuleFilter) ([]*storage.ParserRuleRecord, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}

	query := `
SELECT id, source_name, host_pattern, path_pattern, target_type, version, enabled, selectors, confidence, description, page_type, created_at, updated_at
FROM parser_rules
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

	var out []*storage.ParserRuleRecord
	for rows.Next() {
		rec, err := scanParserRule(rows)
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

const sqlDeleteParserRule = `DELETE FROM parser_rules WHERE id = $1`

// Delete 는 ID 로 규칙을 삭제합니다 (idempotent — 미존재여도 nil).
func (r *pgParserRuleRepository) Delete(ctx context.Context, id int64) error {
	if _, err := r.pool.Exec(ctx, sqlDeleteParserRule, id); err != nil {
		return fmt.Errorf("delete parsing rule %d: %w", id, err)
	}
	return nil
}

// scanParserRule 은 Row/Rows 에서 ParserRuleRecord 를 스캔합니다.
// selectors / confidence 는 raw JSONB → application struct 로 unmarshal.
func scanParserRule(s scanner) (*storage.ParserRuleRecord, error) {
	rec := &storage.ParserRuleRecord{}
	var selectorsRaw, confidenceRaw []byte
	var targetType string
	if err := s.Scan(
		&rec.ID, &rec.SourceName, &rec.HostPattern, &rec.PathPattern, &targetType, &rec.Version,
		&rec.Enabled, &selectorsRaw, &confidenceRaw, &rec.Description, &rec.PageType,
		&rec.CreatedAt, &rec.UpdatedAt,
	); err != nil {
		return nil, err
	}
	rec.TargetType = storage.TargetType(targetType)
	if len(selectorsRaw) > 0 {
		if err := json.Unmarshal(selectorsRaw, &rec.Selectors); err != nil {
			return nil, fmt.Errorf("unmarshal selectors for rule %d: %w", rec.ID, err)
		}
	}
	if len(confidenceRaw) > 0 {
		if err := json.Unmarshal(confidenceRaw, &rec.Confidence); err != nil {
			return nil, fmt.Errorf("unmarshal confidence for rule %d: %w", rec.ID, err)
		}
	}
	return rec, nil
}

// marshalConfidence 는 Confidence map 을 JSONB byte 로 직렬화합니다.
//
// nil 또는 빈 map 은 "{}" 로 직렬화 — JSONB NOT NULL 제약 충족 + scan 시 빈 map 으로 복원.
func marshalConfidence(c map[string]storage.FieldConfidence) ([]byte, error) {
	if len(c) == 0 {
		return []byte("{}"), nil
	}
	return json.Marshal(c)
}
