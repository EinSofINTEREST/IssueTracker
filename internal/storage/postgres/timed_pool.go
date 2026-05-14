package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TimedPool 은 *pgxpool.Pool 을 wrap 하여 모든 외부 호출에 query-level timeout 을 적용합니다 (이슈 #427).
//
// 정책:
//   - Query / Exec / QueryRow / Begin / SendBatch 진입 시 withQueryTimeout(ctx) 적용
//   - 호출자 ctx 가 이미 더 짧은 deadline 보유 시 그대로 통과 (caller-priority)
//   - 패키지 전역 timeout 미설정 (SetQueryTimeout 미호출 또는 0) 시 timeout 미적용 (legacy 호환)
//
// Tx (Begin 결과) 의 내부 메서드는 wrap 하지 않음 — Begin 성공 시점에 이미 connection 이
// 획득되었으므로 Acquire 무한 대기 시나리오가 적용되지 않음. 단일 SQL 의 query timeout 은
// statement_timeout 등 DB 측 정책 영역.
//
// *pgxpool.Pool 을 embed 하여 wrap 하지 않은 다른 메서드 (Ping, Close, Stat, Acquire 직접 호출 등)
// 는 그대로 사용 가능.
type TimedPool struct {
	*pgxpool.Pool
}

// NewTimedPool 은 pgxpool.Pool 을 TimedPool 로 감쌉니다.
//
// 본 함수는 NewPool 의 후처리 단계로만 호출되어야 합니다 (main 의 wiring).
// timeout 정책은 SetQueryTimeout (패키지 전역) 에서 가져옵니다 — 인스턴스별 timeout 이 필요한
// 경우 본 구조체를 확장하세요.
func NewTimedPool(pool *pgxpool.Pool) *TimedPool {
	return &TimedPool{Pool: pool}
}

// Query 는 timeout 을 적용한 후 pgxpool.Pool.Query 를 호출합니다.
func (p *TimedPool) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	ctx, cancel := withQueryTimeout(ctx)
	defer cancel()
	return p.Pool.Query(ctx, sql, args...)
}

// Exec 는 timeout 을 적용한 후 pgxpool.Pool.Exec 를 호출합니다.
func (p *TimedPool) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	ctx, cancel := withQueryTimeout(ctx)
	defer cancel()
	return p.Pool.Exec(ctx, sql, args...)
}

// QueryRow 는 timeout 을 적용한 후 pgxpool.Pool.QueryRow 를 호출합니다.
//
// 주의: pgx v5 의 QueryRow 는 내부적으로 Query 를 즉시 실행하여 첫 row 를 버퍼링한 뒤
// 반환합니다. 따라서 본 메서드 return 직후 cancel() 이 호출되어도 후속 Scan 은 버퍼된 row 를
// 읽으므로 안전합니다.
func (p *TimedPool) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	ctx, cancel := withQueryTimeout(ctx)
	defer cancel()
	return p.Pool.QueryRow(ctx, sql, args...)
}

// Begin 은 timeout 을 적용한 후 pgxpool.Pool.Begin 를 호출합니다.
//
// 반환된 pgx.Tx 자체는 wrap 하지 않음 — Begin 성공 = connection 획득 완료이므로 후속 tx 호출은
// Acquire 무한 대기 시나리오에 해당하지 않습니다.
func (p *TimedPool) Begin(ctx context.Context) (pgx.Tx, error) {
	ctx, cancel := withQueryTimeout(ctx)
	defer cancel()
	return p.Pool.Begin(ctx)
}

// SendBatch 는 timeout 을 적용한 후 pgxpool.Pool.SendBatch 를 호출합니다.
//
// SendBatch 는 pgx.BatchResults 인터페이스를 반환하며, 결과 읽기는 후속 Exec / Query 호출 시점에
// 발생합니다. timeout 은 SendBatch 자체의 Acquire 단계만 cover 하며, 결과 읽기는 호출자 ctx 에
// 의존합니다.
func (p *TimedPool) SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults {
	ctx, cancel := withQueryTimeout(ctx)
	defer cancel()
	return p.Pool.SendBatch(ctx, b)
}
