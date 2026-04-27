package core_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	core "issuetracker/internal/crawler/core"
)

const testURL = "https://example.com/article"

// TestCheckHTTPStatus_TableDriven 는 상태 코드별 분기 동작을 검증합니다.
// 이슈 #75 의 4-way 분기를 정확히 보존하는지 회귀 잠금.
func TestCheckHTTPStatus_TableDriven(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		wantNil  bool
		wantCode string // CrawlerError.Code 검증, wantNil=true 시 무시
		wantCat  core.ErrorCategory
		retry    bool
	}{
		{"200 OK → nil", 200, true, "", "", false},
		{"204 No Content → nil", 204, true, "", "", false},
		{"301 Redirect → nil", 301, true, "", "", false},
		{"399 Edge → nil", 399, true, "", "", false},
		{"400 Bad Request → ClientError", 400, false, "HTTP_400", core.ErrCategoryInternal, false},
		{"401 Unauthorized → ClientError", 401, false, "HTTP_401", core.ErrCategoryInternal, false},
		{"403 Forbidden → ClientError", 403, false, "HTTP_403", core.ErrCategoryInternal, false},
		{"404 Not Found → NotFoundError", 404, false, "HTTP_404", core.ErrCategoryNotFound, false},
		{"429 Too Many Requests → RateLimitError", 429, false, "HTTP_429", core.ErrCategoryRateLimit, true},
		{"499 → ClientError", 499, false, "HTTP_499", core.ErrCategoryInternal, false},
		{"500 Internal Server Error → ServerError", 500, false, "HTTP_500", core.ErrCategoryNetwork, true},
		{"502 Bad Gateway → ServerError", 502, false, "HTTP_502", core.ErrCategoryNetwork, true},
		{"503 Service Unavailable → ServerError", 503, false, "HTTP_503", core.ErrCategoryNetwork, true},
		{"599 Edge → ServerError", 599, false, "HTTP_599", core.ErrCategoryNetwork, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := core.CheckHTTPStatus(testURL, tt.status)

			if tt.wantNil {
				assert.NoError(t, err)
				return
			}

			require.Error(t, err)
			var ce *core.CrawlerError
			require.True(t, errors.As(err, &ce), "에러는 CrawlerError 타입이어야 함")
			assert.Equal(t, tt.wantCode, ce.Code)
			assert.Equal(t, tt.wantCat, ce.Category)
			assert.Equal(t, tt.retry, ce.Retryable)
			assert.Equal(t, testURL, ce.URL)
			assert.Equal(t, tt.status, ce.StatusCode)
		})
	}
}

// TestCheckHTTPStatus_BoundaryAt400 는 399→400 경계에서 분기가 정확히 전환되는지 검증.
func TestCheckHTTPStatus_BoundaryAt400(t *testing.T) {
	assert.NoError(t, core.CheckHTTPStatus(testURL, 399))
	assert.Error(t, core.CheckHTTPStatus(testURL, 400))
}

// TestCheckHTTPStatus_BoundaryAt500 는 499→500 경계에서 ClientError → ServerError
// 카테고리 전환을 검증.
func TestCheckHTTPStatus_BoundaryAt500(t *testing.T) {
	err499 := core.CheckHTTPStatus(testURL, 499)
	err500 := core.CheckHTTPStatus(testURL, 500)
	require.Error(t, err499)
	require.Error(t, err500)

	var ce499, ce500 *core.CrawlerError
	require.True(t, errors.As(err499, &ce499))
	require.True(t, errors.As(err500, &ce500))

	assert.Equal(t, core.ErrCategoryInternal, ce499.Category)
	assert.False(t, ce499.Retryable, "4xx 클라이언트 에러는 retry 불가")

	assert.Equal(t, core.ErrCategoryNetwork, ce500.Category)
	assert.True(t, ce500.Retryable, "5xx 서버 에러는 retry 가능")
}
