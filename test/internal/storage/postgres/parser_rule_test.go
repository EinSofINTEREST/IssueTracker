package postgres_test

import (
	"context"
	"errors"
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

// 본 파일은 parser_rule repository 의 판단이 얽힌 지점을 검증합니다 (이슈 #623, 부모 #616).
//
// LLM 룰 자동 생성 경로 (이슈 #149) 가 여기에 의존한다. 특히 ErrDuplicate 는 llmgen 이
// **정상 경로로 흡수** 하므로 (이미 존재하는 룰을 재확인한 것으로 보고 진행), 매핑이 깨지면
// 일반 에러로 떨어져 룰 생성이 실패로 기록되고 재시도가 돈다.

func newParserRuleRepo(t *testing.T) (repository.ParserRuleRepository, *pgxpool.Pool) {
	t.Helper()
	pool := newTestPool(t)
	truncateParserRules(t, pool)
	t.Cleanup(func() { truncateParserRules(t, pool) })
	return pgstore.NewParserRuleRepository(pool, logger.New(logger.DefaultConfig())), pool
}

func truncateParserRules(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(context.Background(), "TRUNCATE parser_rules CASCADE")
	require.NoError(t, err)
}

func rule(host, path string, tt model.TargetType) *model.ParserRuleRecord {
	return &model.ParserRuleRecord{
		SourceName:  "test",
		HostPattern: host,
		PathPattern: path,
		TargetType:  tt,
		Enabled:     true,
		Selectors:   model.SelectorMap{Title: &model.FieldSelector{CSS: "h1"}},
		Description: "integration test",
	}
}

// ── Insert ──────────────────────────────────────────────────────────────────

// TestParserRule_Insert_Basic 은 정상 INSERT 가 ID / 타임스탬프를 채우는지 검증합니다.
func TestParserRule_Insert_Basic(t *testing.T) {
	repo, _ := newParserRuleRepo(t)

	rec := rule("a.example.com", "", model.TargetTypePage)
	require.NoError(t, repo.Insert(context.Background(), rec))

	assert.NotZero(t, rec.ID, "Insert 가 생성된 ID 를 rec 에 채워야 한다")
	assert.False(t, rec.CreatedAt.IsZero())
	assert.Equal(t, 1, rec.Version, "Version 0 은 1 로 보정된다")
}

// TestParserRule_Insert_Duplicate 는 같은 자연키 재INSERT 가 storage.ErrDuplicate 로
// 매핑되는지 검증합니다.
//
// **llmgen 이 이 에러를 정상 경로로 흡수한다.** 매핑이 깨지면 일반 에러로 떨어져 룰 생성이
// 실패로 기록되고 재시도가 돈다.
func TestParserRule_Insert_Duplicate(t *testing.T) {
	repo, _ := newParserRuleRepo(t)
	ctx := context.Background()

	require.NoError(t, repo.Insert(ctx, rule("dup.example.com", "", model.TargetTypePage)))

	err := repo.Insert(ctx, rule("dup.example.com", "", model.TargetTypePage))
	require.Error(t, err)
	assert.True(t, errors.Is(err, storage.ErrDuplicate),
		"UNIQUE 위반이 storage.ErrDuplicate 로 매핑돼야 한다, got: %v", err)
}

// TestParserRule_Insert_InvalidPathPattern 은 RE2 컴파일 실패가 DB 도달 전에
// 거부되는지 검증합니다.
//
// 잘못된 regex 가 저장되면 resolver 가 매 조회마다 컴파일에 실패한다.
func TestParserRule_Insert_InvalidPathPattern(t *testing.T) {
	repo, _ := newParserRuleRepo(t)

	err := repo.Insert(context.Background(), rule("bad.example.com", "/article/[", model.TargetTypePage))
	require.Error(t, err)
	assert.True(t, errors.Is(err, storage.ErrInvalid),
		"잘못된 regex 는 storage.ErrInvalid 여야 한다, got: %v", err)
}

// TestParserRule_Insert_CrawlPriorityNormalized 는 범위 밖 우선순위가 보정되고
// **rec 에도 반영** 되는지 검증합니다.
//
// 호출자가 Insert 후 rec.CrawlPriority 를 참조할 수 있으므로, in-memory 와 DB row 의 값이
// 일치해야 한다.
func TestParserRule_Insert_CrawlPriorityNormalized(t *testing.T) {
	repo, _ := newParserRuleRepo(t)
	ctx := context.Background()

	for i, p := range []int16{0, 4, -1} {
		rec := rule("prio.example.com", "/p"+string(rune('a'+i)), model.TargetTypePage)
		rec.CrawlPriority = p
		require.NoError(t, repo.Insert(ctx, rec))

		assert.Equal(t, int16(2), rec.CrawlPriority, "범위 밖(%d) 은 normal(2) 로 보정", p)

		stored, err := repo.GetByID(ctx, rec.ID)
		require.NoError(t, err)
		assert.Equal(t, int16(2), stored.CrawlPriority, "DB 값도 보정된 값이어야 한다")
	}
}

// TestParserRule_Insert_ValidPriorityPreserved 는 유효한 값이 그대로 저장되는지 검증합니다.
func TestParserRule_Insert_ValidPriorityPreserved(t *testing.T) {
	repo, _ := newParserRuleRepo(t)
	ctx := context.Background()

	for i, p := range []int16{1, 2, 3} {
		rec := rule("keep.example.com", "/k"+string(rune('a'+i)), model.TargetTypePage)
		rec.CrawlPriority = p
		require.NoError(t, repo.Insert(ctx, rec))

		stored, err := repo.GetByID(ctx, rec.ID)
		require.NoError(t, err)
		assert.Equal(t, p, stored.CrawlPriority)
	}
}

// ── InsertNextVersion ───────────────────────────────────────────────────────

// TestParserRule_InsertNextVersion_CreatesV2 는 stale 재학습이 기존 버전을 보존한 채
// 다음 버전을 만드는지 검증합니다.
//
// 버전 계산이 틀리면 중복 INSERT (ErrDuplicate) 가 나거나 버전을 건너뛴다.
func TestParserRule_InsertNextVersion_CreatesV2(t *testing.T) {
	repo, _ := newParserRuleRepo(t)
	ctx := context.Background()

	v1 := rule("ver.example.com", "", model.TargetTypePage)
	require.NoError(t, repo.Insert(ctx, v1))
	require.Equal(t, 1, v1.Version)

	v2 := rule("ver.example.com", "", model.TargetTypePage)
	require.NoError(t, repo.InsertNextVersion(ctx, v2))
	assert.Equal(t, 2, v2.Version)

	// v1 이 보존됐는지 — 재학습은 덮어쓰기가 아니다.
	stored, err := repo.GetByID(ctx, v1.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, stored.Version)
}

// TestParserRule_InsertNextVersion_NoExisting 은 룰이 없는 자연키에서 v1 을 만드는지
// 검증합니다.
func TestParserRule_InsertNextVersion_NoExisting(t *testing.T) {
	repo, _ := newParserRuleRepo(t)

	rec := rule("fresh.example.com", "", model.TargetTypePage)
	require.NoError(t, repo.InsertNextVersion(context.Background(), rec))
	assert.Equal(t, 1, rec.Version, "기존 룰이 없으면 v1")
}

// TestParserRule_InsertNextVersion_Consecutive 는 연속 호출이 버전을 건너뛰지 않는지
// 검증합니다.
func TestParserRule_InsertNextVersion_Consecutive(t *testing.T) {
	repo, _ := newParserRuleRepo(t)
	ctx := context.Background()

	for want := 1; want <= 3; want++ {
		rec := rule("seq.example.com", "", model.TargetTypePage)
		require.NoError(t, repo.InsertNextVersion(ctx, rec))
		assert.Equal(t, want, rec.Version)
	}
}

// ── HasAnyRule ──────────────────────────────────────────────────────────────

// TestParserRule_HasAnyRule 은 존재 여부와 enabled 여부를 함께 산출하는지 검증합니다.
//
// disabled 만 있는 경우를 "룰 없음" 으로 보면 llmgen 이 이미 있는 host 에 룰을 또
// 생성한다 — 운영자가 일부러 꺼 둔 룰을 무시하는 셈이다.
func TestParserRule_HasAnyRule(t *testing.T) {
	repo, _ := newParserRuleRepo(t)
	ctx := context.Background()

	exists, hasEnabled, err := repo.HasAnyRule(ctx, "none.example.com", model.TargetTypePage)
	require.NoError(t, err)
	assert.False(t, exists, "룰이 없으면 exists=false")
	assert.False(t, hasEnabled)

	off := rule("off.example.com", "", model.TargetTypePage)
	off.Enabled = false
	require.NoError(t, repo.Insert(ctx, off))

	exists, hasEnabled, err = repo.HasAnyRule(ctx, "off.example.com", model.TargetTypePage)
	require.NoError(t, err)
	assert.True(t, exists, "disabled 룰도 '존재함' 이다")
	assert.False(t, hasEnabled)

	on := rule("on.example.com", "", model.TargetTypePage)
	require.NoError(t, repo.Insert(ctx, on))

	exists, hasEnabled, err = repo.HasAnyRule(ctx, "on.example.com", model.TargetTypePage)
	require.NoError(t, err)
	assert.True(t, exists)
	assert.True(t, hasEnabled)
}

// ── FindActiveCandidates ────────────────────────────────────────────────────

// TestParserRule_FindActiveCandidates_Ordering 은 정렬 규칙을 검증합니다.
//
// **catch-all (빈 path_pattern) 이 specific 을 가리면 안 된다.** 긴 패턴이 먼저 와야
// application 측 매칭이 더 구체적인 룰을 고른다.
func TestParserRule_FindActiveCandidates_Ordering(t *testing.T) {
	repo, _ := newParserRuleRepo(t)
	ctx := context.Background()
	host := "order.example.com"

	// 일부러 catch-all 을 먼저 넣는다 — 삽입 순서가 결과를 좌우하면 안 된다.
	require.NoError(t, repo.Insert(ctx, rule(host, "", model.TargetTypePage)))
	require.NoError(t, repo.Insert(ctx, rule(host, "/article/sports/.*", model.TargetTypePage)))
	require.NoError(t, repo.Insert(ctx, rule(host, "/article/.*", model.TargetTypePage)))

	got, err := repo.FindActiveCandidates(ctx, host, model.TargetTypePage)
	require.NoError(t, err)
	require.Len(t, got, 3)

	assert.Equal(t, "/article/sports/.*", got[0].PathPattern, "가장 긴 패턴이 먼저")
	assert.Equal(t, "/article/.*", got[1].PathPattern)
	assert.Empty(t, got[2].PathPattern, "catch-all 은 마지막")
}

// TestParserRule_FindActiveCandidates_VersionOrder 는 같은 패턴 안에서 최신 버전이
// 먼저 오는지 검증합니다 — stale 재학습의 v2 가 v1 보다 우선해야 한다.
func TestParserRule_FindActiveCandidates_VersionOrder(t *testing.T) {
	repo, _ := newParserRuleRepo(t)
	ctx := context.Background()
	host := "vorder.example.com"

	v1 := rule(host, "/a/.*", model.TargetTypePage)
	require.NoError(t, repo.Insert(ctx, v1))
	v2 := rule(host, "/a/.*", model.TargetTypePage)
	require.NoError(t, repo.InsertNextVersion(ctx, v2))

	got, err := repo.FindActiveCandidates(ctx, host, model.TargetTypePage)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, 2, got[0].Version, "최신 버전이 먼저")
	assert.Equal(t, 1, got[1].Version)
}

// TestParserRule_FindActiveCandidates_ExcludesDisabled 는 비활성 룰이 제외되는지
// 검증합니다.
func TestParserRule_FindActiveCandidates_ExcludesDisabled(t *testing.T) {
	repo, _ := newParserRuleRepo(t)
	ctx := context.Background()
	host := "filter.example.com"

	off := rule(host, "/off/.*", model.TargetTypePage)
	off.Enabled = false
	require.NoError(t, repo.Insert(ctx, off))
	require.NoError(t, repo.Insert(ctx, rule(host, "/on/.*", model.TargetTypePage)))

	got, err := repo.FindActiveCandidates(ctx, host, model.TargetTypePage)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "/on/.*", got[0].PathPattern)
}

// TestParserRule_FindActive_NotFound 는 활성 룰이 없을 때 ErrNotFound 인지 검증합니다.
func TestParserRule_FindActive_NotFound(t *testing.T) {
	repo, _ := newParserRuleRepo(t)

	_, err := repo.FindActive(context.Background(), "absent.example.com", model.TargetTypePage)
	require.Error(t, err)
	assert.True(t, errors.Is(err, storage.ErrNotFound), "got: %v", err)
}
