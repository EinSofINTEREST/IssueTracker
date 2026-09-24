package postgres_test

// pagination_test.go — 이슈 #654: offset 페이지네이션의 결정성.
//
// published_at 이 동률인 행이 여러 개일 때, 페이지를 넘겨가며 모든 행이 **정확히 한 번씩**
// 나와야 한다. tiebreaker 가 없으면 동률 그룹의 순서가 쿼리마다 달라져 행이 건너뛰어지거나
// 중복된다 — 에러 없이.

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/storage/model"
	pgstore "issuetracker/internal/storage/postgres"
	"issuetracker/pkg/logger"
)

const (
	tiedRowCount = 25
	pageSize     = 5
)

func TestContentList_TiedPublishedAt_PaginatesDeterministically(t *testing.T) {
	pool := newTestPool(t)
	truncateContents(t, pool)

	repo := pgstore.NewContentRepository(pool, logger.New(logger.DefaultConfig()))

	// 전부 같은 published_at — tiebreaker 가 없으면 순서가 보장되지 않는 상황.
	published := time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC)
	created := published
	for i := 0; i < tiedRowCount; i++ {
		insertContent(t, pool,
			fmt.Sprintf("tie-%02d", i),
			fmt.Sprintf("https://example.com/tie/%02d", i),
			"politics", "passed", published, created)
	}

	seen := map[string]int{}
	for offset := 0; offset < tiedRowCount; offset += pageSize {
		page, err := repo.List(context.Background(), model.ContentFilter{
			Pagination: model.Pagination{Limit: pageSize, Offset: offset},
		})
		require.NoError(t, err)
		require.Len(t, page, pageSize, "offset=%d 에서 페이지 크기가 다릅니다", offset)

		for _, c := range page {
			seen[c.ID]++
		}
	}

	assert.Len(t, seen, tiedRowCount, "페이지를 모두 넘겼는데 고유 행 수가 다릅니다 — 누락 또는 중복")
	for id, n := range seen {
		assert.Equal(t, 1, n, "%s 가 %d 번 나왔습니다", id, n)
	}
}

// 같은 조건을 반복 조회해도 순서가 같아야 한다 — tiebreaker 의 직접 검증.
func TestContentList_TiedPublishedAt_OrderIsStableAcrossQueries(t *testing.T) {
	pool := newTestPool(t)
	truncateContents(t, pool)

	repo := pgstore.NewContentRepository(pool, logger.New(logger.DefaultConfig()))

	published := time.Date(2026, 5, 2, 9, 0, 0, 0, time.UTC)
	for i := 0; i < tiedRowCount; i++ {
		insertContent(t, pool,
			fmt.Sprintf("stable-%02d", i),
			fmt.Sprintf("https://example.com/stable/%02d", i),
			"politics", "passed", published, published)
	}

	filter := model.ContentFilter{Pagination: model.Pagination{Limit: tiedRowCount, Offset: 0}}

	first, err := repo.List(context.Background(), filter)
	require.NoError(t, err)
	require.Len(t, first, tiedRowCount)

	for attempt := 0; attempt < 3; attempt++ {
		again, err := repo.List(context.Background(), filter)
		require.NoError(t, err)
		require.Len(t, again, tiedRowCount)

		for i := range first {
			require.Equal(t, first[i].ID, again[i].ID,
				"%d 번째 행이 조회마다 다릅니다 (attempt=%d)", i, attempt)
		}
	}
}

// published_at 이 NULL 인 행은 전부 하나의 동률 그룹이다 — 가장 큰 그룹이 생기는 경우.
func TestContentList_NullPublishedAt_PaginatesDeterministically(t *testing.T) {
	pool := newTestPool(t)
	truncateContents(t, pool)

	repo := pgstore.NewContentRepository(pool, logger.New(logger.DefaultConfig()))

	created := time.Date(2026, 5, 3, 9, 0, 0, 0, time.UTC)
	for i := 0; i < tiedRowCount; i++ {
		// publishedAt zero → helper 가 NULL 로 넣는다.
		insertContent(t, pool,
			fmt.Sprintf("null-%02d", i),
			fmt.Sprintf("https://example.com/null/%02d", i),
			"politics", "passed", time.Time{}, created)
	}

	seen := map[string]int{}
	for offset := 0; offset < tiedRowCount; offset += pageSize {
		page, err := repo.List(context.Background(), model.ContentFilter{
			Pagination: model.Pagination{Limit: pageSize, Offset: offset},
		})
		require.NoError(t, err)

		for _, c := range page {
			seen[c.ID]++
		}
	}

	assert.Len(t, seen, tiedRowCount)
	for id, n := range seen {
		assert.Equal(t, 1, n, "%s 가 %d 번 나왔습니다", id, n)
	}
}
