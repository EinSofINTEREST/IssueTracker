package postgres

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"issuetracker/internal/storage"
	"issuetracker/pkg/logger"
)

// pgBlacklistRepository 는 pgx/v5 기반 BlacklistRepository 구현체입니다 (이슈 #295).
type pgBlacklistRepository struct {
	pool *pgxpool.Pool
}

// NewBlacklistRepository 는 pgxpool 기반 BlacklistRepository 를 생성합니다.
// log 인자는 향후 query latency / error log 등 운영 가시성 추가용 — 현재 미사용.
func NewBlacklistRepository(pool *pgxpool.Pool, log *logger.Logger) storage.BlacklistRepository {
	_ = log
	return &pgBlacklistRepository{pool: pool}
}

const sqlInsertBlacklist = `
INSERT INTO parsing_blacklist (host_pattern, path_pattern, reason, source, mode, enabled)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id, created_at, updated_at
`

// Insert 는 새 row 를 저장합니다. 자연키 (host_pattern, path_pattern) 충돌 시 ErrDuplicate.
//
// path_pattern 이 비어있지 않으면 RE2 컴파일 검증 — parsing_rules.Insert 와 동일 정책 (DB write
// 전 잘못된 regex 거부, 운영 가시성 ↑ + Matcher 가 매 호출마다 negative cache 로 흡수하지 않도록).
//
// PR #296 gemini 피드백: HostPattern 을 lowercase 로 정규화 — BlacklistMatcher 가 host 를
// lowercase 로 lookup 하므로 저장 시점에도 동일 정규화 필수 (대소문자 섞인 등록 시 미스매치 회피).
func (r *pgBlacklistRepository) Insert(ctx context.Context, rec *storage.BlacklistRecord) error {
	if rec.PathPattern != "" {
		if _, reErr := regexp.Compile(rec.PathPattern); reErr != nil {
			return fmt.Errorf("%w: path_pattern %q is not a valid regex: %v", storage.ErrInvalid, rec.PathPattern, reErr)
		}
	}
	rec.HostPattern = strings.ToLower(rec.HostPattern)
	if rec.Source == "" {
		rec.Source = storage.BlacklistSourceManual
	}
	if rec.Mode == "" {
		rec.Mode = storage.BlacklistModeDrop
	}
	row := r.pool.QueryRow(ctx, sqlInsertBlacklist,
		rec.HostPattern, rec.PathPattern, rec.Reason, string(rec.Source), string(rec.Mode), rec.Enabled,
	)
	if err := row.Scan(&rec.ID, &rec.CreatedAt, &rec.UpdatedAt); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgerrcode.UniqueViolation {
			return storage.ErrDuplicate
		}
		return fmt.Errorf("insert blacklist: %w", err)
	}
	return nil
}

const sqlUpdateBlacklist = `
UPDATE parsing_blacklist
SET reason = $2, source = $3, mode = $4, enabled = $5, updated_at = NOW()
WHERE id = $1
RETURNING updated_at
`

// Update 는 ID 로 row 의 mutable 필드 (reason / source / mode / enabled) 만 갱신합니다.
// 자연키 (host_pattern, path_pattern) 는 변경 불가 — 의도가 다르면 Delete + Insert.
func (r *pgBlacklistRepository) Update(ctx context.Context, rec *storage.BlacklistRecord) error {
	if rec.Mode == "" {
		rec.Mode = storage.BlacklistModeDrop
	}
	row := r.pool.QueryRow(ctx, sqlUpdateBlacklist,
		rec.ID, rec.Reason, string(rec.Source), string(rec.Mode), rec.Enabled,
	)
	if err := row.Scan(&rec.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return storage.ErrNotFound
		}
		return fmt.Errorf("update blacklist: %w", err)
	}
	return nil
}

const sqlDeleteBlacklist = `DELETE FROM parsing_blacklist WHERE id = $1`

// Delete 는 ID 로 row 를 삭제합니다. 존재하지 않아도 nil 반환 (idempotent).
func (r *pgBlacklistRepository) Delete(ctx context.Context, id int64) error {
	if _, err := r.pool.Exec(ctx, sqlDeleteBlacklist, id); err != nil {
		return fmt.Errorf("delete blacklist: %w", err)
	}
	return nil
}

const sqlGetBlacklistByID = `
SELECT id, host_pattern, path_pattern, reason, source, mode, enabled, created_at, updated_at
FROM parsing_blacklist
WHERE id = $1
`

// GetByID 는 ID 로 row 를 조회합니다.
func (r *pgBlacklistRepository) GetByID(ctx context.Context, id int64) (*storage.BlacklistRecord, error) {
	row := r.pool.QueryRow(ctx, sqlGetBlacklistByID, id)
	rec, err := scanBlacklist(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, storage.ErrNotFound
		}
		return nil, fmt.Errorf("get blacklist by id: %w", err)
	}
	return rec, nil
}

const sqlFindEnabledBlacklistByHost = `
SELECT id, host_pattern, path_pattern, reason, source, mode, enabled, created_at, updated_at
FROM parsing_blacklist
WHERE host_pattern = $1 AND enabled = TRUE
ORDER BY LENGTH(path_pattern) DESC, id DESC
`

// FindEnabledByHost 는 (host) 매칭 enabled=TRUE row 들을 LENGTH(path_pattern) DESC 정렬로 반환합니다.
//
// parsing_rules.FindActiveCandidates 와 동일 정책 — 더 구체적인 path 우선 평가, catch-all (path="")
// 이 가장 마지막. tie-break 로 id DESC (최근 등록 우선) 사용.
//
// host 는 lowercase 로 정규화된 상태 가정 (Matcher 가 lowercase 로 호출). DB 에 저장된 host_pattern
// 도 Insert 시 lowercase 정규화 (PR #296 gemini 피드백) — 양쪽 일치 보장.
func (r *pgBlacklistRepository) FindEnabledByHost(ctx context.Context, host string) ([]*storage.BlacklistRecord, error) {
	rows, err := r.pool.Query(ctx, sqlFindEnabledBlacklistByHost, strings.ToLower(host))
	if err != nil {
		return nil, fmt.Errorf("query blacklist by host: %w", err)
	}
	defer rows.Close()

	out := make([]*storage.BlacklistRecord, 0)
	for rows.Next() {
		rec, scanErr := scanBlacklist(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan blacklist row: %w", scanErr)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate blacklist rows: %w", err)
	}
	return out, nil
}

// List 는 필터 조건에 맞는 row 들을 반환합니다 (운영 대시보드용).
func (r *pgBlacklistRepository) List(ctx context.Context, f storage.BlacklistFilter) ([]*storage.BlacklistRecord, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	q := `
SELECT id, host_pattern, path_pattern, reason, source, mode, enabled, created_at, updated_at
FROM parsing_blacklist
WHERE ($1 = '' OR host_pattern = $1)
  AND ($2 = '' OR source = $2)
  AND (NOT $3 OR enabled = TRUE)
ORDER BY id DESC
LIMIT $4 OFFSET $5
`
	// HostPattern 을 lowercase 로 정규화 — Insert 와 동일 정책 (PR #296 gemini 피드백).
	// 저장은 lowercase 이므로 검색 조건도 lowercase 여야 일치.
	rows, err := r.pool.Query(ctx, q,
		strings.ToLower(f.HostPattern), string(f.Source), f.OnlyEnabled, limit, f.Offset,
	)
	if err != nil {
		return nil, fmt.Errorf("list blacklist: %w", err)
	}
	defer rows.Close()

	out := make([]*storage.BlacklistRecord, 0)
	for rows.Next() {
		rec, scanErr := scanBlacklist(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan blacklist row: %w", scanErr)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate blacklist rows: %w", err)
	}
	return out, nil
}

// scanBlacklist 는 pgx Row / Rows scanner 를 통합 처리합니다.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanBlacklist(s rowScanner) (*storage.BlacklistRecord, error) {
	rec := &storage.BlacklistRecord{}
	var source, mode string
	if err := s.Scan(
		&rec.ID,
		&rec.HostPattern,
		&rec.PathPattern,
		&rec.Reason,
		&source,
		&mode,
		&rec.Enabled,
		&rec.CreatedAt,
		&rec.UpdatedAt,
	); err != nil {
		return nil, err
	}
	rec.Source = storage.BlacklistSource(source)
	rec.Mode = storage.BlacklistMode(mode)
	return rec, nil
}

// 컴파일 타임 인터페이스 만족 검증.
var _ storage.BlacklistRepository = (*pgBlacklistRepository)(nil)
