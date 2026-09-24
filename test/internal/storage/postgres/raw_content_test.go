package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/processor/fetcher/core"
	"issuetracker/internal/storage"
	"issuetracker/internal/storage/model"
	pgstore "issuetracker/internal/storage/postgres"
	"issuetracker/internal/storage/repository"
	"issuetracker/pkg/logger"
)

// 본 파일은 raw_content (Claim Check 저장소) 를 검증합니다 (이슈 #631, 부모 #616).
//
// parser 가 ID 만 Kafka 로 받고 본문은 여기서 읽는다 (이슈 #134). 저장이 깨지면 파이프라인
// 전체가 멈추고, purge 가 과도하면 아직 처리 안 된 row 가 사라진다.

func newRawContentRepo(t *testing.T) (repository.RawContentRepository, *pgxpool.Pool) {
	t.Helper()
	pool := newTestPool(t)
	truncateRawContents(t, pool)
	t.Cleanup(func() { truncateRawContents(t, pool) })
	return pgstore.NewRawContentRepository(pool, logger.New(logger.DefaultConfig())), pool
}

func truncateRawContents(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(context.Background(), "TRUNCATE raw_contents CASCADE")
	require.NoError(t, err)
}

func rawContent(id, url string, fetchedAt time.Time) *core.RawContent {
	return &core.RawContent{
		ID:         id,
		URL:        url,
		HTML:       "<html><body>" + id + "</body></html>",
		StatusCode: 200,
		FetchedAt:  fetchedAt,
		SourceInfo: core.SourceInfo{
			Name:     "test",
			Country:  "KR",
			Language: "ko",
		},
		Headers: map[string]string{"content-type": "text/html"},
	}
}

// TestRawContent_SaveGet_Roundtrip 은 Claim Check 의 핵심 왕복을 검증합니다.
//
// HTML 이 온전히 돌아오지 않으면 parser 가 빈 본문을 파싱한다.
func TestRawContent_SaveGet_Roundtrip(t *testing.T) {
	repo, _ := newRawContentRepo(t)
	ctx := context.Background()

	raw := rawContent("rc-1", "https://raw.example.com/1", time.Now().UTC())
	require.NoError(t, repo.Save(ctx, raw))

	got, err := repo.GetByID(ctx, "rc-1")
	require.NoError(t, err)
	assert.Equal(t, raw.HTML, got.HTML, "HTML 이 온전히 왕복해야 한다")
	assert.Equal(t, 200, got.StatusCode)
	assert.Equal(t, "https://raw.example.com/1", got.URL)
}

// TestRawContent_GetByURL 은 URL 조회를 검증합니다 — 중복 감지 경로.
func TestRawContent_GetByURL(t *testing.T) {
	repo, _ := newRawContentRepo(t)
	ctx := context.Background()
	const url = "https://raw.example.com/byurl"

	require.NoError(t, repo.Save(ctx, rawContent("rc-url", url, time.Now().UTC())))

	got, err := repo.GetByURL(ctx, url)
	require.NoError(t, err)
	assert.Equal(t, "rc-url", got.ID)
}

// TestRawContent_NotFound 는 없는 키에 대한 매핑을 검증합니다.
func TestRawContent_NotFound(t *testing.T) {
	repo, _ := newRawContentRepo(t)
	ctx := context.Background()

	_, err := repo.GetByID(ctx, "absent")
	assert.True(t, errors.Is(err, storage.ErrNotFound), "GetByID: %v", err)

	_, err = repo.GetByURL(ctx, "https://raw.example.com/absent")
	assert.True(t, errors.Is(err, storage.ErrNotFound), "GetByURL: %v", err)
}

// TestRawContent_Delete 는 처리 완료 후 정리 경로를 검증합니다.
//
// parser 가 성공 후 삭제한다 — 남으면 raw HTML 이 무한 누적된다.
func TestRawContent_Delete(t *testing.T) {
	repo, _ := newRawContentRepo(t)
	ctx := context.Background()

	require.NoError(t, repo.Save(ctx, rawContent("rc-del", "https://raw.example.com/del", time.Now().UTC())))
	require.NoError(t, repo.Delete(ctx, "rc-del"))

	_, err := repo.GetByID(ctx, "rc-del")
	assert.True(t, errors.Is(err, storage.ErrNotFound))
}

// TestRawContent_DeleteBefore_KeepsRecent 는 purge 가 **cutoff 이후 row 를 남기는지**
// 검증합니다.
//
// 과도하면 아직 처리되지 않은 row 가 사라져 parser 가 ErrNotFound 를 받는다 — Claim Check
// 가 깨지는 지점이다.
func TestRawContent_DeleteBefore_KeepsRecent(t *testing.T) {
	repo, _ := newRawContentRepo(t)
	ctx := context.Background()

	now := time.Now().UTC()
	require.NoError(t, repo.Save(ctx, rawContent("old-1", "https://raw.example.com/o1", now.Add(-48*time.Hour))))
	require.NoError(t, repo.Save(ctx, rawContent("old-2", "https://raw.example.com/o2", now.Add(-36*time.Hour))))
	require.NoError(t, repo.Save(ctx, rawContent("new-1", "https://raw.example.com/n1", now.Add(-time.Hour))))

	deleted, err := repo.DeleteBefore(ctx, now.Add(-24*time.Hour))
	require.NoError(t, err)
	assert.Equal(t, int64(2), deleted, "cutoff 이전 2건만 삭제")

	_, err = repo.GetByID(ctx, "new-1")
	require.NoError(t, err, "cutoff 이후 row 는 남아야 한다")

	_, err = repo.GetByID(ctx, "old-1")
	assert.True(t, errors.Is(err, storage.ErrNotFound))
}

// TestRawContent_DeleteBefore_NoMatch 는 삭제 대상이 없을 때 0 을 반환하는지 검증합니다.
func TestRawContent_DeleteBefore_NoMatch(t *testing.T) {
	repo, _ := newRawContentRepo(t)
	ctx := context.Background()

	require.NoError(t, repo.Save(ctx, rawContent("keep", "https://raw.example.com/k", time.Now().UTC())))

	deleted, err := repo.DeleteBefore(ctx, time.Now().UTC().Add(-24*time.Hour))
	require.NoError(t, err)
	assert.Zero(t, deleted)
}

// TestRawContent_List 는 필터 조회가 동작하는지 검증합니다.
func TestRawContent_List(t *testing.T) {
	repo, _ := newRawContentRepo(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		require.NoError(t, repo.Save(ctx,
			rawContent(fmt.Sprintf("l-%d", i), fmt.Sprintf("https://raw.example.com/l%d", i), time.Now().UTC())))
	}

	got, err := repo.List(ctx, model.RawContentFilter{Pagination: model.Pagination{Limit: 2}})
	require.NoError(t, err)
	assert.Len(t, got, 2, "limit 준수")
}
