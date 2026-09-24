package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pgstore "issuetracker/internal/storage/postgres"
	"issuetracker/pkg/logger"
)

// 본 파일은 이슈 #382 의 host signal 집계 SQL 을 **실제 postgres 에서** 검증합니다.
//
// 그 SQL 은 CTE + DISTINCT ON + FILTER 절 + regexp host 파생이 섞여 있는데 도입 시점에
// 실행해 본 적이 없다 (PR #604 본문에 "라이브 검증 불가" 로 기록). 추론만으로 검증하다
// 실제로 대소문자 버그를 하나 놓칠 뻔했다.

// newAggregator 는 집계기와 pool 을 함께 돌려줍니다.
//
// pool 을 함께 돌려주는 이유: 테스트가 fixture 를 넣으려면 같은 pool 이 필요하다.
// 따로 열면 연결이 두 벌 생기고, cleanup 이 다른 pool 을 닫는 혼동도 생긴다.
func newAggregator(t *testing.T) (*pgstore.HostSignalAggregator, *pgxpool.Pool) {
	t.Helper()
	pool := newTestPool(t)
	truncateContents(t, pool)
	t.Cleanup(func() { truncateContents(t, pool) })
	return pgstore.NewHostSignalAggregator(pool, logger.New(logger.DefaultConfig())), pool
}

func find(t *testing.T, rows []pgstore.HostSignalAggregate, host string) pgstore.HostSignalAggregate {
	t.Helper()
	for _, r := range rows {
		if r.Host == host {
			return r
		}
	}
	t.Fatalf("host %q 가 집계 결과에 없다 (결과 %d건)", host, len(rows))
	return pgstore.HostSignalAggregate{}
}

// TestAggregate_HostExtraction 은 URL 에서 host 를 뽑는 규칙을 검증합니다.
//
// **저장(SQL)과 조회(Go url.Parse)가 같은 host 를 내야** 점수가 매칭된다. 어긋나면
// 에러 없이 조용히 빗나가므로 실제 DB 에서 확인한다.
func TestAggregate_HostExtraction(t *testing.T) {
	agg, pool := newAggregator(t)
	now := time.Now().UTC()

	cases := []struct {
		id, url, wantHost string
	}{
		{"h1", "https://a.example.com/article/1", "a.example.com"},
		{"h2", "HTTPS://B.Example.COM/article/2", "b.example.com"},
		{"h3", "http://c.example.com:8080/article/3", "c.example.com"},
		{"h4", "https://d.example.com", "d.example.com"},
	}
	for _, c := range cases {
		insertContent(t, pool, c.id, c.url, "politics", "passed", now.Add(-time.Hour), now)
	}

	rows, err := agg.Aggregate(context.Background(), 60)
	require.NoError(t, err)

	for _, c := range cases {
		got := find(t, rows, c.wantHost)
		assert.Equal(t, c.wantHost, got.Host, "url=%s", c.url)
	}
}

// TestAggregate_ValidationCounts 는 FILTER 절이 passed / rejected 를 정확히 세는지
// 검증합니다.
func TestAggregate_ValidationCounts(t *testing.T) {
	agg, pool := newAggregator(t)
	now := time.Now().UTC()
	host := "https://counts.example.com/"

	for i, st := range []string{"passed", "passed", "passed", "rejected", "pending"} {
		insertContent(t, pool, "c"+string(rune('a'+i)), host+string(rune('a'+i)),
			"politics", st, now.Add(-time.Hour), now)
	}

	rows, err := agg.Aggregate(context.Background(), 60)
	require.NoError(t, err)

	got := find(t, rows, "counts.example.com")
	assert.Equal(t, 3, got.PassedCount)
	assert.Equal(t, 1, got.RejectedCount)
	assert.Equal(t, 5, got.SampleCount, "pending 도 표본에는 포함된다")
}

// TestAggregate_TopCategory 는 DISTINCT ON + GROUP BY 조합이 최빈 카테고리를 고르는지
// 검증합니다.
//
// 이 조합은 문법이 까다로워 (DISTINCT ON 의 선두가 ORDER BY 와 일치해야 함) 실행 없이는
// 확신할 수 없었다.
func TestAggregate_TopCategory(t *testing.T) {
	agg, pool := newAggregator(t)
	now := time.Now().UTC()
	base := "https://cat.example.com/"

	for i := 0; i < 3; i++ {
		insertContent(t, pool, "sports"+string(rune('a'+i)), base+"s"+string(rune('a'+i)),
			"sports", "passed", now.Add(-time.Hour), now)
	}
	insertContent(t, pool, "pol1", base+"p1", "politics", "passed", now.Add(-time.Hour), now)

	rows, err := agg.Aggregate(context.Background(), 60)
	require.NoError(t, err)

	got := find(t, rows, "cat.example.com")
	assert.Equal(t, "sports", got.TopCategory, "최빈 카테고리를 골라야 한다")
}

// TestAggregate_NullPublishedAt 은 published_at NULL 이 평균에서 제외되는지 검증합니다.
//
// migration 028 이후 nullable 이다. NULL 을 0 으로 치면 지연이 0 인 것처럼 보여
// freshness 가 부당하게 높아진다.
func TestAggregate_NullPublishedAt(t *testing.T) {
	agg, pool := newAggregator(t)
	now := time.Now().UTC()
	base := "https://nullpub.example.com/"

	// 지연 1시간인 row 하나 + published_at 없는 row 둘
	insertContent(t, pool, "np1", base+"1", "politics", "passed", now.Add(-time.Hour), now)
	insertContent(t, pool, "np2", base+"2", "politics", "passed", time.Time{}, now)
	insertContent(t, pool, "np3", base+"3", "politics", "passed", time.Time{}, now)

	rows, err := agg.Aggregate(context.Background(), 60)
	require.NoError(t, err)

	got := find(t, rows, "nullpub.example.com")
	assert.Equal(t, 3, got.SampleCount)
	assert.InDelta(t, 3600.0, got.AvgDetectLagSeconds, 60.0,
		"NULL 은 평균에서 빠지므로 1시간 지연 하나만 반영돼야 한다")
}

// TestAggregate_WindowBoundary 는 구간 밖 row 가 제외되는지 검증합니다.
func TestAggregate_WindowBoundary(t *testing.T) {
	agg, pool := newAggregator(t)
	now := time.Now().UTC()
	base := "https://window.example.com/"

	insertContent(t, pool, "w1", base+"1", "politics", "passed", now.Add(-time.Hour), now)
	// 구간(10분) 밖
	insertContent(t, pool, "w2", base+"2", "politics", "passed", now.Add(-5*time.Hour), now.Add(-3*time.Hour))

	rows, err := agg.Aggregate(context.Background(), 10)
	require.NoError(t, err)

	got := find(t, rows, "window.example.com")
	assert.Equal(t, 1, got.SampleCount, "구간 밖 row 는 제외돼야 한다")
}

// TestAggregate_NegativeLagExcluded 는 published_at 이 created_at 보다 미래인 row 가
// 평균에서 빠지는지 검증합니다.
//
// 잘못 파싱된 날짜가 음수 지연을 만들면 평균이 끌려 내려가 freshness 가 부풀려진다.
func TestAggregate_NegativeLagExcluded(t *testing.T) {
	agg, pool := newAggregator(t)
	now := time.Now().UTC()
	base := "https://neg.example.com/"

	insertContent(t, pool, "n1", base+"1", "politics", "passed", now.Add(-time.Hour), now)
	insertContent(t, pool, "n2", base+"2", "politics", "passed", now.Add(10*time.Hour), now)

	rows, err := agg.Aggregate(context.Background(), 60)
	require.NoError(t, err)

	got := find(t, rows, "neg.example.com")
	assert.Greater(t, got.AvgDetectLagSeconds, 0.0, "음수 지연이 평균을 끌어내리면 안 된다")
	assert.InDelta(t, 3600.0, got.AvgDetectLagSeconds, 60.0)
}

// TestAggregate_InvalidWindow 는 잘못된 구간이 거부되는지 검증합니다.
func TestAggregate_InvalidWindow(t *testing.T) {
	agg, _ := newAggregator(t)

	for _, w := range []int{0, -1} {
		_, err := agg.Aggregate(context.Background(), w)
		require.Error(t, err, "window=%d", w)
	}
}

// TestAggregate_Empty 는 데이터가 없을 때 빈 결과를 내는지 검증합니다.
func TestAggregate_Empty(t *testing.T) {
	agg, _ := newAggregator(t)

	rows, err := agg.Aggregate(context.Background(), 60)
	require.NoError(t, err)
	assert.Empty(t, rows)
}
