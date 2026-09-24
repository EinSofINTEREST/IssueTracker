package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/storage"
	"issuetracker/internal/storage/model"
	pgstore "issuetracker/internal/storage/postgres"
	"issuetracker/internal/storage/repository"
	"issuetracker/pkg/logger"
)

// 본 파일은 이슈 #382 의 host_scoring repository 를 실제 postgres 에서 검증합니다 (이슈 #614).
//
// 집계 SQL 은 이슈 #612 에서 덮었으나 repository 는 남아 있었다. Upsert 의 ON CONFLICT,
// 빈 signals 보정, CHECK 제약 — 모두 추론으로만 넣은 것들이다.

func newScoringRepo(t *testing.T) (repository.HostScoringRepository, *pgxpool.Pool) {
	t.Helper()
	pool := newTestPool(t)
	truncateHostScoring(t, pool)
	t.Cleanup(func() { truncateHostScoring(t, pool) })
	return pgstore.NewHostScoringRepository(pool, logger.New(logger.DefaultConfig())), pool
}

func truncateHostScoring(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(context.Background(), "TRUNCATE host_scoring_state")
	require.NoError(t, err)
}

func state(host string, score float64, signals string, window int) *model.HostScoringState {
	s := &model.HostScoringState{Host: host, Score: score, WindowMinutes: window}
	if signals != "" {
		s.Signals = json.RawMessage(signals)
	}
	return s
}

// TestHostScoring_Upsert_InsertThenUpdate 는 같은 host 재계산이 덮어쓰기인지 검증합니다.
//
// scorer 가 주기마다 같은 host 를 다시 쓴다. upsert 가 아니면 PK 충돌로 매 주기 실패하는데,
// 그 실패는 host 단위 WARN 으로만 남아 조용히 누적된다.
func TestHostScoring_Upsert_InsertThenUpdate(t *testing.T) {
	repo, _ := newScoringRepo(t)
	ctx := context.Background()

	require.NoError(t, repo.Upsert(ctx, state("a.example.com", 0.3, `{"freshness":0.1}`, 60)))

	first, err := repo.Get(ctx, "a.example.com")
	require.NoError(t, err)
	assert.InDelta(t, 0.3, first.Score, 0.0001)

	require.NoError(t, repo.Upsert(ctx, state("a.example.com", 0.9, `{"freshness":0.9}`, 120)))

	second, err := repo.Get(ctx, "a.example.com")
	require.NoError(t, err)
	assert.InDelta(t, 0.9, second.Score, 0.0001, "재계산이 덮어써야 한다")
	assert.Equal(t, 120, second.WindowMinutes)
	assert.JSONEq(t, `{"freshness":0.9}`, string(second.Signals))
}

// TestHostScoring_Upsert_EmptySignals 는 빈 signals 가 `{}` 로 보정되는지 검증합니다.
//
// signals 는 NOT NULL JSONB 다. 보정이 없으면 제약 위반인데, 그 위반이 실제로 나는지
// 확인한 적이 없었다 — 추론으로 넣은 가드다.
func TestHostScoring_Upsert_EmptySignals(t *testing.T) {
	repo, _ := newScoringRepo(t)
	ctx := context.Background()

	require.NoError(t, repo.Upsert(ctx, state("empty.example.com", 0.5, "", 60)),
		"빈 signals 가 NOT NULL 위반을 일으키면 안 된다")

	got, err := repo.Get(ctx, "empty.example.com")
	require.NoError(t, err)
	assert.JSONEq(t, `{}`, string(got.Signals))
}

// TestHostScoring_Upsert_CheckConstraints 는 DB CHECK 제약이 실제로 막는지 검증합니다.
//
// 계산 버그로 범위 밖 점수가 와도 저장되면 resolver 가 그대로 쓴다. migration 034 의
// CHECK 가 마지막 방어선이다.
func TestHostScoring_Upsert_CheckConstraints(t *testing.T) {
	repo, _ := newScoringRepo(t)
	ctx := context.Background()

	tests := []struct {
		name string
		st   *model.HostScoringState
	}{
		{"score 1 초과", state("bad1.example.com", 1.5, `{}`, 60)},
		{"score 음수", state("bad2.example.com", -0.1, `{}`, 60)},
		{"window 0", state("bad3.example.com", 0.5, `{}`, 0)},
		{"window 음수", state("bad4.example.com", 0.5, `{}`, -10)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Error(t, repo.Upsert(ctx, tt.st), "CHECK 제약이 막아야 한다")
		})
	}
}

// TestHostScoring_Upsert_BoundaryScores 는 경계값이 허용되는지 검증합니다.
func TestHostScoring_Upsert_BoundaryScores(t *testing.T) {
	repo, _ := newScoringRepo(t)
	ctx := context.Background()

	for _, sc := range []float64{0, 1} {
		require.NoError(t, repo.Upsert(ctx, state("edge.example.com", sc, `{}`, 1)),
			"score=%v 는 허용돼야 한다", sc)
	}
}

// TestHostScoring_Upsert_InvalidInput 은 프로그래밍 오류를 DB 까지 보내지 않는지 검증합니다.
func TestHostScoring_Upsert_InvalidInput(t *testing.T) {
	repo, _ := newScoringRepo(t)
	ctx := context.Background()

	require.Error(t, repo.Upsert(ctx, nil))
	require.Error(t, repo.Upsert(ctx, state("", 0.5, `{}`, 60)))
}

// TestHostScoring_Get_NotFound 는 없는 host 가 storage.ErrNotFound 로 매핑되는지
// 검증합니다 — 호출자가 errors.Is 로 분기한다.
func TestHostScoring_Get_NotFound(t *testing.T) {
	repo, _ := newScoringRepo(t)

	_, err := repo.Get(context.Background(), "absent.example.com")
	require.Error(t, err)
	assert.True(t, errors.Is(err, storage.ErrNotFound),
		"pgx.ErrNoRows 가 storage.ErrNotFound 로 매핑돼야 한다, got: %v", err)
}

// TestHostScoring_ListAll 은 전량 조회를 검증합니다 — resolver 스냅샷 hydrate 경로다.
func TestHostScoring_ListAll(t *testing.T) {
	repo, _ := newScoringRepo(t)
	ctx := context.Background()

	empty, err := repo.ListAll(ctx)
	require.NoError(t, err)
	assert.Empty(t, empty)

	for _, h := range []string{"l1.example.com", "l2.example.com", "l3.example.com"} {
		require.NoError(t, repo.Upsert(ctx, state(h, 0.5, `{"impact":0.5}`, 60)))
	}

	all, err := repo.ListAll(ctx)
	require.NoError(t, err)
	assert.Len(t, all, 3)

	seen := map[string]bool{}
	for _, s := range all {
		seen[s.Host] = true
		assert.InDelta(t, 0.5, s.Score, 0.0001)
		assert.NotEmpty(t, s.Signals)
	}
	assert.Len(t, seen, 3)
}

// TestHostScoring_CalculatedAt_UsesDBClock 은 calculated_at 이 갱신마다 전진하는지
// 검증합니다.
//
// DB NOW() 를 쓰는 이유는 여러 인스턴스의 시계가 어긋나도 stale 판정이 한 기준으로
// 이뤄지게 하기 위함이다. 클라이언트 시각이 들어가면 그 전제가 깨진다.
func TestHostScoring_CalculatedAt_UsesDBClock(t *testing.T) {
	repo, _ := newScoringRepo(t)
	ctx := context.Background()

	require.NoError(t, repo.Upsert(ctx, state("clock.example.com", 0.4, `{}`, 60)))
	first, err := repo.Get(ctx, "clock.example.com")
	require.NoError(t, err)
	assert.False(t, first.CalculatedAt.IsZero(), "DB 가 시각을 채워야 한다")

	time.Sleep(10 * time.Millisecond)
	require.NoError(t, repo.Upsert(ctx, state("clock.example.com", 0.6, `{}`, 60)))
	second, err := repo.Get(ctx, "clock.example.com")
	require.NoError(t, err)

	assert.True(t, second.CalculatedAt.After(first.CalculatedAt),
		"재계산 시 calculated_at 이 전진해야 한다")
}
