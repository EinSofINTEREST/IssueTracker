package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"issuetracker/internal/storage"
	"issuetracker/pkg/logger"
)

// pgSampleURLRepository 는 pgx/v5 기반 SampleURLRepository 구현체입니다.
type pgSampleURLRepository struct {
	pool *pgxpool.Pool
}

// NewSampleURLRepository 는 pgxpool 을 사용하는 SampleURLRepository 를 생성합니다.
//
// log 는 다른 Repository 와 시그니처 일관성을 위해 인자에 유지하지만 현재 미사용 (NewParserRuleRepository
// 와 동일 패턴 — Gemini code review #8).
func NewSampleURLRepository(pool *pgxpool.Pool, log *logger.Logger) storage.SampleURLRepository {
	_ = log
	return &pgSampleURLRepository{pool: pool}
}

const sqlCountSamples = `
SELECT COUNT(*)
FROM parser_rule_sample_urls
WHERE rule_id = $1
`

const sqlInsertSample = `
INSERT INTO parser_rule_sample_urls (rule_id, url)
VALUES ($1, $2)
`

// Insert 는 sample URL 을 누적합니다.
//
// 정책:
//   - 같은 (rule_id, url) 이미 있으면 storage.ErrDuplicate 반환 — 호출자가 무시 가능 (이미 누적됨)
//   - rule_id 의 누적 수가 SampleCapPerRule 도달했으면 skip + nil (운영 cap, 정상 흐름)
//
// 본 cap 정책은 \"trigger 가 동작하지 않는 케이스 (LLM_ENABLED=false / Redis 장애 등)\" 의
// DB 폭증 방어. 정상 흐름에서 trigger 가 5개 시점에 정밀화 + purge 하므로 본 cap 도달 X.
func (r *pgSampleURLRepository) Insert(ctx context.Context, ruleID int64, url string) error {
	// cap 검사 — INSERT 시도 전에 미리 차단 (UNIQUE 충돌과 별개의 cap 정책)
	count, err := r.Count(ctx, ruleID)
	if err != nil {
		return fmt.Errorf("count samples for cap check: %w", err)
	}
	if count >= storage.SampleCapPerRule {
		return nil // skip — 정상 흐름
	}

	if _, err := r.pool.Exec(ctx, sqlInsertSample, ruleID, url); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgerrcode.UniqueViolation {
			return storage.ErrDuplicate
		}
		return fmt.Errorf("insert sample url (rule=%d): %w", ruleID, err)
	}
	return nil
}

// Count 는 rule_id 의 현재 누적 sample 수를 반환합니다.
func (r *pgSampleURLRepository) Count(ctx context.Context, ruleID int64) (int, error) {
	var count int
	if err := r.pool.QueryRow(ctx, sqlCountSamples, ruleID).Scan(&count); err != nil {
		return 0, fmt.Errorf("count samples (rule=%d): %w", ruleID, err)
	}
	return count, nil
}

const sqlListSamples = `
SELECT id, rule_id, url, observed_at
FROM parser_rule_sample_urls
WHERE rule_id = $1
ORDER BY observed_at DESC
LIMIT $2
`

// List 는 rule_id 의 sample 들을 observed_at DESC 순으로 limit 만큼 반환합니다.
func (r *pgSampleURLRepository) List(ctx context.Context, ruleID int64, limit int) ([]*storage.SampleURL, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.pool.Query(ctx, sqlListSamples, ruleID, limit)
	if err != nil {
		return nil, fmt.Errorf("list samples (rule=%d): %w", ruleID, err)
	}
	defer rows.Close()

	var out []*storage.SampleURL
	for rows.Next() {
		s := &storage.SampleURL{}
		if scanErr := rows.Scan(&s.ID, &s.RuleID, &s.URL, &s.ObservedAt); scanErr != nil {
			return nil, fmt.Errorf("scan sample row: %w", scanErr)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sample rows: %w", err)
	}
	return out, nil
}

const sqlPurgeSamples = `DELETE FROM parser_rule_sample_urls WHERE rule_id = $1`

// Purge 는 rule_id 의 모든 sample 을 삭제합니다 (정밀화 완료 후 호출, idempotent).
func (r *pgSampleURLRepository) Purge(ctx context.Context, ruleID int64) error {
	if _, err := r.pool.Exec(ctx, sqlPurgeSamples, ruleID); err != nil {
		return fmt.Errorf("purge samples (rule=%d): %w", ruleID, err)
	}
	return nil
}
