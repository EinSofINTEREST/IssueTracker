package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"issuetracker/internal/storage"
	"issuetracker/internal/storage/model"
	"issuetracker/internal/storage/repository"
	"issuetracker/pkg/logger"
)

// canonicalizeHost 는 host_pattern 의 표준형을 강제합니다 (CodeRabbit 피드백).
// migration 013 의 CHECK 제약 (LOWER(BTRIM(...))) 과 동일한 정규화를 application 측에서 적용해
// 대소문자 / padding 차이로 인한 silent miss 를 회피.
func canonicalizeHost(host string) string {
	return strings.ToLower(strings.TrimSpace(host))
}

// pgFetcherRuleRepository 는 pgx/v5 기반 FetcherRuleRepository 구현체입니다.
type pgFetcherRuleRepository struct {
	pool *pgxpool.Pool
}

// NewFetcherRuleRepository 는 pgxpool 을 사용하는 FetcherRuleRepository 를 생성합니다.
//
// log 인자는 향후 query latency / error log 등 운영 가시성 추가를 대비해 시그니처에 유지하되,
// 현재 구현에서는 사용하지 않습니다 — 다른 Repository 들과 일관된 시그니처.
//
// pool 이 nil 이면 error 반환.
func NewFetcherRuleRepository(pool *pgxpool.Pool, log *logger.Logger) (repository.FetcherRuleRepository, error) {
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
func (r *pgFetcherRuleRepository) Upsert(ctx context.Context, host string, fetcher model.FetcherKind, reason string) error {
	host = canonicalizeHost(host)
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
SELECT id, host_pattern, fetcher, COALESCE(reason, ''),
       COALESCE(source_name, ''), COALESCE(source_type, ''),
       COALESCE(country, ''), COALESCE(language, ''),
       COALESCE(base_url, ''), COALESCE(requests_per_hour, 0),
       created_at, updated_at
FROM fetcher_rules
WHERE host_pattern = $1
`

// GetByHost 는 host_pattern exact match 로 단일 row 를 반환합니다.
// 매칭 없으면 storage.ErrNotFound — Resolver 가 errors.Is 로 분기 (캐시 negative entry 등).
//
// 배포 전제: 이 쿼리는 migration 014 적용 이후 배포해야 합니다.
// 014 이전 schema 에서는 source_name 등 컬럼이 없어 즉시 에러가 발생합니다.
func (r *pgFetcherRuleRepository) GetByHost(ctx context.Context, host string) (*model.FetcherRuleRecord, error) {
	host = canonicalizeHost(host)
	rec := &model.FetcherRuleRecord{}
	var fetcher string
	err := r.pool.QueryRow(ctx, sqlGetFetcherRuleByHost, host).Scan(
		&rec.ID, &rec.HostPattern, &fetcher, &rec.Reason,
		&rec.SourceName, &rec.SourceType, &rec.Country, &rec.Language,
		&rec.BaseURL, &rec.RequestsPerHour,
		&rec.CreatedAt, &rec.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, storage.ErrNotFound
		}
		return nil, fmt.Errorf("get fetcher rule by host %s: %w", host, err)
	}
	rec.Fetcher = model.FetcherKind(fetcher)
	return rec, nil
}

const sqlListFetcherRules = `
SELECT id, host_pattern, fetcher, COALESCE(reason, ''),
       COALESCE(source_name, ''), COALESCE(source_type, ''),
       COALESCE(country, ''), COALESCE(language, ''),
       COALESCE(base_url, ''), COALESCE(requests_per_hour, 0),
       created_at, updated_at
FROM fetcher_rules
ORDER BY host_pattern ASC
`

// List 는 모든 fetcher_rules 를 host_pattern ASC 로 반환합니다.
func (r *pgFetcherRuleRepository) List(ctx context.Context) ([]*model.FetcherRuleRecord, error) {
	rows, err := r.pool.Query(ctx, sqlListFetcherRules)
	if err != nil {
		return nil, fmt.Errorf("list fetcher rules: %w", err)
	}
	defer rows.Close()

	var out []*model.FetcherRuleRecord
	for rows.Next() {
		rec := &model.FetcherRuleRecord{}
		var fetcher string
		if err := rows.Scan(
			&rec.ID, &rec.HostPattern, &fetcher, &rec.Reason,
			&rec.SourceName, &rec.SourceType, &rec.Country, &rec.Language,
			&rec.BaseURL, &rec.RequestsPerHour,
			&rec.CreatedAt, &rec.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan fetcher rule: %w", err)
		}
		rec.Fetcher = model.FetcherKind(fetcher)
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
	host = canonicalizeHost(host)
	if _, err := r.pool.Exec(ctx, sqlDeleteFetcherRule, host); err != nil {
		return fmt.Errorf("delete fetcher rule %s: %w", host, err)
	}
	return nil
}

// sqlBulkDowngradeAutoUpgraded 는 자동 upgrade 로 chromedp 가 된 모든 host 를 goquery 로 다시 내립니다.
//
// 매칭 조건은 reason='auto_upgrade_validation' AND fetcher='chromedp' — manual rule 보호 + 미래 자동 reason 영향 차단.
// DELETE 대신 UPDATE — created_at 등 audit trail 보존.
// RETURNING host_pattern 으로 변경된 호스트 슬라이스를 application 에 반환 — Resolver cache 동기화에 사용.
const sqlBulkDowngradeAutoUpgraded = `
UPDATE fetcher_rules
SET fetcher    = 'goquery',
    updated_at = NOW()
WHERE reason  = 'auto_upgrade_validation'
  AND fetcher = 'chromedp'
RETURNING host_pattern
`

// BulkDowngradeAutoUpgraded 는 auto_upgrade_validation row 를 goquery 로 일괄 변환하고 변경된 host 슬라이스를 반환합니다.
func (r *pgFetcherRuleRepository) BulkDowngradeAutoUpgraded(ctx context.Context) ([]string, error) {
	rows, err := r.pool.Query(ctx, sqlBulkDowngradeAutoUpgraded)
	if err != nil {
		return nil, fmt.Errorf("bulk downgrade auto-upgraded fetcher rules: %w", err)
	}
	defer rows.Close()

	var changed []string
	for rows.Next() {
		var host string
		if err := rows.Scan(&host); err != nil {
			return nil, fmt.Errorf("scan downgraded host: %w", err)
		}
		changed = append(changed, host)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("bulk downgrade iter: %w", err)
	}
	return changed, nil
}
