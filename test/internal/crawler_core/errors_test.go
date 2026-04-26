package core_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	core "issuetracker/internal/crawler/core"
)

func TestCrawlerError_Error(t *testing.T) {
	err := &core.CrawlerError{
		Category: core.ErrCategoryNetwork,
		Code:     "NET_001",
		Message:  "connection failed",
		URL:      "https://example.com",
		Err:      errors.New("underlying error"),
	}

	expected := "[network:NET_001] connection failed: underlying error"
	assert.Equal(t, expected, err.Error())
}

func TestCrawlerError_Unwrap(t *testing.T) {
	underlyingErr := errors.New("underlying error")
	err := &core.CrawlerError{
		Category: core.ErrCategoryNetwork,
		Code:     "NET_001",
		Message:  "connection failed",
		Err:      underlyingErr,
	}

	assert.Equal(t, underlyingErr, err.Unwrap())
}

func TestCrawlerError_Is(t *testing.T) {
	tests := []struct {
		name     string
		err      *core.CrawlerError
		target   error
		expected bool
	}{
		{
			name: "both fields match",
			err: &core.CrawlerError{
				Category: core.ErrCategoryNetwork,
				Code:     "NET_001",
			},
			target: &core.CrawlerError{
				Category: core.ErrCategoryNetwork,
				Code:     "NET_001",
			},
			expected: true,
		},
		{
			name: "category-only target matches by category",
			err: &core.CrawlerError{
				Category: core.ErrCategoryNetwork,
				Code:     "NET_001",
			},
			target: &core.CrawlerError{
				Category: core.ErrCategoryNetwork,
			},
			expected: true,
		},
		{
			name: "code-only target matches by code",
			err: &core.CrawlerError{
				Category: core.ErrCategoryNetwork,
				Code:     "NET_001",
			},
			target: &core.CrawlerError{
				Code: "NET_001",
			},
			expected: true,
		},
		{
			name: "both fields populated, only category matches → false (AND semantics)",
			err: &core.CrawlerError{
				Category: core.ErrCategoryNetwork,
				Code:     "NET_001",
			},
			target: &core.CrawlerError{
				Category: core.ErrCategoryNetwork,
				Code:     "NET_002",
			},
			expected: false,
		},
		{
			name: "both fields populated, only code matches → false (AND semantics)",
			err: &core.CrawlerError{
				Category: core.ErrCategoryNetwork,
				Code:     "NET_001",
			},
			target: &core.CrawlerError{
				Category: core.ErrCategoryParse,
				Code:     "NET_001",
			},
			expected: false,
		},
		{
			name: "different category, code-only target → false",
			err: &core.CrawlerError{
				Category: core.ErrCategoryNetwork,
				Code:     "NET_001",
			},
			target: &core.CrawlerError{
				Code: "PARSE_001",
			},
			expected: false,
		},
		{
			name: "empty target matches nothing",
			err: &core.CrawlerError{
				Category: core.ErrCategoryNetwork,
				Code:     "NET_001",
			},
			target:   &core.CrawlerError{},
			expected: false,
		},
		{
			name: "not crawler error",
			err: &core.CrawlerError{
				Category: core.ErrCategoryNetwork,
				Code:     "NET_001",
			},
			target:   errors.New("some error"),
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.err.Is(tt.target))
		})
	}
}

func TestNewStorageError_InheritsRetryableFromInnerCrawlerError(t *testing.T) {
	// inner DatabaseError(Retryable=false, e.g. constraint violation)
	inner := core.NewDatabaseError(core.CodeDBConstraint, "unique violation", false, errors.New("23505"))

	// 호출자가 retryable=true 로 wrap 해도 inner 의 false 가 보존되어야 함
	wrapped := core.NewStorageError(core.CodeStorageWrite, "save content", true, inner)

	assert.False(t, wrapped.Retryable, "inner *CrawlerError 의 Retryable=false 가 상속되어야 함")
	assert.Equal(t, core.ErrCategoryStorage, wrapped.Category, "Category 는 wrapper(Storage) 가 유지")
}

func TestNewStorageError_UsesDefaultRetryableForNonCrawlerInner(t *testing.T) {
	// inner 가 일반 error 이면 호출자의 default 가 사용됨
	wrapped := core.NewStorageError(core.CodeStorageWrite, "save content", true, errors.New("driver failure"))

	assert.True(t, wrapped.Retryable, "non-CrawlerError inner 에 대해 호출자 default(true) 사용")
}

func TestNewQueueError_InheritsRetryableFromInnerCrawlerError(t *testing.T) {
	inner := core.NewInternalError(core.CodeInternal, "fatal config", errors.New("boom"))

	wrapped := core.NewQueueError(core.CodeQueuePublish, "publish failed", true, inner)

	assert.False(t, wrapped.Retryable, "inner internal error 의 Retryable=false 가 상속되어야 함")
}

func TestNewNetworkError(t *testing.T) {
	url := "https://example.com"
	underlyingErr := errors.New("connection refused")

	err := core.NewNetworkError("NET_001", "failed to connect", url, underlyingErr)

	assert.Equal(t, core.ErrCategoryNetwork, err.Category)
	assert.Equal(t, "NET_001", err.Code)
	assert.Equal(t, url, err.URL)
	assert.True(t, err.Retryable)
	assert.Equal(t, underlyingErr, err.Err)
}

func TestNewRateLimitError(t *testing.T) {
	url := "https://example.com"

	err := core.NewRateLimitError("HTTP_429", "rate limited", url, 429)

	assert.Equal(t, core.ErrCategoryRateLimit, err.Category)
	assert.Equal(t, "HTTP_429", err.Code)
	assert.Equal(t, url, err.URL)
	assert.Equal(t, 429, err.StatusCode)
	assert.True(t, err.Retryable)
}

func TestNewParseError(t *testing.T) {
	url := "https://example.com"
	underlyingErr := errors.New("invalid html")

	err := core.NewParseError("PARSE_001", "failed to parse", url, underlyingErr)

	assert.Equal(t, core.ErrCategoryParse, err.Category)
	assert.Equal(t, "PARSE_001", err.Code)
	assert.Equal(t, url, err.URL)
	assert.False(t, err.Retryable)
	assert.Equal(t, underlyingErr, err.Err)
}

func TestNewNotFoundError(t *testing.T) {
	url := "https://example.com"

	err := core.NewNotFoundError(url)

	assert.Equal(t, core.ErrCategoryNotFound, err.Category)
	assert.Equal(t, "HTTP_404", err.Code)
	assert.Equal(t, url, err.URL)
	assert.Equal(t, 404, err.StatusCode)
	assert.False(t, err.Retryable)
}
