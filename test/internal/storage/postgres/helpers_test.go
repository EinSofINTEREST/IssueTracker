// 본 패키지는 실제 postgres 에 붙는 통합 테스트입니다 (이슈 #612).
//
// 로컬에 DB 가 없으면 t.Skip 으로 빠져나가되, **CI 에서는 skip 을 금지** 합니다 —
// 가용성 기반 skip 은 로컬 편의를 위한 것이고, CI 에서까지 허용하면 서비스 연결 설정이
// 어긋났을 때 전부 skip 되고 CI 는 초록색이 된다. 게이트가 꺼진 것과 구별되지 않는다.
package postgres_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// requireIntegrationDeps 는 CI 에서 skip 을 금지하는 플래그입니다.
//
// CI 워크플로가 `REQUIRE_INTEGRATION_DEPS=true` 를 주입한다. 이 값이 켜져 있는데 DB 에
// 붙지 못하면 **skip 이 아니라 실패** 다 — 그래야 서비스 설정이 어긋난 것을 알 수 있다.
func requireIntegrationDeps() bool {
	return os.Getenv("REQUIRE_INTEGRATION_DEPS") == "true"
}

// newTestPool 은 테스트용 pgx pool 을 만듭니다. 연결 불가 시 skip (CI 에서는 실패).
func newTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	dsn := fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=%s",
		envOr("POSTGRES_USER", "issuetracker"),
		envOr("POSTGRES_PASSWORD", "issuetracker"),
		envOr("POSTGRES_HOST", "localhost"),
		envOr("POSTGRES_PORT", "5432"),
		envOr("POSTGRES_DB", "issuetracker_test"),
		envOr("POSTGRES_SSLMODE", "disable"),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err == nil {
		err = pool.Ping(ctx)
	}
	if err != nil {
		if requireIntegrationDeps() {
			t.Fatalf("Postgres not available (%v) — REQUIRE_INTEGRATION_DEPS=true 이므로 skip 하지 않는다", err)
		}
		t.Skipf("Postgres not available (%v) — skipping integration test", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// truncateContents 는 테스트 간 격리를 위해 contents 를 비웁니다.
//
// 테스트마다 host 를 고유하게 쓰는 것으로는 부족하다 — 집계가 전체 테이블을 훑으므로
// 다른 테스트가 남긴 row 가 sample_count 에 섞인다.
func truncateContents(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(context.Background(), "TRUNCATE contents CASCADE")
	require.NoError(t, err)
}

// insertContent 는 집계 입력이 될 contents row 를 넣습니다.
//
// publishedAt 이 zero 면 NULL 로 넣는다 — migration 028 이후 nullable 이며, 집계가
// NULL 을 어떻게 다루는지가 검증 대상이다.
func insertContent(
	t *testing.T,
	pool *pgxpool.Pool,
	id, url, category, validationStatus string,
	publishedAt, createdAt time.Time,
) {
	t.Helper()

	const q = `
INSERT INTO contents (id, country, language, title, published_at, category, url,
                      validation_status, created_at)
VALUES ($1, 'KR', 'ko', 'title', $2, $3, $4, $5, $6)
`
	var pub any
	if !publishedAt.IsZero() {
		pub = publishedAt
	}
	_, err := pool.Exec(context.Background(), q,
		id, pub, category, url, validationStatus, createdAt)
	require.NoError(t, err, "contents insert 실패 — 스키마가 예상과 다를 수 있다")
}
