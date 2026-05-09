package search_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/processor/fetcher/domain/search"
	"issuetracker/pkg/logger"
)

func newTestLogger(t *testing.T) *logger.Logger {
	t.Helper()
	return logger.New(logger.Config{Level: "error"})
}

func TestCSEClient_NewClient_RequiresAPIKeyAndCX(t *testing.T) {
	t.Parallel()
	log := newTestLogger(t)

	_, err := search.NewCSEClient(search.CSEClientOptions{CX: "cx"}, log)
	assert.Error(t, err, "missing api key should error")

	_, err = search.NewCSEClient(search.CSEClientOptions{APIKey: "k"}, log)
	assert.Error(t, err, "missing CX should error")

	c, err := search.NewCSEClient(search.CSEClientOptions{APIKey: "k", CX: "cx"}, log)
	require.NoError(t, err)
	assert.NotNil(t, c)
}

func TestCSEClient_Search_SinglePage(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "test-key", r.URL.Query().Get("key"))
		assert.Equal(t, "test-cx", r.URL.Query().Get("cx"))
		assert.Equal(t, "ai 규제", r.URL.Query().Get("q"))
		assert.Equal(t, "1", r.URL.Query().Get("start"))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"items":[{"link":"https://example.com/a"},{"link":"https://example.com/b"}]}`))
	}))
	defer server.Close()

	c, err := search.NewCSEClient(search.CSEClientOptions{APIKey: "test-key", CX: "test-cx", BaseURL: server.URL}, newTestLogger(t))
	require.NoError(t, err)

	urls, err := c.Search(context.Background(), "ai 규제", search.SearchOptions{MaxResults: 10})
	require.NoError(t, err)
	assert.Equal(t, []string{"https://example.com/a", "https://example.com/b"}, urls)
}

func TestCSEClient_Search_PaginationAndDedup(t *testing.T) {
	t.Parallel()

	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		start, _ := strconv.Atoi(r.URL.Query().Get("start"))
		// 첫 페이지: 10개 + nextPage. 둘째 페이지: 5개 (그 중 1개는 첫 페이지 중복) + no nextPage.
		w.WriteHeader(http.StatusOK)
		switch start {
		case 1:
			body := `{"items":[`
			for i := 0; i < 10; i++ {
				if i > 0 {
					body += ","
				}
				body += fmt.Sprintf(`{"link":"https://example.com/p1-%d"}`, i)
			}
			body += `],"queries":{"nextPage":[{"startIndex":11}]}}`
			_, _ = w.Write([]byte(body))
		case 11:
			// 1개 중복 (p1-0) + 4개 신규.
			body := `{"items":[{"link":"https://example.com/p1-0"}`
			for i := 0; i < 4; i++ {
				body += fmt.Sprintf(`,{"link":"https://example.com/p2-%d"}`, i)
			}
			body += `]}`
			_, _ = w.Write([]byte(body))
		default:
			t.Fatalf("unexpected start=%d on call %d", start, n)
		}
	}))
	defer server.Close()

	c, err := search.NewCSEClient(search.CSEClientOptions{APIKey: "k", CX: "cx", BaseURL: server.URL}, newTestLogger(t))
	require.NoError(t, err)

	urls, err := c.Search(context.Background(), "kw", search.SearchOptions{MaxResults: 20})
	require.NoError(t, err)

	assert.Equal(t, int32(2), atomic.LoadInt32(&calls), "두 페이지 호출 발생")
	assert.Len(t, urls, 14, "10개 + 4개 신규 (1개 중복 dedup)")
}

func TestCSEClient_Search_RateLimitedRetryable(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"code":429,"message":"quota exceeded"}}`))
	}))
	defer server.Close()

	c, err := search.NewCSEClient(search.CSEClientOptions{APIKey: "k", CX: "cx", BaseURL: server.URL}, newTestLogger(t))
	require.NoError(t, err)

	_, err = c.Search(context.Background(), "kw", search.SearchOptions{MaxResults: 10})
	require.Error(t, err)

	var cseErr *search.CSEError
	require.True(t, errors.As(err, &cseErr))
	assert.Equal(t, http.StatusTooManyRequests, cseErr.StatusCode)
	assert.True(t, cseErr.Retryable, "429 는 retryable")
	assert.Contains(t, cseErr.Message, "quota exceeded")
}

func TestCSEClient_Search_AuthErrorNonRetryable(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"code":403,"message":"API key invalid"}}`))
	}))
	defer server.Close()

	c, err := search.NewCSEClient(search.CSEClientOptions{APIKey: "k", CX: "cx", BaseURL: server.URL}, newTestLogger(t))
	require.NoError(t, err)

	_, err = c.Search(context.Background(), "kw", search.SearchOptions{MaxResults: 10})
	require.Error(t, err)

	var cseErr *search.CSEError
	require.True(t, errors.As(err, &cseErr))
	assert.Equal(t, http.StatusForbidden, cseErr.StatusCode)
	assert.False(t, cseErr.Retryable, "4xx 인증 에러는 non-retryable")
}

func TestCSEClient_Search_ServerErrorRetryable(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"code":500,"message":"backend error"}}`))
	}))
	defer server.Close()

	c, err := search.NewCSEClient(search.CSEClientOptions{APIKey: "k", CX: "cx", BaseURL: server.URL}, newTestLogger(t))
	require.NoError(t, err)

	_, err = c.Search(context.Background(), "kw", search.SearchOptions{MaxResults: 10})
	require.Error(t, err)

	var cseErr *search.CSEError
	require.True(t, errors.As(err, &cseErr))
	assert.True(t, cseErr.Retryable, "5xx 는 retryable")
}

func TestCSEClient_Search_PartialFailureReturnsPartialResults(t *testing.T) {
	t.Parallel()

	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		start, _ := strconv.Atoi(r.URL.Query().Get("start"))
		if start == 1 {
			body := `{"items":[`
			for i := 0; i < 10; i++ {
				if i > 0 {
					body += ","
				}
				body += fmt.Sprintf(`{"link":"https://example.com/a-%d"}`, i)
			}
			body += `],"queries":{"nextPage":[{"startIndex":11}]}}`
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(body))
			return
		}
		// 둘째 페이지에서 5xx — 첫 페이지 결과는 보존되어야 함.
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"code":500,"message":"page2 failure"}}`))
		_ = n
	}))
	defer server.Close()

	c, err := search.NewCSEClient(search.CSEClientOptions{APIKey: "k", CX: "cx", BaseURL: server.URL}, newTestLogger(t))
	require.NoError(t, err)

	urls, err := c.Search(context.Background(), "kw", search.SearchOptions{MaxResults: 30})
	require.NoError(t, err, "둘째 페이지 실패는 부분 결과 + nil error 로 반환")
	assert.Len(t, urls, 10, "첫 페이지 10개 보존")
}

func TestCSEClient_Search_DateRangeAndLanguageInQuery(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "d365", r.URL.Query().Get("dateRestrict"))
		assert.Equal(t, "lang_ko", r.URL.Query().Get("lr"))
		assert.Equal(t, "kr", r.URL.Query().Get("gl"))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"items":[]}`))
	}))
	defer server.Close()

	c, err := search.NewCSEClient(search.CSEClientOptions{APIKey: "k", CX: "cx", BaseURL: server.URL}, newTestLogger(t))
	require.NoError(t, err)

	_, err = c.Search(context.Background(), "kw", search.SearchOptions{
		MaxResults:    10,
		DateRangeDays: 365,
		Language:      "ko",
		Region:        "kr",
	})
	require.NoError(t, err)
}

func TestCSEClient_Search_ContextCancelInterrupts(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 의도적 지연 — context cancel 이 먼저 끊을 수 있도록.
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"items":[]}`))
	}))
	defer server.Close()

	c, err := search.NewCSEClient(search.CSEClientOptions{APIKey: "k", CX: "cx", BaseURL: server.URL, Timeout: 1 * time.Second}, newTestLogger(t))
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	_, err = c.Search(ctx, "kw", search.SearchOptions{MaxResults: 10})
	require.Error(t, err)
}
