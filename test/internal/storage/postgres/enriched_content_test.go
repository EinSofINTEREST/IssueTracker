package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/processor/fetcher/core"
	"issuetracker/internal/storage/model"
	pgstore "issuetracker/internal/storage/postgres"
	"issuetracker/internal/storage/repository"
	"issuetracker/pkg/logger"
)

// 본 파일은 enriched_content 를 검증합니다 (이슈 #631, 부모 #616).
//
// 판단 지점이 1개로 사실상 CRUD 다. 다만 enrich 4단계의 결과가 모이는 곳이라 왕복 자체는
// 확인할 가치가 있다 — JSONB 컬럼이 4개고, content_id FK 로 contents 에 묶인다.

func newEnrichedContentRepo(t *testing.T) (repository.EnrichedContentRepository, *pgxpool.Pool) {
	t.Helper()
	pool := newTestPool(t)
	truncateContents(t, pool) // enriched_contents 는 content_id FK — CASCADE 로 함께 비워진다
	t.Cleanup(func() { truncateContents(t, pool) })
	return pgstore.NewEnrichedContentRepository(pool, logger.New(logger.DefaultConfig())), pool
}

// seedContent 는 FK 대상 contents row 를 만듭니다.
//
// 최소 필드만 채운다 — 본 파일의 관심사는 enriched_contents 이고, contents 는 FK 를
// 만족시키기 위한 것이다.
func seedContent(t *testing.T, pool *pgxpool.Pool, id string) {
	t.Helper()
	contentRepo := pgstore.NewContentRepository(pool, logger.New(logger.DefaultConfig()))
	require.NoError(t, contentRepo.Save(context.Background(), &core.Content{
		ID:          id,
		SourceType:  core.SourceTypeNews,
		Country:     "KR",
		Language:    "ko",
		Title:       "title " + id,
		PublishedAt: time.Now().UTC().Add(-time.Hour),
		URL:         "https://enr.example.com/" + id,
		ContentHash: "hash-" + id,
	}))
}

func enriched(contentID string, score float64) *model.EnrichedContentRecord {
	return &model.EnrichedContentRecord{
		ContentID:     contentID,
		TrustScore:    score,
		Facts:         []byte(`[{"claim":"c1"}]`),
		Verifications: []byte(`[{"verdict":"supported"}]`),
		Context:       []byte(`{"background":"b"}`),
		Rationale:     "scorer rationale",
		Factors:       []byte(`{"claim_support_ratio":0.8}`),
		EnrichedAt:    time.Now().UTC(),
	}
}

// TestEnrichedContent_Upsert_Roundtrip 은 4개 JSONB 컬럼이 온전히 왕복하는지
// 검증합니다.
func TestEnrichedContent_Upsert_Roundtrip(t *testing.T) {
	repo, pool := newEnrichedContentRepo(t)
	ctx := context.Background()

	seedContent(t, pool, "enr-1")
	require.NoError(t, repo.Upsert(ctx, enriched("enr-1", 0.75)))

	got, err := repo.GetByContentID(ctx, "enr-1")
	require.NoError(t, err)

	assert.InDelta(t, 0.75, got.TrustScore, 0.0001)
	assert.JSONEq(t, `[{"claim":"c1"}]`, string(got.Facts))
	assert.JSONEq(t, `[{"verdict":"supported"}]`, string(got.Verifications))
	assert.JSONEq(t, `{"background":"b"}`, string(got.Context))
	assert.Equal(t, "scorer rationale", got.Rationale, "이슈 #457 — rationale 컬럼")
	assert.JSONEq(t, `{"claim_support_ratio":0.8}`, string(got.Factors))
}

// TestEnrichedContent_Upsert_Overwrites 는 재enrich 가 덮어쓰는지 검증합니다.
//
// 누적되면 같은 content 에 대한 과거 점수가 남아 조회가 어느 것을 돌려줄지 불확실해진다.
func TestEnrichedContent_Upsert_Overwrites(t *testing.T) {
	repo, pool := newEnrichedContentRepo(t)
	ctx := context.Background()

	seedContent(t, pool, "enr-2")
	require.NoError(t, repo.Upsert(ctx, enriched("enr-2", 0.3)))

	second := enriched("enr-2", 0.9)
	second.Rationale = "re-scored"
	require.NoError(t, repo.Upsert(ctx, second))

	got, err := repo.GetByContentID(ctx, "enr-2")
	require.NoError(t, err)
	assert.InDelta(t, 0.9, got.TrustScore, 0.0001, "재enrich 가 덮어써야 한다")
	assert.Equal(t, "re-scored", got.Rationale)

	var n int
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT COUNT(*) FROM enriched_contents WHERE content_id = $1", "enr-2").Scan(&n))
	assert.Equal(t, 1, n, "row 가 누적되면 안 된다")
}
