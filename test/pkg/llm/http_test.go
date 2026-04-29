package llm_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/pkg/llm"
)

// TestHTTPClient_PostJSON_Success 는 2xx 응답이 raw bytes 로 반환됨을 검증합니다.
func TestHTTPClient_PostJSON_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := llm.NewHTTPClient(5 * time.Second)
	body, err := c.PostJSON(context.Background(), "test", srv.URL, nil, map[string]string{"hello": "world"})
	require.NoError(t, err)
	assert.JSONEq(t, `{"ok":true}`, string(body))
}

func TestHTTPClient_PostJSON_HeadersForwarded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer xyz", r.Header.Get("Authorization"))
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := llm.NewHTTPClient(5 * time.Second)
	_, err := c.PostJSON(context.Background(), "test", srv.URL,
		map[string]string{"Authorization": "Bearer xyz"}, struct{}{})
	require.NoError(t, err)
}

// TestHTTPClient_PostJSON_StatusMappings 는 status → ErrorCode 매핑을 모두 검증합니다.
func TestHTTPClient_PostJSON_StatusMappings(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		body      string
		wantCode  llm.ErrorCode
		wantRetry bool
	}{
		{"401 → auth", 401, `{"error":"unauthorized"}`, llm.ErrCodeAuth, false},
		{"403 → auth", 403, "forbidden", llm.ErrCodeAuth, false},
		{"429 → rate_limit retryable", 429, `{"error":"rate limit"}`, llm.ErrCodeRateLimit, true},
		{"500 → server retryable", 500, "internal", llm.ErrCodeServer, true},
		{"503 → server retryable", 503, "unavailable", llm.ErrCodeServer, true},
		{"400 generic → bad_request", 400, `{"error":"invalid"}`, llm.ErrCodeBadRequest, false},
		{"400 context window keyword → context_limit", 400, `{"error":"maximum context length 8000 tokens"}`, llm.ErrCodeContextLimit, false},
		{"404 → bad_request", 404, "not found", llm.ErrCodeBadRequest, false},
		{"418 → unknown? actually bad_request 4xx", 418, "teapot", llm.ErrCodeBadRequest, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			c := llm.NewHTTPClient(5 * time.Second)
			_, err := c.PostJSON(context.Background(), "test-provider", srv.URL, nil, struct{}{})
			require.Error(t, err)

			var lerr *llm.Error
			require.True(t, errors.As(err, &lerr))
			assert.Equal(t, tc.wantCode, lerr.Code, "status %d", tc.status)
			assert.Equal(t, "test-provider", lerr.Provider)
			assert.Equal(t, tc.wantRetry, lerr.Retryable, "status %d retryable", tc.status)
		})
	}
}

// TestHTTPClient_PostJSON_ContextCancelled 는 ctx 취소 시 network 에러 반환을 검증합니다.
func TestHTTPClient_PostJSON_ContextCancelled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 응답을 일부러 지연시켜 ctx 취소가 먼저 발화하도록.
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
			w.WriteHeader(200)
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	c := llm.NewHTTPClient(5 * time.Second)
	_, err := c.PostJSON(ctx, "test", srv.URL, nil, struct{}{})
	require.Error(t, err)
	var lerr *llm.Error
	require.True(t, errors.As(err, &lerr))
	assert.Equal(t, llm.ErrCodeNetwork, lerr.Code)
}

// TestHTTPClient_PostJSON_BadResponseEncoding 은 marshal 실패 케이스 — function value (JSON 비호환) 전달.
func TestHTTPClient_PostJSON_BadEncodingBody(t *testing.T) {
	c := llm.NewHTTPClient(5 * time.Second)
	// JSON 인코딩 불가능한 값 (function) 으로 marshal 실패 유도
	badBody := func() {}
	_, err := c.PostJSON(context.Background(), "test", "http://localhost:1", nil, badBody)
	require.Error(t, err)
	var lerr *llm.Error
	require.True(t, errors.As(err, &lerr))
	assert.Equal(t, llm.ErrCodeBadRequest, lerr.Code)
}

// 이하 helper — 다른 provider test 들이 mock JSON 작성 시 사용
func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}
