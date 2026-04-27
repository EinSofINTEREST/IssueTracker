package core_test

import (
	"context"
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

// Err=nil 인 경우 ": <nil>" 깨진 표현 없이 prefix 만 출력하는지 검증
func TestCrawlerError_Error_NilWrappedErr(t *testing.T) {
	err := &core.CrawlerError{
		Category: core.ErrCategoryNotFound,
		Code:     "HTTP_404",
		Message:  "resource not found",
		URL:      "https://example.com",
		Err:      nil,
	}

	expected := "[not_found:HTTP_404] resource not found"
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

// TestCrawlerError_ErrorsIs_ContextCanceled_ChainUnwrap:
// 이슈 #72 회귀 방지 — graceful shutdown 처리는 핸들러·풀 곳곳에서
// errors.Is(err, context.Canceled) 로 cancel 케이스를 식별합니다.
// CrawlerError 가 Unwrap() 을 통해 chain 을 보존해야 이 식별이 동작합니다.
//
// chromedp CDP_006 처럼 errors.Join(runErr, captureErr) 형태로 두 에러를
// 묶은 경우에도 chain 을 따라 매칭되어야 합니다.
func TestCrawlerError_ErrorsIs_ContextCanceled_ChainUnwrap(t *testing.T) {
	t.Run("direct context.Canceled wrap", func(t *testing.T) {
		err := &core.CrawlerError{
			Category: core.ErrCategoryNetwork,
			Code:     "CDP_002",
			Message:  "failed to render page",
			Err:      context.Canceled,
		}
		assert.True(t, errors.Is(err, context.Canceled),
			"CrawlerError 는 errors.Is 가 context.Canceled 까지 chain 을 따라가야 함")
	})

	t.Run("errors.Join(deadline, canceled) — CDP_006 패턴", func(t *testing.T) {
		joined := errors.Join(context.DeadlineExceeded, context.Canceled)
		err := &core.CrawlerError{
			Category: core.ErrCategoryTimeout,
			Code:     "CDP_006",
			Message:  "render timeout and graceful capture failed",
			Err:      joined,
		}
		assert.True(t, errors.Is(err, context.Canceled),
			"errors.Join 으로 묶인 cancel 도 chain 을 따라 매칭되어야 함")
		assert.True(t, errors.Is(err, context.DeadlineExceeded),
			"동시에 deadline 도 매칭되어야 함")
	})

	t.Run("non-canceled error does not match", func(t *testing.T) {
		err := &core.CrawlerError{
			Category: core.ErrCategoryNetwork,
			Code:     "NET_001",
			Message:  "connection refused",
			Err:      errors.New("dial tcp: connection refused"),
		}
		assert.False(t, errors.Is(err, context.Canceled),
			"실제 네트워크 에러는 context.Canceled 와 매칭되면 안 됨")
	})
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

func TestNewTimeoutError(t *testing.T) {
	url := "https://example.com"
	cause := errors.New("deadline exceeded")

	err := core.NewTimeoutError(core.CodeNetTimeout, "request timeout", url, cause)

	assert.Equal(t, core.ErrCategoryTimeout, err.Category)
	assert.Equal(t, core.CodeNetTimeout, err.Code)
	assert.Equal(t, url, err.URL)
	assert.True(t, err.Retryable)
	assert.Equal(t, cause, err.Err)
	assert.Equal(t, cause, err.Unwrap())
}

func TestNewForbiddenError(t *testing.T) {
	url := "https://example.com"

	err := core.NewForbiddenError(url)

	assert.Equal(t, core.ErrCategoryForbidden, err.Category)
	assert.Equal(t, "HTTP_403", err.Code)
	assert.Equal(t, url, err.URL)
	assert.Equal(t, 403, err.StatusCode)
	assert.False(t, err.Retryable)
}

func TestNewValidationError(t *testing.T) {
	cause := errors.New("title too short")

	err := core.NewValidationError(core.CodeValContentShort, "title length below minimum", cause)

	assert.Equal(t, core.ErrCategoryValidation, err.Category)
	assert.Equal(t, core.CodeValContentShort, err.Code)
	assert.False(t, err.Retryable)
	assert.Equal(t, cause, err.Err)
}

func TestNewDatabaseError(t *testing.T) {
	cause := errors.New("connection refused")

	// 일반 inner error → 호출자 default 사용
	err := core.NewDatabaseError(core.CodeDBConnFail, "connection failed", true, cause)
	assert.Equal(t, core.ErrCategoryDatabase, err.Category)
	assert.Equal(t, core.CodeDBConnFail, err.Code)
	assert.True(t, err.Retryable)
	assert.Equal(t, cause, err.Err)
}

func TestNewQueueError(t *testing.T) {
	cause := errors.New("malformed message")

	err := core.NewQueueError(core.CodeQueueMalformed, "invalid message", false, cause)
	assert.Equal(t, core.ErrCategoryQueue, err.Category)
	assert.Equal(t, core.CodeQueueMalformed, err.Code)
	assert.False(t, err.Retryable)
}

func TestNewStorageError(t *testing.T) {
	cause := errors.New("io failure")

	err := core.NewStorageError(core.CodeStorageWrite, "save content", true, cause)
	assert.Equal(t, core.ErrCategoryStorage, err.Category)
	assert.Equal(t, core.CodeStorageWrite, err.Code)
	assert.True(t, err.Retryable)
}

func TestNewConfigError(t *testing.T) {
	cause := errors.New("invalid yaml")

	err := core.NewConfigError(core.CodeConfigParse, "failed to parse config", cause)
	assert.Equal(t, core.ErrCategoryConfig, err.Category)
	assert.Equal(t, core.CodeConfigParse, err.Code)
	assert.False(t, err.Retryable, "config error 는 fail-fast 의도로 항상 non-retryable")
	assert.Equal(t, cause, err.Err)
}

func TestNewInternalError(t *testing.T) {
	cause := errors.New("nil pointer")

	err := core.NewInternalError(core.CodeInternal, "internal failure", cause)
	assert.Equal(t, core.ErrCategoryInternal, err.Category)
	assert.Equal(t, core.CodeInternal, err.Code)
	assert.False(t, err.Retryable)
}
