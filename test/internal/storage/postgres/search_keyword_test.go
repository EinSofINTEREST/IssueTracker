package postgres_test

import (
	"context"
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

// 본 파일은 search_keyword 의 ListEnabled 필터를 검증합니다 (이슈 #631, 부모 #616).
//
// doc comment 가 짚는 미묘한 점:
//
//	빈 문자열 = 전체 매치, 비어있지 않으면 정확 매치 (language='' 자체도 정확 매치 대상)
//
// **인자가 빈 문자열** 이면 전체를 반환하지만, **저장된 row 의 language 가 빈 문자열** 인
// 경우는 인자도 빈 문자열일 때만 매치된다. 두 의미가 한 조건에 얽혀 있어 손대면 조용히 갈린다.

func newSearchKeywordRepo(t *testing.T) (repository.SearchKeywordRepository, *pgxpool.Pool) {
	t.Helper()
	pool := newTestPool(t)
	truncateSearchKeywords(t, pool)
	t.Cleanup(func() { truncateSearchKeywords(t, pool) })

	repo, err := pgstore.NewSearchKeywordRepository(pool, logger.New(logger.DefaultConfig()))
	require.NoError(t, err)
	return repo, pool
}

func truncateSearchKeywords(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(context.Background(), "TRUNCATE search_keywords CASCADE")
	require.NoError(t, err)
}

func keyword(kw, lang, region string, enabled bool) *model.SearchKeywordRecord {
	return &model.SearchKeywordRecord{
		Keyword:  kw,
		Language: lang,
		Region:   region,
		Enabled:  enabled,
	}
}

func names(recs []*model.SearchKeywordRecord) []string {
	out := make([]string, 0, len(recs))
	for _, r := range recs {
		out = append(out, r.Keyword)
	}
	return out
}

// TestSearchKeyword_ListEnabled_FilterMatrix 는 두 필터의 조합을 전부 확인합니다.
//
// **language 가 빈 row** 가 ("", "") 에는 나오고 ("ko", "") 에는 안 나오는 것이 핵심이다 —
// 빈 문자열의 두 의미(전체 / 정확 매치)가 여기서 갈린다.
func TestSearchKeyword_ListEnabled_FilterMatrix(t *testing.T) {
	repo, _ := newSearchKeywordRepo(t)
	ctx := context.Background()

	require.NoError(t, repo.Insert(ctx, keyword("ko-kr", "ko", "KR", true)))
	require.NoError(t, repo.Insert(ctx, keyword("ko-us", "ko", "US", true)))
	require.NoError(t, repo.Insert(ctx, keyword("en-kr", "en", "KR", true)))
	require.NoError(t, repo.Insert(ctx, keyword("blank-lang", "", "KR", true)))

	all, err := repo.ListEnabled(ctx, "", "")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"ko-kr", "ko-us", "en-kr", "blank-lang"}, names(all),
		"빈 인자는 전체")

	ko, err := repo.ListEnabled(ctx, "ko", "")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"ko-kr", "ko-us"}, names(ko),
		"language 지정 시 빈 language row 는 제외돼야 한다")

	kr, err := repo.ListEnabled(ctx, "", "KR")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"ko-kr", "en-kr", "blank-lang"}, names(kr))

	both, err := repo.ListEnabled(ctx, "ko", "KR")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"ko-kr"}, names(both))
}

// TestSearchKeyword_ListEnabled_ExcludesDisabled 는 비활성 keyword 제외를 검증합니다.
func TestSearchKeyword_ListEnabled_ExcludesDisabled(t *testing.T) {
	repo, _ := newSearchKeywordRepo(t)
	ctx := context.Background()

	require.NoError(t, repo.Insert(ctx, keyword("on", "ko", "KR", true)))
	require.NoError(t, repo.Insert(ctx, keyword("off", "ko", "KR", false)))

	got, err := repo.ListEnabled(ctx, "", "")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"on"}, names(got))
}

// TestSearchKeyword_Insert_Validation 은 빈 keyword 거부와 source 보정을 검증합니다.
func TestSearchKeyword_Insert_Validation(t *testing.T) {
	repo, _ := newSearchKeywordRepo(t)
	ctx := context.Background()

	require.Error(t, repo.Insert(ctx, keyword("", "ko", "KR", true)), "빈 keyword 는 거부")

	rec := keyword("defsrc", "ko", "KR", true)
	require.NoError(t, repo.Insert(ctx, rec))
	assert.NotEmpty(t, rec.Source, "빈 source 는 보정돼야 한다")
}

// TestSearchKeyword_Insert_Duplicate 는 UNIQUE 충돌 매핑을 검증합니다.
func TestSearchKeyword_Insert_Duplicate(t *testing.T) {
	repo, _ := newSearchKeywordRepo(t)
	ctx := context.Background()

	require.NoError(t, repo.Insert(ctx, keyword("dup", "ko", "KR", true)))
	err := repo.Insert(ctx, keyword("dup", "ko", "KR", true))
	require.Error(t, err)
	assert.True(t, errors.Is(err, storage.ErrDuplicate), "got: %v", err)
}

// TestSearchKeyword_MarkSearched 는 last_searched_at 갱신을 검증합니다.
//
// 이 값으로 다음 검색 시점을 정하므로, 갱신되지 않으면 같은 keyword 가 반복 검색된다.
func TestSearchKeyword_MarkSearched(t *testing.T) {
	repo, _ := newSearchKeywordRepo(t)
	ctx := context.Background()

	rec := keyword("marked", "ko", "KR", true)
	require.NoError(t, repo.Insert(ctx, rec))

	got, err := repo.ListEnabled(ctx, "", "")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Nil(t, got[0].LastSearchedAt, "초기값은 NULL")

	now := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, repo.MarkSearched(ctx, rec.ID, now))

	got, err = repo.ListEnabled(ctx, "", "")
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.NotNil(t, got[0].LastSearchedAt, "갱신되지 않으면 같은 keyword 가 반복 검색된다")
}

// TestSearchKeyword_NotFound 는 없는 id 에 대한 매핑을 검증합니다.
func TestSearchKeyword_NotFound(t *testing.T) {
	repo, _ := newSearchKeywordRepo(t)
	ctx := context.Background()

	err := repo.MarkSearched(ctx, 999999, time.Now())
	require.Error(t, err)
	assert.True(t, errors.Is(err, storage.ErrNotFound), "MarkSearched: %v", err)

	rec := keyword("ghost", "ko", "KR", true)
	rec.ID = 999999
	err = repo.Update(ctx, rec)
	require.Error(t, err)
	assert.True(t, errors.Is(err, storage.ErrNotFound), "Update: %v", err)
}
