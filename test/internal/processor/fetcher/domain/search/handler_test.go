package search_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/processor/fetcher/core"
	"issuetracker/internal/processor/fetcher/domain/search"
	"issuetracker/internal/storage"
)

// memSearchKeywordRepo 는 SearchKeywordRepository 의 in-memory 구현 (handler 테스트용 fixture).
type memSearchKeywordRepo struct {
	mu      sync.Mutex
	rows    []*storage.SearchKeywordRecord
	marked  map[int64]time.Time
	listErr error
	markErr error
}

func (r *memSearchKeywordRepo) ListEnabled(_ context.Context, language, region string) ([]*storage.SearchKeywordRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.listErr != nil {
		return nil, r.listErr
	}
	var out []*storage.SearchKeywordRecord
	for _, rec := range r.rows {
		if !rec.Enabled {
			continue
		}
		if language != "" && rec.Language != language {
			continue
		}
		if region != "" && rec.Region != region {
			continue
		}
		copyRec := *rec
		out = append(out, &copyRec)
	}
	return out, nil
}

func (r *memSearchKeywordRepo) Insert(_ context.Context, _ *storage.SearchKeywordRecord) error {
	return errors.New("not used in test")
}

func (r *memSearchKeywordRepo) Update(_ context.Context, _ *storage.SearchKeywordRecord) error {
	return errors.New("not used in test")
}

func (r *memSearchKeywordRepo) Delete(_ context.Context, _ int64) error {
	return errors.New("not used in test")
}

func (r *memSearchKeywordRepo) MarkSearched(_ context.Context, id int64, t time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.markErr != nil {
		return r.markErr
	}
	if r.marked == nil {
		r.marked = map[int64]time.Time{}
	}
	r.marked[id] = t
	return nil
}

func (r *memSearchKeywordRepo) markedIDs() []int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	ids := make([]int64, 0, len(r.marked))
	for id := range r.marked {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// recordingPublisher 는 JobPublisher 의 호출을 기록하는 in-memory mock.
type recordingPublisher struct {
	mu    sync.Mutex
	calls []publishCall
	err   error
}

type publishCall struct {
	CrawlerName string
	URLs        []string
	TargetType  core.TargetType
	Timeout     time.Duration
}

func (p *recordingPublisher) Publish(_ context.Context, crawlerName string, urls []string, targetType core.TargetType, timeout time.Duration) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return p.err
	}
	urlsCopy := make([]string, len(urls))
	copy(urlsCopy, urls)
	p.calls = append(p.calls, publishCall{
		CrawlerName: crawlerName,
		URLs:        urlsCopy,
		TargetType:  targetType,
		Timeout:     timeout,
	})
	return nil
}

func (p *recordingPublisher) snapshot() []publishCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]publishCall, len(p.calls))
	copy(out, p.calls)
	return out
}

// stubCSEServer 는 keyword 별 응답을 미리 정의한 fake CSE endpoint 입니다.
func stubCSEServer(t *testing.T, byKeyword map[string][]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("q")
		urls, ok := byKeyword[q]
		if !ok {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"items":[]}`))
			return
		}
		body := `{"items":[`
		for i, u := range urls {
			if i > 0 {
				body += ","
			}
			body += fmt.Sprintf(`{"link":%q}`, u)
		}
		body += `]}`
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}))
}

func newTestHandler(t *testing.T, server *httptest.Server, repo storage.SearchKeywordRepository, pub search.JobPublisher) *search.SearchHandler {
	t.Helper()
	log := newTestLogger(t)
	c, err := search.NewCSEClient(search.CSEClientOptions{APIKey: "k", CX: "cx", BaseURL: server.URL}, log)
	require.NoError(t, err)
	h, err := search.NewSearchHandler(search.SearchHandlerOptions{
		Client:      c,
		KeywordRepo: repo,
		Publisher:   pub,
	}, log)
	require.NoError(t, err)
	return h
}

func TestSearchHandler_Handle_FanoutByHostAndDedup(t *testing.T) {
	t.Parallel()

	byKw := map[string][]string{
		"kw1": {
			"https://example.com/a",
			"https://other.com/x",
		},
		"kw2": {
			"https://example.com/a", // duplicate of kw1
			"https://example.com/b",
			"https://third.com/y",
		},
	}
	server := stubCSEServer(t, byKw)
	defer server.Close()

	repo := &memSearchKeywordRepo{
		rows: []*storage.SearchKeywordRecord{
			{ID: 1, Keyword: "kw1", Enabled: true},
			{ID: 2, Keyword: "kw2", Enabled: true},
		},
	}
	pub := &recordingPublisher{}
	h := newTestHandler(t, server, repo, pub)

	job := &core.CrawlJob{
		ID:          "job-1",
		CrawlerName: "customsearch.googleapis.com",
		Target: core.Target{
			URL:  "https://customsearch.googleapis.com/customsearch/v1",
			Type: core.TargetTypeSearchResults,
			Metadata: map[string]interface{}{
				"engine": "google_cse",
			},
		},
	}
	out, err := h.Handle(context.Background(), job)
	require.NoError(t, err)
	assert.Nil(t, out)

	calls := pub.snapshot()
	require.Len(t, calls, 3, "host 3개 (example.com / other.com / third.com)")

	byHost := map[string]publishCall{}
	for _, c := range calls {
		byHost[c.CrawlerName] = c
	}
	assert.ElementsMatch(t, []string{"https://example.com/a", "https://example.com/b"}, byHost["example.com"].URLs)
	assert.Equal(t, []string{"https://other.com/x"}, byHost["other.com"].URLs)
	assert.Equal(t, []string{"https://third.com/y"}, byHost["third.com"].URLs)

	for _, c := range calls {
		assert.Equal(t, core.TargetTypeArticle, c.TargetType)
	}

	assert.Equal(t, []int64{1, 2}, repo.markedIDs(), "성공 keyword 모두 last_searched_at 갱신")
}

func TestSearchHandler_Handle_NoEnabledKeywords(t *testing.T) {
	t.Parallel()

	server := stubCSEServer(t, nil)
	defer server.Close()

	repo := &memSearchKeywordRepo{
		rows: []*storage.SearchKeywordRecord{
			{ID: 1, Keyword: "off", Enabled: false},
		},
	}
	pub := &recordingPublisher{}
	h := newTestHandler(t, server, repo, pub)

	out, err := h.Handle(context.Background(), &core.CrawlJob{Target: core.Target{Type: core.TargetTypeSearchResults}})
	require.NoError(t, err)
	assert.Nil(t, out)
	assert.Empty(t, pub.snapshot(), "publish 호출 없음")
}

func TestSearchHandler_Handle_SkipUnsupportedEngine(t *testing.T) {
	t.Parallel()

	server := stubCSEServer(t, map[string][]string{"kw": {"https://example.com/a"}})
	defer server.Close()

	repo := &memSearchKeywordRepo{rows: []*storage.SearchKeywordRecord{{ID: 1, Keyword: "kw", Enabled: true}}}
	pub := &recordingPublisher{}
	h := newTestHandler(t, server, repo, pub)

	job := &core.CrawlJob{
		Target: core.Target{
			Type:     core.TargetTypeSearchResults,
			Metadata: map[string]interface{}{"engine": "bing_search"},
		},
	}
	out, err := h.Handle(context.Background(), job)
	require.NoError(t, err)
	assert.Nil(t, out)
	assert.Empty(t, pub.snapshot(), "다른 engine 은 skip")
}

func TestSearchHandler_Handle_KeywordSkipsOnTransientError(t *testing.T) {
	t.Parallel()

	// failing_kw 는 5xx, ok_kw 는 정상.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("q")
		if q == "failing_kw" {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"code":500,"message":"backend"}}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"items":[{"link":"https://example.com/a"}]}`))
	}))
	defer server.Close()

	repo := &memSearchKeywordRepo{
		rows: []*storage.SearchKeywordRecord{
			{ID: 1, Keyword: "failing_kw", Enabled: true},
			{ID: 2, Keyword: "ok_kw", Enabled: true},
		},
	}
	pub := &recordingPublisher{}
	h := newTestHandler(t, server, repo, pub)

	out, err := h.Handle(context.Background(), &core.CrawlJob{Target: core.Target{Type: core.TargetTypeSearchResults}})
	require.NoError(t, err)
	assert.Nil(t, out)

	calls := pub.snapshot()
	require.Len(t, calls, 1)
	assert.Equal(t, []string{"https://example.com/a"}, calls[0].URLs)

	assert.Equal(t, []int64{2}, repo.markedIDs(), "성공한 keyword 만 mark")
}

func TestSearchHandler_Handle_NonRetryableAbortsCycle(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"code":403,"message":"key invalid"}}`))
	}))
	defer server.Close()

	repo := &memSearchKeywordRepo{
		rows: []*storage.SearchKeywordRecord{
			{ID: 1, Keyword: "kw1", Enabled: true},
			{ID: 2, Keyword: "kw2", Enabled: true},
		},
	}
	pub := &recordingPublisher{}
	h := newTestHandler(t, server, repo, pub)

	out, err := h.Handle(context.Background(), &core.CrawlJob{Target: core.Target{Type: core.TargetTypeSearchResults}})
	require.NoError(t, err, "non-retryable 도 nil error 반환 — 다음 cycle 까지 자연 중단")
	assert.Nil(t, out)
	assert.Empty(t, pub.snapshot(), "publish 호출 없음")
	assert.Empty(t, repo.markedIDs(), "non-retryable 시 mark 안 됨")
}

func TestSearchHandler_Handle_ListEnabledFailsBubbles(t *testing.T) {
	t.Parallel()

	server := stubCSEServer(t, nil)
	defer server.Close()

	repo := &memSearchKeywordRepo{listErr: errors.New("db down")}
	pub := &recordingPublisher{}
	h := newTestHandler(t, server, repo, pub)

	_, err := h.Handle(context.Background(), &core.CrawlJob{Target: core.Target{Type: core.TargetTypeSearchResults}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "list enabled keywords")
}

func TestSearchHandler_Handle_NilJobReturnsError(t *testing.T) {
	t.Parallel()

	server := stubCSEServer(t, nil)
	defer server.Close()

	repo := &memSearchKeywordRepo{}
	pub := &recordingPublisher{}
	h := newTestHandler(t, server, repo, pub)

	_, err := h.Handle(context.Background(), nil)
	require.Error(t, err)
}

func TestSearchHandler_Handle_PerKeywordLanguageRegionPropagated(t *testing.T) {
	t.Parallel()

	var (
		mu     sync.Mutex
		params []string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		params = append(params, fmt.Sprintf("q=%s lr=%s gl=%s", r.URL.Query().Get("q"), r.URL.Query().Get("lr"), r.URL.Query().Get("gl")))
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"items":[]}`))
	}))
	defer server.Close()

	repo := &memSearchKeywordRepo{
		rows: []*storage.SearchKeywordRecord{
			{ID: 1, Keyword: "ko_kw", Enabled: true, Language: "ko", Region: "kr"},
			{ID: 2, Keyword: "en_kw", Enabled: true, Language: "en", Region: "us"},
		},
	}
	pub := &recordingPublisher{}
	h := newTestHandler(t, server, repo, pub)

	_, err := h.Handle(context.Background(), &core.CrawlJob{Target: core.Target{Type: core.TargetTypeSearchResults}})
	require.NoError(t, err)

	mu.Lock()
	defer mu.Unlock()
	sort.Strings(params)
	assert.Equal(t, []string{
		"q=en_kw lr=lang_en gl=us",
		"q=ko_kw lr=lang_ko gl=kr",
	}, params)
}
