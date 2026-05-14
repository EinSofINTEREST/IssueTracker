// postgres 패키지는 pgx/v5를 사용한 storage 인터페이스 구현체를 제공합니다.
// pgxpool.Pool을 공유하여 모든 repository 구현체가 연결 풀을 재사용합니다.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"issuetracker/pkg/config"
	"issuetracker/pkg/logger"
)

// queryTimeoutNanos 는 본 패키지 모든 repository 메서드가 적용하는 query-level timeout (이슈 #427).
//
// pgxpool.Acquire 가 MaxConns 고갈 상황에서 무한 대기하는 시나리오 차단용.
// SetQueryTimeout 으로 startup 1회 설정 — atomic.Int64 로 안전한 동시 read.
// 0 이면 timeout 미적용 (legacy 호환).
var queryTimeoutNanos atomic.Int64

// SetQueryTimeout 은 패키지 전역 query timeout 을 설정합니다 (startup 1회).
//
// d <= 0 면 timeout 미적용. NewPool 호출 직후 또는 main 의 wiring 단계에서 1회 호출 권장.
func SetQueryTimeout(d time.Duration) {
	if d < 0 {
		d = 0
	}
	queryTimeoutNanos.Store(int64(d))
}

// withQueryTimeout 은 repository 메서드 진입 시 query timeout 을 적용한 ctx 를 반환합니다 (이슈 #427).
//
// 정책:
//   - 패키지 timeout 미설정 (0) → ctx 그대로 + no-op cancel
//   - 호출자 ctx 가 이미 더 짧은 deadline 보유 → ctx 그대로 + no-op cancel (caller-priority)
//   - 그 외 → context.WithTimeout 적용
//
// 사용 패턴 (모든 repository 메서드 첫 줄):
//
//	ctx, cancel := withQueryTimeout(ctx)
//	defer cancel()
func withQueryTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	d := time.Duration(queryTimeoutNanos.Load())
	if d <= 0 {
		return ctx, func() {}
	}
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < d {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, d)
}

// NewPool은 DBConfig를 기반으로 pgxpool.Pool을 생성하고 TimedPool 로 감쌉니다.
// 연결이 실패하면 즉시 에러를 반환합니다.
//
// 반환 타입 *TimedPool 은 *pgxpool.Pool 을 embed 하므로 기존 메서드 (Ping, Close, Stat 등) 는
// 그대로 호출 가능 + Query/Exec/QueryRow/Begin/SendBatch 는 query-level timeout 적용 (이슈 #427).
func NewPool(ctx context.Context, cfg config.DBConfig, log *logger.Logger) (*TimedPool, error) {
	poolCfg, err := pgxpool.ParseConfig(cfg.DSN())
	if err != nil {
		return nil, fmt.Errorf("parse db config: %w", err)
	}

	poolCfg.MaxConns = cfg.MaxConns
	poolCfg.MinConns = cfg.MinConns
	poolCfg.MaxConnLifetime = 30 * time.Minute
	poolCfg.MaxConnIdleTime = 10 * time.Minute
	poolCfg.HealthCheckPeriod = 1 * time.Minute

	// 초기 연결 타임아웃 적용
	connectCtx, cancel := context.WithTimeout(ctx, cfg.ConnTimeout)
	defer cancel()

	pool, err := pgxpool.NewWithConfig(connectCtx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}

	// 연결 확인 (DB가 실제로 응답하는지)
	if err := pool.Ping(connectCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping db: %w", err)
	}

	log.WithFields(map[string]interface{}{
		"host":      cfg.Host,
		"port":      cfg.Port,
		"database":  cfg.Database,
		"max_conns": cfg.MaxConns,
	}).Info("postgresql pool connected")

	return NewTimedPool(pool), nil
}

// ─────────────────────────────────────────────────────────────────────────────
// 패키지 공통 헬퍼 함수
// ─────────────────────────────────────────────────────────────────────────────

// isPgUniqueViolation은 pgconn.PgError가 유일성 제약 위반(23505)인지 확인합니다.
func isPgUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// mustMarshalJSON은 값을 JSON 바이트로 변환합니다.
// 변환 실패는 개발 오류이므로 빈 객체({})를 반환합니다.
func mustMarshalJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte("{}")
	}
	return b
}
