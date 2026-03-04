package core

import "fmt"

// ErrorCategory는 에러의 카테고리를 나타냅니다.
type ErrorCategory string

const (
	// Temporary errors - retry 가능
	ErrCategoryNetwork   ErrorCategory = "network"
	ErrCategoryRateLimit ErrorCategory = "rate_limit"
	ErrCategoryTimeout   ErrorCategory = "timeout"

	// Permanent errors - retry 불가
	ErrCategoryNotFound   ErrorCategory = "not_found"
	ErrCategoryForbidden  ErrorCategory = "forbidden"
	ErrCategoryParse      ErrorCategory = "parse"
	ErrCategoryValidation ErrorCategory = "validation"

	// System errors
	ErrCategoryDatabase ErrorCategory = "database"
	ErrCategoryQueue    ErrorCategory = "queue"
	ErrCategoryStorage  ErrorCategory = "storage"

	// Logic errors
	ErrCategoryConfig   ErrorCategory = "config"
	ErrCategoryInternal ErrorCategory = "internal"
)

// CrawlerError는 크롤러에서 발생하는 에러를 나타냅니다.
// 에러 카테고리, 코드, 메시지를 포함하며 retry 가능 여부를 나타냅니다.
type CrawlerError struct {
	Category   ErrorCategory
	Code       string
	Message    string
	Source     string
	URL        string
	StatusCode int
	Retryable  bool
	Err        error
}

func (e *CrawlerError) Error() string {
	return fmt.Sprintf("[%s:%s] %s: %v", e.Category, e.Code, e.Message, e.Err)
}

func (e *CrawlerError) Unwrap() error {
	return e.Err
}

func (e *CrawlerError) Is(target error) bool {
	t, ok := target.(*CrawlerError)
	if !ok {
		return false
	}
	return e.Code == t.Code || e.Category == t.Category
}

// NewNetworkError는 network 에러를 생성합니다.
func NewNetworkError(code, message, url string, err error) *CrawlerError {
	return &CrawlerError{
		Category:  ErrCategoryNetwork,
		Code:      code,
		Message:   message,
		URL:       url,
		Retryable: true,
		Err:       err,
	}
}

// NewRateLimitError는 rate limit 에러를 생성합니다.
func NewRateLimitError(code, message, url string, statusCode int) *CrawlerError {
	return &CrawlerError{
		Category:   ErrCategoryRateLimit,
		Code:       code,
		Message:    message,
		URL:        url,
		StatusCode: statusCode,
		Retryable:  true,
	}
}

// NewParseError는 parse 에러를 생성합니다.
func NewParseError(code, message, url string, err error) *CrawlerError {
	return &CrawlerError{
		Category:  ErrCategoryParse,
		Code:      code,
		Message:   message,
		URL:       url,
		Retryable: false,
		Err:       err,
	}
}

// NewNotFoundError는 404 에러를 생성합니다.
func NewNotFoundError(url string) *CrawlerError {
	return &CrawlerError{
		Category:   ErrCategoryNotFound,
		Code:       "HTTP_404",
		Message:    "resource not found",
		URL:        url,
		StatusCode: 404,
		Retryable:  false,
	}
}
