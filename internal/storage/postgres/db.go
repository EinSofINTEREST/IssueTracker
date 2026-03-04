// postgres 패키지는 pgx/v5를 사용한 storage 인터페이스 구현체를 제공합니다.
// pgxpool.Pool을 공유하여 모든 repository 구현체가 연결 풀을 재사용합니다.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"issuetracker/pkg/config"
	"issuetracker/pkg/logger"
)

// NewPool은 DBConfig를 기반으로 pgxpool.Pool을 생성하고 연결을 확인합니다.
// 연결이 실패하면 즉시 에러를 반환합니다.
func NewPool(ctx context.Context, cfg config.DBConfig, log *logger.Logger) (*pgxpool.Pool, error) {
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

	return pool, nil
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
