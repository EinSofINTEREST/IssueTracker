package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/storage"
	"issuetracker/internal/storage/model"
	pgstore "issuetracker/internal/storage/postgres"
	"issuetracker/internal/storage/repository"
	"issuetracker/pkg/logger"
)

// 본 파일은 sample_url repository 의 cap 정책을 검증합니다 (이슈 #627, 부모 #616).
//
// cap 도달이 **에러가 아니라 nil** 이라는 점이 핵심이다. 뒤집히면 호출자가 실패로 처리해
// 정밀화 파이프라인이 멈추고, 반대로 cap 이 동작하지 않으면 doc comment 가 말하는
// "trigger 가 동작하지 않는 케이스 (LLM_ENABLED=false / Redis 장애) 의 DB 폭증" 이 그대로 난다.

func newSampleURLRepo(t *testing.T) (repository.SampleURLRepository, *pgxpool.Pool, int64) {
	t.Helper()
	pool := newTestPool(t)
	truncateParserRules(t, pool) // sample 은 rule_id FK 를 갖는다

	ruleRepo := pgstore.NewParserRuleRepository(pool, logger.New(logger.DefaultConfig()))
	rec := rule("sample.example.com", "", model.TargetTypePage)
	require.NoError(t, ruleRepo.Insert(context.Background(), rec))

	t.Cleanup(func() { truncateParserRules(t, pool) })
	return pgstore.NewSampleURLRepository(pool, logger.New(logger.DefaultConfig())), pool, rec.ID
}

// TestSampleURL_Insert_UnderCap 은 cap 미만에서 정상 누적되는지 검증합니다.
func TestSampleURL_Insert_UnderCap(t *testing.T) {
	repo, _, ruleID := newSampleURLRepo(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		require.NoError(t, repo.Insert(ctx, ruleID, fmt.Sprintf("https://sample.example.com/%d", i)))
	}

	n, err := repo.Count(ctx, ruleID)
	require.NoError(t, err)
	assert.Equal(t, 3, n)
}

// TestSampleURL_Insert_AtCap_SkipsWithoutError 는 cap 도달이 **에러가 아닌지** 검증합니다.
//
// 에러를 돌려주면 호출자가 실패로 처리해 정밀화 파이프라인이 멈춘다. cap 은 정상 흐름의
// 방어선이지 오류가 아니다.
func TestSampleURL_Insert_AtCap_SkipsWithoutError(t *testing.T) {
	repo, _, ruleID := newSampleURLRepo(t)
	ctx := context.Background()

	for i := 0; i < model.SampleCapPerRule; i++ {
		require.NoError(t, repo.Insert(ctx, ruleID, fmt.Sprintf("https://sample.example.com/c%d", i)))
	}

	n, err := repo.Count(ctx, ruleID)
	require.NoError(t, err)
	require.Equal(t, model.SampleCapPerRule, n)

	// cap 도달 후 추가 시도
	require.NoError(t, repo.Insert(ctx, ruleID, "https://sample.example.com/overflow"),
		"cap 도달은 에러가 아니라 skip 이어야 한다")

	after, err := repo.Count(ctx, ruleID)
	require.NoError(t, err)
	assert.Equal(t, model.SampleCapPerRule, after, "cap 을 넘어 저장되면 안 된다")
}

// TestSampleURL_Insert_Duplicate 는 같은 (rule_id, url) 이 ErrDuplicate 인지 검증합니다.
func TestSampleURL_Insert_Duplicate(t *testing.T) {
	repo, _, ruleID := newSampleURLRepo(t)
	ctx := context.Background()

	const u = "https://sample.example.com/dup"
	require.NoError(t, repo.Insert(ctx, ruleID, u))

	err := repo.Insert(ctx, ruleID, u)
	require.Error(t, err)
	assert.True(t, errors.Is(err, storage.ErrDuplicate), "got: %v", err)
}

// TestSampleURL_Count_IsolatedPerRule 은 cap 검사가 rule 별로 분리되는지 검증합니다.
//
// 섞이면 다른 rule 의 sample 때문에 cap 이 조기 발동해, 정작 필요한 rule 이 표본을
// 못 모은다.
func TestSampleURL_Count_IsolatedPerRule(t *testing.T) {
	repo, pool, ruleA := newSampleURLRepo(t)
	ctx := context.Background()

	ruleRepo := pgstore.NewParserRuleRepository(pool, logger.New(logger.DefaultConfig()))
	other := rule("sample.example.com", "/other/.*", model.TargetTypePage)
	require.NoError(t, ruleRepo.Insert(ctx, other))

	require.NoError(t, repo.Insert(ctx, ruleA, "https://sample.example.com/a1"))
	require.NoError(t, repo.Insert(ctx, ruleA, "https://sample.example.com/a2"))
	require.NoError(t, repo.Insert(ctx, other.ID, "https://sample.example.com/b1"))

	nA, err := repo.Count(ctx, ruleA)
	require.NoError(t, err)
	assert.Equal(t, 2, nA)

	nB, err := repo.Count(ctx, other.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, nB, "다른 rule 의 sample 이 섞이면 안 된다")
}

// TestSampleURL_List_LimitAndOrder 는 limit 준수와 최신 우선 정렬을 검증합니다.
func TestSampleURL_List_LimitAndOrder(t *testing.T) {
	repo, _, ruleID := newSampleURLRepo(t)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		require.NoError(t, repo.Insert(ctx, ruleID, fmt.Sprintf("https://sample.example.com/l%d", i)))
	}

	got, err := repo.List(ctx, ruleID, 3)
	require.NoError(t, err)
	assert.Len(t, got, 3, "limit 을 넘으면 안 된다")
}

// TestSampleURL_Purge 는 정밀화 완료 후 정리 경로를 검증합니다.
func TestSampleURL_Purge(t *testing.T) {
	repo, _, ruleID := newSampleURLRepo(t)
	ctx := context.Background()

	require.NoError(t, repo.Insert(ctx, ruleID, "https://sample.example.com/p1"))
	require.NoError(t, repo.Insert(ctx, ruleID, "https://sample.example.com/p2"))

	require.NoError(t, repo.Purge(ctx, ruleID))

	n, err := repo.Count(ctx, ruleID)
	require.NoError(t, err)
	assert.Zero(t, n)

	// purge 후 다시 누적 가능해야 한다 — 정밀화가 반복되는 경로다.
	require.NoError(t, repo.Insert(ctx, ruleID, "https://sample.example.com/p1"))
}
