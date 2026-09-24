package postgres_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/storage/model"
	pgstore "issuetracker/internal/storage/postgres"
	"issuetracker/internal/storage/repository"
	"issuetracker/pkg/logger"
)

// 본 파일은 PR #605 (이슈 #383) 가 추가한 priority_config scan 경로를 검증합니다 (이슈 #617).
//
// model.ParsePriorityConfig 자체는 단위 테스트로 덮었으나, GetByHost / List 의 scan 은
// 실행된 적이 없다. 두 메소드가 **파싱 실패를 다르게 처리** 하는데 그 차이가 의도대로
// 동작하는지는 DB 를 거쳐야 확인된다.

func newFetcherRuleRepo(t *testing.T) (repository.FetcherRuleRepository, *pgxpool.Pool) {
	t.Helper()
	pool := newTestPool(t)
	truncateFetcherRules(t, pool)
	t.Cleanup(func() { truncateFetcherRules(t, pool) })

	repo, err := pgstore.NewFetcherRuleRepository(pool, logger.New(logger.DefaultConfig()))
	require.NoError(t, err)
	return repo, pool
}

func truncateFetcherRules(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(context.Background(), "TRUNCATE fetcher_rules CASCADE")
	require.NoError(t, err)
}

// seedRule 은 host 를 등록하고 priority_config 를 직접 씁니다.
//
// Upsert 가 priority_config 를 받지 않으므로 raw SQL 로 넣는다 — scan 경로만 독립적으로
// 검증하기 위함이며, 잘못된 JSON 을 일부러 심을 수도 있다.
func seedRule(t *testing.T, repo repository.FetcherRuleRepository, pool *pgxpool.Pool, host, priorityJSON string) {
	t.Helper()
	require.NoError(t, repo.Upsert(context.Background(), host, model.FetcherGoQuery, "manual"))

	if priorityJSON == "" {
		return
	}
	_, err := pool.Exec(context.Background(),
		"UPDATE fetcher_rules SET priority_config = $1::jsonb WHERE host_pattern = $2",
		priorityJSON, host)
	require.NoError(t, err)
}

// TestFetcherRule_PriorityConfig_Null 은 기존 row (NULL) 가 안전하게 읽히는지 검증합니다.
//
// **대다수 row 가 NULL 이다.** NULL scan 이 실패하면 기존 host 전부의 조회가 깨진다.
func TestFetcherRule_PriorityConfig_Null(t *testing.T) {
	repo, pool := newFetcherRuleRepo(t)
	seedRule(t, repo, pool, "null.example.com", "")

	got, err := repo.GetByHost(context.Background(), "null.example.com")
	require.NoError(t, err)
	assert.Nil(t, got.PriorityConfig, "NULL 은 nil 로 읽혀야 한다")
	assert.Empty(t, got.PriorityConfigError, "NULL 은 오류가 아니다")
}

// TestFetcherRule_PriorityConfig_Valid 는 유효한 설정이 정확히 파싱되는지 검증합니다.
func TestFetcherRule_PriorityConfig_Valid(t *testing.T) {
	repo, pool := newFetcherRuleRepo(t)
	seedRule(t, repo, pool, "valid.example.com", `{
		"base_priority": "high",
		"override_until": "2030-01-02T03:04:05Z",
		"signal_weights": {"host_trust": 1.25},
		"score_threshold": 0.55
	}`)

	got, err := repo.GetByHost(context.Background(), "valid.example.com")
	require.NoError(t, err)
	require.NotNil(t, got.PriorityConfig)

	cfg := got.PriorityConfig
	assert.Equal(t, "high", cfg.BasePriority)
	require.NotNil(t, cfg.OverrideUntil)
	assert.Equal(t, 2030, cfg.OverrideUntil.Year())
	require.NotNil(t, cfg.ScoreThreshold)
	assert.InDelta(t, 0.55, *cfg.ScoreThreshold, 0.0001)
	require.NotNil(t, cfg.SignalWeights)
	require.NotNil(t, cfg.SignalWeights.HostTrust)
	assert.InDelta(t, 1.25, *cfg.SignalWeights.HostTrust, 0.0001)
	assert.Nil(t, cfg.SignalWeights.Freshness, "미지정 weight 는 nil — 기본값을 쓴다는 뜻")
}

// TestFetcherRule_PriorityConfig_PartialThresholdOnly 는 부분 지정이 보존되는지
// 검증합니다 — 운영자가 threshold 만 바꾸는 구성이 성립해야 한다.
func TestFetcherRule_PriorityConfig_PartialThresholdOnly(t *testing.T) {
	repo, pool := newFetcherRuleRepo(t)
	seedRule(t, repo, pool, "partial.example.com", `{"score_threshold": 0.33}`)

	got, err := repo.GetByHost(context.Background(), "partial.example.com")
	require.NoError(t, err)
	require.NotNil(t, got.PriorityConfig)

	assert.Empty(t, got.PriorityConfig.BasePriority)
	assert.Nil(t, got.PriorityConfig.SignalWeights)
	require.NotNil(t, got.PriorityConfig.ScoreThreshold)
	assert.InDelta(t, 0.33, *got.PriorityConfig.ScoreThreshold, 0.0001)
}

// TestFetcherRule_PriorityConfig_GetByHost_InvalidFails 는 단일 조회에서 잘못된 설정이
// **에러** 가 되는지 검증합니다.
//
// 단일 host 조회라 호출자가 진단 가능한 메시지를 받아야 한다.
func TestFetcherRule_PriorityConfig_GetByHost_InvalidFails(t *testing.T) {
	repo, pool := newFetcherRuleRepo(t)
	seedRule(t, repo, pool, "bad.example.com", `{"base_priorty": "high"}`) // 오타 키

	_, err := repo.GetByHost(context.Background(), "bad.example.com")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bad.example.com")
}

// TestFetcherRule_PriorityConfig_List_InvalidIsolated 는 목록 조회에서 한 host 의 잘못된
// 설정이 **전체를 실패시키지 않는지** 검증합니다.
//
// 그리고 그 host 에 **사유가 실리는지** — 사유가 없으면 nil 인 이유가 "설정 없음" 인지
// "설정 오류" 인지 구별되지 않아 조용히 무시되는 설정이 된다. 이 구별을 위해
// PriorityConfigError 필드를 따로 뒀다.
func TestFetcherRule_PriorityConfig_List_InvalidIsolated(t *testing.T) {
	repo, pool := newFetcherRuleRepo(t)

	seedRule(t, repo, pool, "ok.example.com", `{"base_priority": "low"}`)
	seedRule(t, repo, pool, "broken.example.com", `{"signal_weights": {"cost": 1.0}}`) // Sub B 미지원 키
	seedRule(t, repo, pool, "plain.example.com", "")

	all, err := repo.List(context.Background())
	require.NoError(t, err, "한 host 의 잘못된 설정이 목록 전체를 실패시키면 안 된다")
	require.Len(t, all, 3)

	byHost := map[string]*model.FetcherRuleRecord{}
	for _, r := range all {
		byHost[r.HostPattern] = r
	}

	ok := byHost["ok.example.com"]
	require.NotNil(t, ok.PriorityConfig)
	assert.Equal(t, "low", ok.PriorityConfig.BasePriority)
	assert.Empty(t, ok.PriorityConfigError)

	broken := byHost["broken.example.com"]
	assert.Nil(t, broken.PriorityConfig, "파싱 실패 host 는 override 없이 둔다")
	assert.NotEmpty(t, broken.PriorityConfigError,
		"사유가 없으면 '설정 없음' 과 '설정 오류' 가 구별되지 않는다")

	plain := byHost["plain.example.com"]
	assert.Nil(t, plain.PriorityConfig)
	assert.Empty(t, plain.PriorityConfigError, "NULL 은 오류가 아니다")
}
