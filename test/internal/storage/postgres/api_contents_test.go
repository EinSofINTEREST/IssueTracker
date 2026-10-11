package postgres_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/api"
	"issuetracker/internal/processor/fetcher/core"
	"issuetracker/pkg/logger"
)

// 본 파일은 라우터 → 핸들러 → 실제 PostgreSQL repository 를 잇는 통합 테스트입니다 (이슈 #636 완료 조건).
//
// test/internal/api 의 fake repository 는 "HTTP 쿼리 → ContentFilter 변환" 만 검증한다. 그 필터가
// 실제 SQL 에서 기대한 행을 고르는지, 상세의 3-테이블 JOIN 이 body · image_urls 를 되돌리는지는
// 여기서만 확인된다.
//
// contents 를 TRUNCATE 하는 helper 와 **같은 패키지** 에 두는 이유는 helpers_test.go 의 경고 참조 —
// 다른 패키지에서 contents 를 쓰면 병렬 실행 중 임의 시점에 지워진다.

// newAPIServer 는 실제 repository 를 주입한 API 서버와 시드 데이터 3건을 만듭니다.
//
//	id        country/lang  source   source_type  category  published    tags               reliability
//	api-us-1  US/en         src-us   news         economy   2026-03-01   economy,markets    0.9
//	api-kr-1  KR/ko         src-kr   community    politics  2026-01-15   politics           0.3
//	api-kr-2  KR/ko         src-kr2  news         politics  2026-02-15   politics,election  0.7
//
// 필터마다 "맞는 행" 과 "안 맞는 행" 이 모두 있도록 값을 흩어 놓았다 — 전부 같은 값이면 WHERE 가
// 빠져도 테스트가 통과한다.
func newAPIServer(t *testing.T) *httptest.Server {
	t.Helper()

	repo, pool := newContentRepo(t)
	ctx := context.Background()

	us := content("api-us-1", "https://c.example.com/api-us-1", "hash-api-us-1")
	us.Country, us.Language = "US", "en"
	us.SourceID, us.SourceType, us.Category = "src-us", core.SourceTypeNews, "economy"
	us.PublishedAt = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	us.Tags = []string{"economy", "markets"}
	us.Reliability = 0.9

	kr1 := content("api-kr-1", "https://c.example.com/api-kr-1", "hash-api-kr-1")
	kr1.SourceID, kr1.SourceType, kr1.Category = "src-kr", core.SourceTypeCommunity, "politics"
	kr1.PublishedAt = time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	kr1.Tags = []string{"politics"}
	kr1.Reliability = 0.3

	kr2 := content("api-kr-2", "https://c.example.com/api-kr-2", "hash-api-kr-2")
	kr2.SourceID, kr2.SourceType, kr2.Category = "src-kr2", core.SourceTypeNews, "politics"
	kr2.PublishedAt = time.Date(2026, 2, 15, 0, 0, 0, 0, time.UTC)
	kr2.Tags = []string{"politics", "election"}
	kr2.Reliability = 0.7

	for _, c := range []*core.Content{us, kr1, kr2} {
		require.NoError(t, repo.Save(ctx, c))
	}

	srv := httptest.NewServer(api.NewRouter(api.Deps{
		DB:       pool,
		Contents: repo,
		Log:      logger.New(logger.DefaultConfig()),
	}))
	t.Cleanup(srv.Close)
	return srv
}

func getJSON(t *testing.T, url string) (int, map[string]interface{}) {
	t.Helper()

	resp, err := http.Get(url) //nolint:gosec // 테스트 서버 URL
	require.NoError(t, err)
	defer resp.Body.Close()

	var body map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	return resp.StatusCode, body
}

func itemIDs(t *testing.T, body map[string]interface{}) []string {
	t.Helper()

	items, ok := body["items"].([]interface{})
	require.True(t, ok, "items 가 배열이 아닙니다: %v", body)

	ids := make([]string, 0, len(items))
	for _, it := range items {
		ids = append(ids, it.(map[string]interface{})["id"].(string))
	}
	return ids
}

// country 필터가 실제 WHERE 로 내려가 다른 나라 행을 걸러내는지 — fake 로는 보이지 않는 경로.
func TestAPIContents_CountryFilter_AppliesInSQL(t *testing.T) {
	srv := newAPIServer(t)

	status, body := getJSON(t, srv.URL+"/api/contents?country=us")
	require.Equal(t, http.StatusOK, status, body)
	assert.ElementsMatch(t, []string{"api-us-1"}, itemIDs(t, body))

	// List 는 content_meta 를 JOIN 하지 않는다 — 목록 항목에 image_urls 가 있으면 항상 [] 로 보여 호출자를 속인다.
	item := body["items"].([]interface{})[0].(map[string]interface{})
	_, has := item["image_urls"]
	assert.False(t, has, "목록 항목에 image_urls 가 노출됐습니다")
}

// 나머지 필터 전부 — 각 WHERE 조건이 실제로 행을 가르는지 (이슈 #636: 필터별 통합 케이스).
func TestAPIContents_EachFilter_AppliesInSQL(t *testing.T) {
	srv := newAPIServer(t)

	tests := []struct {
		name  string
		query string
		want  []string
	}{
		{"language", "language=en", []string{"api-us-1"}},
		{"category", "category=economy", []string{"api-us-1"}},
		{"source", "source=src-kr", []string{"api-kr-1"}},
		{"source_type", "source_type=community", []string{"api-kr-1"}},
		{"published_after", "published_after=2026-02-01T00:00:00Z", []string{"api-us-1", "api-kr-2"}},
		{"published_before", "published_before=2026-02-01T00:00:00Z", []string{"api-kr-1"}},
		{"복합 — 같은 나라에서 source_type 으로 가름", "country=KR&source_type=news", []string{"api-kr-2"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, body := getJSON(t, srv.URL+"/api/contents?"+tt.query)
			require.Equal(t, http.StatusOK, status, body)
			assert.ElementsMatch(t, tt.want, itemIDs(t, body))
		})
	}
}

// tags 는 AND 조건 (@>) 이다 — 하나만 가진 행은 빠져야 한다.
func TestAPIContents_TagsFilter_IsConjunctive(t *testing.T) {
	srv := newAPIServer(t)

	status, body := getJSON(t, srv.URL+"/api/contents?tags=politics,election")
	require.Equal(t, http.StatusOK, status, body)
	assert.ElementsMatch(t, []string{"api-kr-2"}, itemIDs(t, body))

	status, body = getJSON(t, srv.URL+"/api/contents?tags=politics")
	require.Equal(t, http.StatusOK, status, body)
	assert.ElementsMatch(t, []string{"api-kr-1", "api-kr-2"}, itemIDs(t, body))
}

// min_reliability 와 with_total 이 같은 필터로 List · Count 에 적용되는지.
func TestAPIContents_MinReliabilityWithTotal_CountsFilteredRows(t *testing.T) {
	srv := newAPIServer(t)

	status, body := getJSON(t, srv.URL+"/api/contents?min_reliability=0.5&with_total=true")
	require.Equal(t, http.StatusOK, status, body)
	assert.ElementsMatch(t, []string{"api-us-1", "api-kr-2"}, itemIDs(t, body))
	assert.EqualValues(t, 2, body["total"], "total 은 필터가 적용된 행 수여야 합니다")
}

// 상세는 3-테이블 JOIN 으로 body (content_bodies) 와 image_urls (content_meta) 를 되돌려야 한다.
func TestAPIContentDetail_JoinsBodyAndImageURLs(t *testing.T) {
	srv := newAPIServer(t)

	status, body := getJSON(t, srv.URL+"/api/contents/api-kr-1")
	require.Equal(t, http.StatusOK, status, body)
	assert.Equal(t, "api-kr-1", body["id"])
	assert.Equal(t, "body api-kr-1", body["body"], "content_bodies 가 JOIN 되지 않았습니다")
	assert.Equal(t, []interface{}{"https://img.example.com/api-kr-1.jpg"}, body["image_urls"],
		"content_meta 가 JOIN 되지 않았습니다")
}

func TestAPIContentDetail_UnknownID_Returns404(t *testing.T) {
	srv := newAPIServer(t)

	status, body := getJSON(t, srv.URL+"/api/contents/does-not-exist")
	require.Equal(t, http.StatusNotFound, status, body)
}
