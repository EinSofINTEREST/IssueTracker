package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"issuetracker/internal/storage"
	"issuetracker/pkg/logger"
)

// pgFetcherRuleRepository 는 pgx/v5 기반 FetcherRuleRepository 구현체입니다 (이슈 #175 단계 1).
type pgFetcherRuleRepository struct {
	pool *pgxpool.Pool
}

// NewFetcherRuleRepository 는 pgxpool 을 사용하는 FetcherRuleRepository 를 생성합니다.
//
// log 인자는 향후 query latency / error log 등 운영 가시성 추가를 대비해 시그니처에 유지하되,
// 현재 구현에서는 사용하지 않습니다 — 다른 Repository 들과 일관된 시그니처.
//
// pool 이 nil 이면 error 반환 (이슈 #208 의 panic-on-nil → error 마이그레이션 정책).
func NewFetcherRuleRepository(pool *pgxpool.Pool, log *logger.Logger) (storage.FetcherRuleRepository, error) {
	if pool == nil {
		return nil, errors.New("postgres: NewFetcherRuleRepository requires non-nil pool")
	}
	_ = log
	return &pgFetcherRuleRepository{pool: pool}, nil
}

// sqlUpsertFetcherRule 는 host_pattern 단위 UPSERT 입니다 — 신규 INSERT 또는 fetcher / reason 갱신.
// updated_at 은 ON CONFLICT 분기에서 NOW() 로 명시 갱신.
const sqlUpsertFetcherRule = `
INSERT INTO fetcher_rules (host_pattern, fetcher, reason)
VALUES ($1, $2, $3)
ON CONFLICT (host_pattern)
DO UPDATE SET
  fetcher    = EXCLUDED.fetcher,
  reason     = EXCLUDED.reason,
  updated_at = NOW()
`

// Upsert 는 host_pattern 단위 UPSERT 를 수행합니다.
// host 가 빈 문자열이거나 fetcher 가 유효하지 않으면 storage.ErrInvalid 반환 — DB CHECK 제약과 application 측 검증 이중화.
func (r *pgFetcherRuleRepository) Upsert(ctx context.Context, host string, fetcher storage.FetcherKind, reason string) error {
	if host == "" {
		return fmt.Errorf("upsert fetcher rule: %w: empty host_pattern", storage.ErrInvalid)
	}
	if !fetcher.IsValid() {
		return fmt.Errorf("upsert fetcher rule: %w: invalid fetcher %q", storage.ErrInvalid, fetcher)
	}
	if _, err := r.pool.Exec(ctx, sqlUpsertFetcherRule, host, string(fetcher), reason); err != nil {
		return fmt.Errorf("upsert fetcher rule for %s: %w", host, err)
	}
	return nil
}

const sqlGetFetcherRuleByHost = `
SELECT id, host_pattern, fetcher, COALESCE(reason, ''), created_at, updated_at
FROM fetcher_rules
WHERE host_pattern = $1
`

// GetByHost 는 host_pattern exact match 로 단일 row 를 반환합니다.
// 매칭 없으면 storage.ErrNotFound — Resolver 가 errors.Is 로 분기 (캐시 negative entry 등).
func (r *pgFetcherRuleRepository) GetByHost(ctx context.Context, host string) (*storage.FetcherRuleRecord, error) {
	rec := &storage.FetcherRuleRecord{}
	var fetcher string
	err := r.pool.QueryRow(ctx, sqlGetFetcherRuleByHost, host).Scan(
		&rec.ID, &rec.HostPattern, &fetcher, &rec.Reason, &rec.CreatedAt, &rec.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, storage.ErrNotFound
		}
		return nil, fmt.Errorf("get fetcher rule by host %s: %w", host, err)
	}
	rec.Fetcher = storage.FetcherKind(fetcher)
	return rec, nil
}

const sqlListFetcherRules = `
SELECT id, host_pattern, fetcher, COALESCE(reason, ''), created_at, updated_at
FROM fetcher_rules
ORDER BY host_pattern ASC
`

// List 는 모든 fetcher_rules 를 host_pattern ASC 로 반환합니다.
func (r *pgFetcherRuleRepository) List(ctx context.Context) ([]*storage.FetcherRuleRecord, error) {
	rows, err := r.pool.Query(ctx, sqlListFetcherRules)
	if err != nil {
		return nil, fmt.Errorf("list fetcher rules: %w", err)
	}
	defer rows.Close()

	var out []*storage.FetcherRuleRecord
	for rows.Next() {
		rec := &storage.FetcherRuleRecord{}
		var fetcher string
		if err := rows.Scan(&rec.ID, &rec.HostPattern, &fetcher, &rec.Reason, &rec.CreatedAt, &rec.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan fetcher rule: %w", err)
		}
		rec.Fetcher = storage.FetcherKind(fetcher)
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list fetcher rules iter: %w", err)
	}
	return out, nil
}

const sqlDeleteFetcherRule = `DELETE FROM fetcher_rules WHERE host_pattern = $1`

// Delete 는 host_pattern 으로 row 를 제거합니다. 존재 여부와 무관하게 nil 반환 (idempotent).
func (r *pgFetcherRuleRepository) Delete(ctx context.Context, host string) error {
	if _, err := r.pool.Exec(ctx, sqlDeleteFetcherRule, host); err != nil {
		return fmt.Errorf("delete fetcher rule %s: %w", host, err)
	}
	return nil
}
