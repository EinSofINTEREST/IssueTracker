package goquery_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/processor/fetcher/core"
	gqimpl "issuetracker/internal/processor/fetcher/implementation/goquery"
)

// recordingLimiter 는 Wait 호출을 카운트하고 선택적으로 에러를 주입하는 테스트 stub.
// 실 limiter 는 IP 해석 + token bucket 까지 포함하지만, 본 테스트는 fetcher 가 limiter 를
// 호출하는지 (RPH 미강제 회귀 방지) 만 검증.
type recordingLimiter struct {
	calls atomic.Int64
	err   error
}

func (r *recordingLimiter) Wait(_ context.Context, _ string) error {
	r.calls.Add(1)
	return r.err
}

// TestFetch_RateLimiterCalled 는 GoqueryCrawler 가 fetch 직전 limiter.Wait 를 호출하는지
// 검증합니다 — 이슈 #323 회귀 방지 (production wiring 누락 시 silent 우회).
func TestFetch_RateLimiterCalled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<html><body>ok</body></html>`))
	}))
	defer srv.Close()

	limiter := &recordingLimiter{}
	cfg := core.DefaultConfig()
	cfg.UserAgent = "test-agent"
	info := core.SourceInfo{Name: "test", Country: "KR", Language: "ko"}
	crawler := gqimpl.NewGoqueryCrawlerWithRateLimiter("test", info, cfg, limiter)

	_, err := crawler.Fetch(t.Context(), core.Target{URL: srv.URL, Type: core.TargetTypeArticle})
	require.NoError(t, err)

	assert.Equal(t, int64(1), limiter.calls.Load(), "limiter.Wait 가 정확히 1회 호출되어야 함")
}

// TestFetch_RateLimiterError_PropagatesAsRateLimitError 는 limiter.Wait 실패 시 fetch 가
// rate_limit 카테고리 CrawlerError 로 즉시 전파되는지 검증합니다.
func TestFetch_RateLimiterError_PropagatesAsRateLimitError(t *testing.T) {
	// 핸들러가 절대 호출되면 안 됨 — limiter 거부 시점에 fetch 가 차단되어야 함.
	var serverHits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serverHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	wantErr := errors.New("rate limit refused")
	limiter := &recordingLimiter{err: wantErr}
	cfg := core.DefaultConfig()
	info := core.SourceInfo{Name: "test", Country: "KR", Language: "ko"}
	crawler := gqimpl.NewGoqueryCrawlerWithRateLimiter("test", info, cfg, limiter)

	_, err := crawler.Fetch(t.Context(), core.Target{URL: srv.URL, Type: core.TargetTypeArticle})
	require.Error(t, err)

	var ce *core.CrawlerError
	require.ErrorAs(t, err, &ce, "limiter 에러는 CrawlerError 로 wrap")
	assert.Equal(t, core.ErrCategoryRateLimit, ce.Category)
	assert.True(t, errors.Is(err, wantErr), "원본 limiter 에러가 chain 에 보존")
	assert.Equal(t, int64(0), serverHits.Load(), "limiter 거부 시 실제 HTTP 호출 발생 X")
}

// TestFetch_NilRateLimiter_BypassesGracefully 는 limiter 가 nil 이면 기존 동작과 동일하게
// 진행되는지 검증합니다 — example / test 환경 호환성.
func TestFetch_NilRateLimiter_BypassesGracefully(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<html><body>ok</body></html>`))
	}))
	defer srv.Close()

	cfg := core.DefaultConfig()
	info := core.SourceInfo{Name: "test", Country: "KR", Language: "ko"}
	crawler := gqimpl.NewGoqueryCrawler("test", info, cfg) // nil limiter

	_, err := crawler.Fetch(t.Context(), core.Target{URL: srv.URL, Type: core.TargetTypeArticle})
	require.NoError(t, err)
}
