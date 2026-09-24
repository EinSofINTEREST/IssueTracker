package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/storage/model"
	pgstore "issuetracker/internal/storage/postgres"
	"issuetracker/internal/storage/repository"
	"issuetracker/pkg/logger"
)

// 본 파일은 scheduler_entry repository 의 ListEnabled 분기를 검증합니다 (이슈 #627, 부모 #616).
//
// **빈 category 가 "전체"** 라는 규칙이 두 SQL 로 갈려 있어, 한쪽만 고치면 조용히 갈린다.
// scheduler 가 부팅 시 이 결과로 시드를 spawn 하므로 — disabled 가 새면 꺼 둔 시드가 돌고,
// category 필터가 틀리면 엉뚱한 시드가 돈다.

func newSchedulerEntryRepo(t *testing.T) (repository.SchedulerEntryRepository, *pgxpool.Pool) {
	t.Helper()
	pool := newTestPool(t)
	truncateSchedulerEntries(t, pool)
	t.Cleanup(func() { truncateSchedulerEntries(t, pool) })
	repo, err := pgstore.NewSchedulerEntryRepository(pool, logger.New(logger.DefaultConfig()))
	require.NoError(t, err)
	return repo, pool
}

func truncateSchedulerEntries(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(context.Background(), "TRUNCATE scheduler_entries CASCADE")
	require.NoError(t, err)
}

func entry(cat model.SchedulerCategory, name, url string, enabled bool) *model.SchedulerEntryRecord {
	return &model.SchedulerEntryRecord{
		Category:   cat,
		SourceName: name,
		URL:        url,
		TargetType: "category",
		Interval:   30 * time.Minute,
		Priority:   2,
		Enabled:    enabled,
	}
}

// TestSchedulerEntry_ListEnabled_EmptyCategoryReturnsAll 은 빈 category 가 "전체" 로
// 동작하는지 검증합니다.
func TestSchedulerEntry_ListEnabled_EmptyCategoryReturnsAll(t *testing.T) {
	repo, _ := newSchedulerEntryRepo(t)
	ctx := context.Background()

	require.NoError(t, repo.Insert(ctx, entry(model.SchedulerCategoryNews, "n1", "https://n1.example.com", true)))
	require.NoError(t, repo.Insert(ctx, entry(model.SchedulerCategoryCommunity, "c1", "https://c1.example.com", true)))
	require.NoError(t, repo.Insert(ctx, entry(model.SchedulerCategorySearch, "s1", "https://s1.example.com", true)))

	got, err := repo.ListEnabled(ctx, "")
	require.NoError(t, err)
	assert.Len(t, got, 3, "빈 category 는 전체를 반환해야 한다")
}

// TestSchedulerEntry_ListEnabled_FiltersByCategory 는 category 필터를 검증합니다.
func TestSchedulerEntry_ListEnabled_FiltersByCategory(t *testing.T) {
	repo, _ := newSchedulerEntryRepo(t)
	ctx := context.Background()

	require.NoError(t, repo.Insert(ctx, entry(model.SchedulerCategoryNews, "n1", "https://n1.example.com", true)))
	require.NoError(t, repo.Insert(ctx, entry(model.SchedulerCategoryNews, "n2", "https://n2.example.com", true)))
	require.NoError(t, repo.Insert(ctx, entry(model.SchedulerCategoryCommunity, "c1", "https://c1.example.com", true)))

	got, err := repo.ListEnabled(ctx, model.SchedulerCategoryNews)
	require.NoError(t, err)
	require.Len(t, got, 2)
	for _, e := range got {
		assert.Equal(t, model.SchedulerCategoryNews, e.Category)
	}
}

// TestSchedulerEntry_ListEnabled_ExcludesDisabled_BothPaths 는 **두 SQL 경로 모두**
// disabled 를 제외하는지 검증합니다.
//
// 규칙이 두 쿼리로 갈려 있어 한쪽만 고치면 조용히 어긋난다. 꺼 둔 시드가 돌면 운영자가
// 중단한 수집이 계속된다.
func TestSchedulerEntry_ListEnabled_ExcludesDisabled_BothPaths(t *testing.T) {
	repo, _ := newSchedulerEntryRepo(t)
	ctx := context.Background()

	require.NoError(t, repo.Insert(ctx, entry(model.SchedulerCategoryNews, "on", "https://on.example.com", true)))
	require.NoError(t, repo.Insert(ctx, entry(model.SchedulerCategoryNews, "off", "https://off.example.com", false)))

	all, err := repo.ListEnabled(ctx, "")
	require.NoError(t, err)
	require.Len(t, all, 1, "전체 경로도 disabled 를 제외해야 한다")
	assert.Equal(t, "on", all[0].SourceName)

	byCat, err := repo.ListEnabled(ctx, model.SchedulerCategoryNews)
	require.NoError(t, err)
	require.Len(t, byCat, 1, "category 경로도 disabled 를 제외해야 한다")
	assert.Equal(t, "on", byCat[0].SourceName)
}

// TestSchedulerEntry_ListEnabled_NoMatch 는 매칭 없음이 에러가 아닌 빈 결과인지
// 검증합니다 — 해당 category 시드가 없는 것은 정상 상태다.
func TestSchedulerEntry_ListEnabled_NoMatch(t *testing.T) {
	repo, _ := newSchedulerEntryRepo(t)
	ctx := context.Background()

	require.NoError(t, repo.Insert(ctx, entry(model.SchedulerCategoryNews, "n1", "https://n1.example.com", true)))

	got, err := repo.ListEnabled(ctx, model.SchedulerCategorySearch)
	require.NoError(t, err)
	assert.Empty(t, got)
}

// TestSchedulerEntry_Insert_PreservesFields 는 주요 필드가 왕복하는지 검증합니다.
//
// Interval 이 특히 중요하다 — 시드 발행 주기라, 잘못 저장되면 수집 빈도가 바뀐다.
func TestSchedulerEntry_Insert_PreservesFields(t *testing.T) {
	repo, _ := newSchedulerEntryRepo(t)
	ctx := context.Background()

	rec := entry(model.SchedulerCategorySearch, "kw", "https://kw.example.com", true)
	rec.Interval = 90 * time.Minute
	rec.Priority = 1
	rec.Notes = "integration test"
	require.NoError(t, repo.Insert(ctx, rec))
	require.NotZero(t, rec.ID)

	got, err := repo.ListEnabled(ctx, model.SchedulerCategorySearch)
	require.NoError(t, err)
	require.Len(t, got, 1)

	assert.Equal(t, 90*time.Minute, got[0].Interval, "발행 주기가 왕복해야 한다")
	assert.Equal(t, 1, got[0].Priority)
	assert.Equal(t, "kw", got[0].SourceName)
}

// TestSchedulerEntry_Update_TogglesEnabled 는 운영자가 시드를 끄는 경로를 검증합니다.
func TestSchedulerEntry_Update_TogglesEnabled(t *testing.T) {
	repo, _ := newSchedulerEntryRepo(t)
	ctx := context.Background()

	rec := entry(model.SchedulerCategoryNews, "toggle", "https://toggle.example.com", true)
	require.NoError(t, repo.Insert(ctx, rec))

	rec.Enabled = false
	require.NoError(t, repo.Update(ctx, rec))

	got, err := repo.ListEnabled(ctx, "")
	require.NoError(t, err)
	assert.Empty(t, got, "끈 시드는 더 이상 반환되면 안 된다")
}

// TestSchedulerEntry_Delete 는 삭제 후 목록에서 빠지는지 검증합니다.
func TestSchedulerEntry_Delete(t *testing.T) {
	repo, _ := newSchedulerEntryRepo(t)
	ctx := context.Background()

	rec := entry(model.SchedulerCategoryNews, "del", "https://del.example.com", true)
	require.NoError(t, repo.Insert(ctx, rec))
	require.NoError(t, repo.Delete(ctx, rec.ID))

	got, err := repo.ListEnabled(ctx, "")
	require.NoError(t, err)
	assert.Empty(t, got)
}
