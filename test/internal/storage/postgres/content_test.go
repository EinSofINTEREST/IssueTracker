package postgres_test

import (
	"context"
	"errors"
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

// 본 파일은 content repository 의 3-테이블 CTE upsert 를 검증합니다 (이슈 #629, 부모 #616).
//
// 핵심은 doc comment 가 설명하는 함정이다:
//
//	ON CONFLICT (url) 충돌 시 content_hash 가 동일하면 UPDATE 가 발생하지 않아
//	RETURNING 이 빈 결과를 반환하므로, COALESCE 로 기존 row 의 id 를 SELECT 하여
//	하위 테이블에 사용합니다.
//
// 그 fallback 이 깨지면 동일 해시 재저장 시 body/meta 가 갱신되지 않거나 NULL id 로
// INSERT 가 실패한다. 추론으로는 검증하기 어렵다.

func newContentRepo(t *testing.T) (repository.ContentRepository, *pgxpool.Pool) {
	t.Helper()
	pool := newTestPool(t)
	truncateContents(t, pool)
	t.Cleanup(func() { truncateContents(t, pool) })
	return pgstore.NewContentRepository(pool, logger.New(logger.DefaultConfig())), pool
}

func content(id, url, hash string) *core.Content {
	return &core.Content{
		ID:          id,
		SourceID:    "src",
		SourceType:  core.SourceTypeNews,
		Country:     "KR",
		Language:    "ko",
		Title:       "title " + id,
		Body:        "body " + id,
		Summary:     "summary " + id,
		Author:      "author",
		PublishedAt: time.Now().UTC().Add(-time.Hour),
		Category:    "politics",
		Tags:        []string{"t1", "t2"},
		URL:         url,
		ImageURLs:   []string{"https://img.example.com/" + id + ".jpg"},
		ContentHash: hash,
		WordCount:   42,
		Reliability: 0.5,
	}
}

// TestContent_Save_NewFillsAllTables 는 신규 저장이 3개 테이블을 모두 채우는지
// 검증합니다.
//
// body / summary / word_count 는 content_bodies, image_urls 는 content_meta 다.
// 조회가 join 으로 되돌려주는지까지 확인해야 CTE 전체가 동작한 것이다.
func TestContent_Save_NewFillsAllTables(t *testing.T) {
	repo, _ := newContentRepo(t)
	ctx := context.Background()

	c := content("new-1", "https://c.example.com/new-1", "hash-new-1")
	require.NoError(t, repo.Save(ctx, c))

	got, err := repo.GetByID(ctx, "new-1")
	require.NoError(t, err)

	assert.Equal(t, "title new-1", got.Title)
	assert.Equal(t, "body new-1", got.Body, "content_bodies 가 채워져야 한다")
	assert.Equal(t, "summary new-1", got.Summary)
	assert.Equal(t, 42, got.WordCount)
	require.Len(t, got.ImageURLs, 1, "content_meta 가 채워져야 한다")
}

// TestContent_Save_SameURLDifferentHash_Updates 는 내용이 바뀐 재저장이 반영되는지
// 검증합니다 — UPDATE 가 실제로 발생하는 경로.
func TestContent_Save_SameURLDifferentHash_Updates(t *testing.T) {
	repo, _ := newContentRepo(t)
	ctx := context.Background()
	const url = "https://c.example.com/upd"

	require.NoError(t, repo.Save(ctx, content("upd-1", url, "hash-v1")))

	v2 := content("upd-1", url, "hash-v2")
	v2.Title = "updated title"
	v2.Body = "updated body"
	v2.WordCount = 99
	require.NoError(t, repo.Save(ctx, v2))

	got, err := repo.GetByURL(ctx, url)
	require.NoError(t, err)
	assert.Equal(t, "updated title", got.Title)
	assert.Equal(t, "updated body", got.Body, "body 도 갱신돼야 한다")
	assert.Equal(t, 99, got.WordCount)
}

// TestContent_Save_SameHash_BodyStillProcessed 는 **COALESCE fallback 경로** 를
// 검증합니다.
//
// content_hash 가 같으면 contents 의 ON CONFLICT DO UPDATE 가 `WHERE hash != EXCLUDED.hash`
// 때문에 발생하지 않는다 → RETURNING 이 빈 결과 → 하위 테이블에 쓸 id 가 없다.
// COALESCE 가 기존 row 의 id 를 찾아 주지 못하면 **NULL id INSERT 로 실패하거나 body 가
// 조용히 누락** 된다.
func TestContent_Save_SameHash_BodyStillProcessed(t *testing.T) {
	repo, _ := newContentRepo(t)
	ctx := context.Background()
	const url = "https://c.example.com/samehash"

	require.NoError(t, repo.Save(ctx, content("sh-1", url, "hash-same")))

	// 같은 해시로 재저장 — body 만 다르게 (해시는 본문 기준이 아닐 수 있다)
	again := content("sh-1", url, "hash-same")
	again.Body = "second pass body"
	again.Summary = "second pass summary"
	again.WordCount = 77

	require.NoError(t, repo.Save(ctx, again),
		"동일 해시 재저장이 NULL id INSERT 로 실패하면 안 된다")

	got, err := repo.GetByURL(ctx, url)
	require.NoError(t, err)
	assert.Equal(t, "second pass body", got.Body,
		"contents UPDATE 가 skip 돼도 body 는 처리돼야 한다 (COALESCE fallback)")
	assert.Equal(t, 77, got.WordCount)
}

// TestContent_GetByContentHash 는 해시 조회가 body/meta 를 함께 반환하는지 검증합니다.
func TestContent_GetByContentHash(t *testing.T) {
	repo, _ := newContentRepo(t)
	ctx := context.Background()

	require.NoError(t, repo.Save(ctx, content("h-1", "https://c.example.com/h1", "hash-lookup")))

	got, err := repo.GetByContentHash(ctx, "hash-lookup")
	require.NoError(t, err)
	assert.Equal(t, "h-1", got.ID)
	assert.NotEmpty(t, got.Summary)
}

// TestContent_Get_NotFound 는 세 조회 경로가 모두 ErrNotFound 로 매핑되는지 검증합니다.
func TestContent_Get_NotFound(t *testing.T) {
	repo, _ := newContentRepo(t)
	ctx := context.Background()

	_, err := repo.GetByID(ctx, "absent")
	assert.True(t, errors.Is(err, storage.ErrNotFound), "GetByID: %v", err)

	_, err = repo.GetByURL(ctx, "https://c.example.com/absent")
	assert.True(t, errors.Is(err, storage.ErrNotFound), "GetByURL: %v", err)

	_, err = repo.GetByContentHash(ctx, "absent-hash")
	assert.True(t, errors.Is(err, storage.ErrNotFound), "GetByContentHash: %v", err)
}

// TestContent_ExistsByURL 는 존재 확인을 검증합니다 — 중복 감지 경로다.
func TestContent_ExistsByURL(t *testing.T) {
	repo, _ := newContentRepo(t)
	ctx := context.Background()
	const url = "https://c.example.com/exists"

	ok, err := repo.ExistsByURL(ctx, url)
	require.NoError(t, err)
	assert.False(t, ok)

	require.NoError(t, repo.Save(ctx, content("ex-1", url, "hash-ex")))

	ok, err = repo.ExistsByURL(ctx, url)
	require.NoError(t, err)
	assert.True(t, ok)
}

// TestContent_SaveBatch 는 다건 저장을 검증합니다.
func TestContent_SaveBatch(t *testing.T) {
	repo, _ := newContentRepo(t)
	ctx := context.Background()

	batch := []*core.Content{
		content("b-1", "https://c.example.com/b1", "hash-b1"),
		content("b-2", "https://c.example.com/b2", "hash-b2"),
		content("b-3", "https://c.example.com/b3", "hash-b3"),
	}
	require.NoError(t, repo.SaveBatch(ctx, batch))

	for _, c := range batch {
		got, err := repo.GetByID(ctx, c.ID)
		require.NoError(t, err, "batch 의 %s", c.ID)
		assert.NotEmpty(t, got.Body, "batch 도 하위 테이블을 채워야 한다")
	}
}

// ── UpdateValidationStatus ──────────────────────────────────────────────────

// TestContent_UpdateValidationStatus_NonRejectedNullsCodeDetail 은 rejected 가 아닐 때
// code/detail 이 NULL 로 저장되는지 검증합니다.
//
// 남아 있으면 통과한 콘텐츠에 과거 거부 사유가 붙어, 운영자가 원인을 오인한다.
func TestContent_UpdateValidationStatus_NonRejectedNullsCodeDetail(t *testing.T) {
	repo, pool := newContentRepo(t)
	ctx := context.Background()

	require.NoError(t, repo.Save(ctx, content("v-1", "https://c.example.com/v1", "hash-v")))

	// 먼저 rejected 로 사유를 채운다
	require.NoError(t, repo.UpdateValidationStatus(ctx, "v-1",
		model.ValidationStatusRejected, "VAL_001", "published_at missing"))

	// 그 다음 passed 로 전환
	require.NoError(t, repo.UpdateValidationStatus(ctx, "v-1",
		model.ValidationStatusPassed, "", ""))

	var code, detail *string
	err := pool.QueryRow(ctx,
		"SELECT reject_code, reject_detail FROM contents WHERE id = $1", "v-1").
		Scan(&code, &detail)
	require.NoError(t, err)
	assert.Nil(t, code, "rejected 가 아니면 reject_code 는 NULL")
	assert.Nil(t, detail, "rejected 가 아니면 reject_detail 은 NULL")
}

// TestContent_UpdateValidationStatus_RejectedStoresReason 은 거부 사유가 저장되는지
// 검증합니다.
func TestContent_UpdateValidationStatus_RejectedStoresReason(t *testing.T) {
	repo, pool := newContentRepo(t)
	ctx := context.Background()

	require.NoError(t, repo.Save(ctx, content("r-1", "https://c.example.com/r1", "hash-r")))
	require.NoError(t, repo.UpdateValidationStatus(ctx, "r-1",
		model.ValidationStatusRejected, "VAL_003", "content too short"))

	var status string
	var code, detail *string
	err := pool.QueryRow(ctx,
		"SELECT validation_status, reject_code, reject_detail FROM contents WHERE id = $1", "r-1").
		Scan(&status, &code, &detail)
	require.NoError(t, err)

	assert.Equal(t, model.ValidationStatusRejected, status)
	require.NotNil(t, code)
	assert.Equal(t, "VAL_003", *code)
	require.NotNil(t, detail)
	assert.Equal(t, "content too short", *detail)
}

// TestContent_UpdateValidationStatus_NotFound 는 없는 id 가 ErrNotFound 인지 검증합니다.
func TestContent_UpdateValidationStatus_NotFound(t *testing.T) {
	repo, _ := newContentRepo(t)

	err := repo.UpdateValidationStatus(context.Background(), "absent",
		model.ValidationStatusPassed, "", "")
	require.Error(t, err)
	assert.True(t, errors.Is(err, storage.ErrNotFound), "got: %v", err)
}

// TestContent_Delete_CascadesToChildTables 는 삭제가 하위 테이블까지 정리하는지
// 검증합니다 — 남으면 고아 row 가 쌓인다.
func TestContent_Delete_CascadesToChildTables(t *testing.T) {
	repo, pool := newContentRepo(t)
	ctx := context.Background()

	require.NoError(t, repo.Save(ctx, content("d-1", "https://c.example.com/d1", "hash-d")))
	require.NoError(t, repo.Delete(ctx, "d-1"))

	_, err := repo.GetByID(ctx, "d-1")
	assert.True(t, errors.Is(err, storage.ErrNotFound))

	var bodies int
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT COUNT(*) FROM content_bodies WHERE content_id = $1", "d-1").Scan(&bodies))
	assert.Zero(t, bodies, "content_bodies 에 고아 row 가 남으면 안 된다")
}
