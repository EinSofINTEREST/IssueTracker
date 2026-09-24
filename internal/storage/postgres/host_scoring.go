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

// pgHostScoringRepository 는 pgx/v5 기반 HostScoringRepository 구현체입니다 (이슈 #382).
type pgHostScoringRepository struct {
	pool *pgxpool.Pool
}

// NewHostScoringRepository 는 pgxpool 을 사용하는 HostScoringRepository 를 생성합니다.
//
// log 는 다른 Repository 와 시그니처 일관성을 위해 유지하되 현재 미사용입니다.
func NewHostScoringRepository(pool *pgxpool.Pool, log *logger.Logger) repository.HostScoringRepository {
	_ = log
	return &pgHostScoringRepository{pool: pool}
}

const sqlUpsertHostScoring = `
INSERT INTO host_scoring_state (host, score, signals, window_minutes, calculated_at)
VALUES ($1, $2, $3, $4, NOW())
ON CONFLICT (host) DO UPDATE SET
  score = EXCLUDED.score,
  signals = EXCLUDED.signals,
  window_minutes = EXCLUDED.window_minutes,
  calculated_at = EXCLUDED.calculated_at
`

const sqlGetHostScoring = `
SELECT host, score, signals, window_minutes, calculated_at
FROM host_scoring_state
WHERE host = $1
`

const sqlListHostScoring = `
SELECT host, score, signals, window_minutes, calculated_at
FROM host_scoring_state
`

// Upsert 는 host 의 점수 상태를 저장합니다.
//
// calculated_at 은 DB 의 NOW() 를 쓴다 — 여러 인스턴스의 시계가 어긋나도 stale 판정이
// 한 기준으로 이뤄지게 하기 위함입니다.
func (r *pgHostScoringRepository) Upsert(ctx context.Context, state *model.HostScoringState) error {
	if state == nil {
		return errors.New("host scoring upsert: nil state")
	}
	if state.Host == "" {
		return errors.New("host scoring upsert: empty host")
	}
	signals := state.Signals
	if len(signals) == 0 {
		// NOT NULL + JSONB 컬럼이라 빈 값을 그대로 넣으면 실패한다.
		signals = []byte(`{}`)
	}
	_, err := r.pool.Exec(ctx, sqlUpsertHostScoring,
		state.Host, state.Score, signals, state.WindowMinutes)
	if err != nil {
		return fmt.Errorf("upsert host scoring state %q: %w", state.Host, err)
	}
	return nil
}

// Get 은 host 의 점수 상태를 반환합니다. 없으면 storage.ErrNotFound.
func (r *pgHostScoringRepository) Get(ctx context.Context, host string) (*model.HostScoringState, error) {
	row := r.pool.QueryRow(ctx, sqlGetHostScoring, host)

	var s model.HostScoringState
	if err := row.Scan(&s.Host, &s.Score, &s.Signals, &s.WindowMinutes, &s.CalculatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, storage.ErrNotFound
		}
		return nil, fmt.Errorf("get host scoring state %q: %w", host, err)
	}
	return &s, nil
}

// ListAll 은 모든 점수 상태를 반환합니다 — resolver hydrate 용.
func (r *pgHostScoringRepository) ListAll(ctx context.Context) ([]*model.HostScoringState, error) {
	rows, err := r.pool.Query(ctx, sqlListHostScoring)
	if err != nil {
		return nil, fmt.Errorf("list host scoring state: %w", err)
	}
	defer rows.Close()

	var out []*model.HostScoringState
	for rows.Next() {
		var s model.HostScoringState
		if err := rows.Scan(&s.Host, &s.Score, &s.Signals, &s.WindowMinutes, &s.CalculatedAt); err != nil {
			return nil, fmt.Errorf("scan host scoring state: %w", err)
		}
		out = append(out, &s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate host scoring state: %w", err)
	}
	return out, nil
}
