package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/api"
	"issuetracker/internal/processor/fetcher/core"
	"issuetracker/internal/storage"
	"issuetracker/internal/storage/model"
	"issuetracker/internal/storage/repository"
)

// ─────────────────────────────────────────────────────────────────────────────
// fake repository
//
// 본 계층의 책임은 "HTTP 쿼리 → ContentFilter 변환" 이다. fake 는 변환 결과를 그대로
// 포착하므로 실제 DB 보다 정확히 검증한다 — SQL 자체는 이슈 #629 의 repository 통합
// 테스트가 덮는다.
// ─────────────────────────────────────────────────────────────────────────────

type fakeContentRepo struct {
	listResult []*core.Content
	listErr    error

	countResult int64
	countErr    error
	countCalls  int

	getResult *core.Content
	getErr    error
	getID     string

	gotFilter model.ContentFilter
}

func (f *fakeContentRepo) List(_ context.Context, filter model.ContentFilter) ([]*core.Content, error) {
	f.gotFilter = filter
	return f.listResult, f.listErr
}

func (f *fakeContentRepo) Count(_ context.Context, filter model.ContentFilter) (int64, error) {
	f.countCalls++
	f.gotFilter = filter
	return f.countResult, f.countErr
}

func (f *fakeContentRepo) GetByID(_ context.Context, id string) (*core.Content, error) {
	f.getID = id
	return f.getResult, f.getErr
}

// 나머지 인터페이스 메소드는 본 계층이 쓰지 않는다 — 호출되면 테스트가 드러내도록 panic.
func (f *fakeContentRepo) Save(context.Context, *core.Content) error { panic("unexpected Save") }
func (f *fakeContentRepo) SaveBatch(context.Context, []*core.Content) error {
	panic("unexpected SaveBatch")
}
func (f *fakeContentRepo) GetByURL(context.Context, string) (*core.Content, error) {
	panic("unexpected GetByURL")
}
func (f *fakeContentRepo) GetByContentHash(context.Context, string) (*core.Content, error) {
	panic("unexpected GetByContentHash")
}
func (f *fakeContentRepo) Delete(context.Context, string) error { panic("unexpected Delete") }
func (f *fakeContentRepo) ExistsByURL(context.Context, string) (bool, error) {
	panic("unexpected ExistsByURL")
}
func (f *fakeContentRepo) UpdateValidationStatus(context.Context, string, string, string, string) error {
	panic("unexpected UpdateValidationStatus")
}

var _ repository.ContentRepository = (*fakeContentRepo)(nil)

func doGet(t *testing.T, repo repository.ContentRepository, target string) *httptest.ResponseRecorder {
	t.Helper()

	handler := api.NewRouter(api.Deps{DB: &stubPinger{}, Contents: repo, Log: quietLogger()})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))

	return rec
}

func sampleContent() *core.Content {
	published := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	return &core.Content{
		ID:          "content-1",
		SourceID:    "src-1",
		SourceType:  core.SourceTypeNews,
		Country:     "KR",
		Language:    "ko",
		Title:       "제목",
		Summary:     "요약",
		Body:        "본문 전체",
		Author:      "기자",
		PublishedAt: published,
		Category:    "politics",
		Tags:        []string{"a", "b"},
		URL:         "https://example.com/1",
		ContentHash: "deadbeef",
		WordCount:   120,
		Reliability: 0.8,
		Extra:       map[string]interface{}{"internal": "value"},
		CreatedAt:   published,
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 목록 — 필터 변환
// ─────────────────────────────────────────────────────────────────────────────

func TestContentList_MapsAllFiltersToRepository(t *testing.T) {
	repo := &fakeContentRepo{}

	rec := doGet(t, repo, "/api/contents?country=kr&language=KO&category=politics&source=src-1"+
		"&source_type=news&published_after=2026-01-01T00:00:00Z&published_before=2026-02-01T00:00:00Z"+
		"&tags=a,b&min_reliability=0.5&limit=10&offset=20")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	f := repo.gotFilter
	assert.Equal(t, "KR", f.Country, "country 는 대문자로 정규화돼야 합니다")
	assert.Equal(t, "ko", f.Language, "language 는 소문자로 정규화돼야 합니다")
	assert.Equal(t, "politics", f.Category)
	assert.Equal(t, "src-1", f.Source)
	assert.Equal(t, "news", f.SourceType)
	require.NotNil(t, f.PublishedAfter)
	assert.Equal(t, 2026, f.PublishedAfter.Year())
	require.NotNil(t, f.PublishedBefore)
	assert.Equal(t, time.February, f.PublishedBefore.Month())
	assert.Equal(t, []string{"a", "b"}, f.Tags)
	require.NotNil(t, f.MinReliability)
	assert.InDelta(t, 0.5, *f.MinReliability, 0.0001)
	assert.Equal(t, 10, f.Pagination.Limit)
	assert.Equal(t, 20, f.Pagination.Offset)
}

func TestContentList_NoParams_UsesDefaultPagination(t *testing.T) {
	repo := &fakeContentRepo{}

	rec := doGet(t, repo, "/api/contents")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, model.DefaultPagination().Limit, repo.gotFilter.Pagination.Limit)
	assert.Equal(t, 0, repo.gotFilter.Pagination.Offset)
}

// ─────────────────────────────────────────────────────────────────────────────
// 목록 — 파라미터 검증
// ─────────────────────────────────────────────────────────────────────────────

func TestContentList_InvalidParams_Return400(t *testing.T) {
	tests := []struct {
		name  string
		query string
	}{
		{"country 형식", "country=KOR"},
		{"language 형식", "language=kor"},
		{"source_type 열거", "source_type=blog"},
		{"published_after 형식", "published_after=2026-01-01"},
		{"뒤집힌 날짜 범위", "published_after=2026-02-01T00:00:00Z&published_before=2026-01-01T00:00:00Z"},
		{"min_reliability 범위 초과", "min_reliability=1.5"},
		{"min_reliability 비수치", "min_reliability=high"},
		{"limit 상한 초과", fmt.Sprintf("limit=%d", 201)},
		{"limit 0", "limit=0"},
		{"limit 비수치", "limit=ten"},
		{"offset 음수", "offset=-1"},
		{"빈 tag", "tags=a,,b"},
		{"with_total 비불리언", "with_total=yes-please"},
		{"오타 파라미터", "contry=KR"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &fakeContentRepo{}

			rec := doGet(t, repo, "/api/contents?"+tt.query)

			require.Equal(t, http.StatusBadRequest, rec.Code, "body: %s", rec.Body.String())
			assert.Equal(t, api.CodeBadRequest, decodeError(t, rec.Body).Code)
			assert.Equal(t, model.ContentFilter{}, repo.gotFilter,
				"검증 실패인데 repository 가 호출됐습니다")
		})
	}
}

// 오타를 조용히 무시하면 무필터 전체 결과가 200 으로 돌아가 호출자가 필터가 먹은 줄 안다.
func TestContentList_UnknownParam_NamesTheParam(t *testing.T) {
	rec := doGet(t, &fakeContentRepo{}, "/api/contents?contry=KR&langauge=ko")

	require.Equal(t, http.StatusBadRequest, rec.Code)
	msg := decodeError(t, rec.Body).Message
	assert.Contains(t, msg, "contry")
	assert.Contains(t, msg, "langauge")
}

func TestContentList_LimitAtMax_IsAccepted(t *testing.T) {
	repo := &fakeContentRepo{}

	rec := doGet(t, repo, "/api/contents?limit=200")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, 200, repo.gotFilter.Pagination.Limit)
}

// ─────────────────────────────────────────────────────────────────────────────
// 목록 — 응답 형태
// ─────────────────────────────────────────────────────────────────────────────

func decodeList(t *testing.T, rec *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()

	var body map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))

	return body
}

func TestContentList_OmitsBodyAndInternalFields(t *testing.T) {
	repo := &fakeContentRepo{listResult: []*core.Content{sampleContent()}}

	rec := doGet(t, repo, "/api/contents")
	require.Equal(t, http.StatusOK, rec.Code)

	raw := rec.Body.String()
	assert.NotContains(t, raw, "본문 전체", "목록에 본문이 실렸습니다")
	assert.NotContains(t, raw, "deadbeef", "content_hash 가 노출됐습니다")
	assert.NotContains(t, raw, "internal", "Extra 가 노출됐습니다")

	items, ok := decodeList(t, rec)["items"].([]interface{})
	require.True(t, ok)
	require.Len(t, items, 1)

	item := items[0].(map[string]interface{})
	assert.Equal(t, "content-1", item["id"])
	assert.Equal(t, "제목", item["title"])
	assert.Equal(t, []interface{}{"a", "b"}, item["tags"])
}

// 결과가 없을 때 items 가 null 이면 호출자가 매번 null 검사를 해야 한다.
func TestContentList_Empty_ReturnsEmptyArrayNotNull(t *testing.T) {
	rec := doGet(t, &fakeContentRepo{}, "/api/contents")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"items":[]`)
}

func TestContentList_NilSlices_SerializeAsEmptyArrays(t *testing.T) {
	c := sampleContent()
	c.Tags = nil
	c.ImageURLs = nil

	rec := doGet(t, &fakeContentRepo{listResult: []*core.Content{c}}, "/api/contents")
	require.Equal(t, http.StatusOK, rec.Code)

	item := decodeList(t, rec)["items"].([]interface{})[0].(map[string]interface{})
	assert.Equal(t, []interface{}{}, item["tags"])
	assert.Equal(t, []interface{}{}, item["image_urls"])
}

// PublishedAt zero-value 는 "미상" 이다 — 0001-01-01 로 내보내면 오래된 기사와 구분되지 않는다.
func TestContentList_ZeroPublishedAt_SerializesAsNull(t *testing.T) {
	c := sampleContent()
	c.PublishedAt = time.Time{}

	rec := doGet(t, &fakeContentRepo{listResult: []*core.Content{c}}, "/api/contents")
	require.Equal(t, http.StatusOK, rec.Code)

	item := decodeList(t, rec)["items"].([]interface{})[0].(map[string]interface{})
	assert.Nil(t, item["published_at"])
	assert.NotContains(t, rec.Body.String(), "0001-01-01")
}

// ─────────────────────────────────────────────────────────────────────────────
// 목록 — total
// ─────────────────────────────────────────────────────────────────────────────

func TestContentList_WithoutTotal_DoesNotCallCount(t *testing.T) {
	repo := &fakeContentRepo{}

	rec := doGet(t, repo, "/api/contents")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, 0, repo.countCalls, "total 을 요청하지 않았는데 Count 가 호출됐습니다")
	assert.NotContains(t, rec.Body.String(), "total")
}

func TestContentList_WithTotal_IncludesCount(t *testing.T) {
	repo := &fakeContentRepo{countResult: 42}

	rec := doGet(t, repo, "/api/contents?with_total=true")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, 1, repo.countCalls)
	assert.Equal(t, float64(42), decodeList(t, rec)["total"])
}

func TestContentList_RepositoryError_Returns500WithoutLeak(t *testing.T) {
	repo := &fakeContentRepo{listErr: errors.New(`pq: column "titel" does not exist`)}

	rec := doGet(t, repo, "/api/contents")

	require.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.NotContains(t, rec.Body.String(), "titel")
	assert.Equal(t, api.CodeInternal, decodeError(t, rec.Body).Code)
}

// ─────────────────────────────────────────────────────────────────────────────
// 상세
// ─────────────────────────────────────────────────────────────────────────────

func TestContentDetail_IncludesBody(t *testing.T) {
	repo := &fakeContentRepo{getResult: sampleContent()}

	rec := doGet(t, repo, "/api/contents/content-1")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "content-1", repo.getID)

	var body map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))

	// 임베딩된 summary 필드가 최상위로 평탄화돼야 한다.
	assert.Equal(t, "제목", body["title"])
	assert.Equal(t, "본문 전체", body["body"])
	assert.NotContains(t, rec.Body.String(), "deadbeef")
}

func TestContentDetail_NotFound_Returns404(t *testing.T) {
	repo := &fakeContentRepo{getErr: storage.ErrNotFound}

	rec := doGet(t, repo, "/api/contents/missing")

	require.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, api.CodeNotFound, decodeError(t, rec.Body).Code)
}

// DB 지연을 500 으로 뭉뚱그리면 "코드 결함" 알림과 "DB 부하" 알림이 섞인다.
func TestContentList_QueryTimeout_Returns504(t *testing.T) {
	repo := &fakeContentRepo{listErr: fmt.Errorf("query contents: %w", context.DeadlineExceeded)}

	rec := doGet(t, repo, "/api/contents")

	require.Equal(t, http.StatusGatewayTimeout, rec.Code)
	assert.Equal(t, api.CodeTimeout, decodeError(t, rec.Body).Code)
}

func TestContentDetail_QueryTimeout_Returns504(t *testing.T) {
	repo := &fakeContentRepo{getErr: fmt.Errorf("get content: %w", context.DeadlineExceeded)}

	rec := doGet(t, repo, "/api/contents/content-1")

	require.Equal(t, http.StatusGatewayTimeout, rec.Code)
	assert.Equal(t, api.CodeTimeout, decodeError(t, rec.Body).Code)
}

func TestContentDetail_RepositoryError_Returns500(t *testing.T) {
	repo := &fakeContentRepo{getErr: errors.New("connection reset by peer")}

	rec := doGet(t, repo, "/api/contents/content-1")

	require.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.NotContains(t, rec.Body.String(), "connection reset")
}

func TestContentEndpoints_NilRepository_Return500(t *testing.T) {
	handler := api.NewRouter(api.Deps{DB: &stubPinger{}, Log: quietLogger()})

	for _, target := range []string{"/api/contents", "/api/contents/x"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))

		assert.Equal(t, http.StatusInternalServerError, rec.Code, "target=%s", target)
	}
}
