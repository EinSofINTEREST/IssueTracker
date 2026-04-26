// Package core 의 errors.go는 시스템 전반에서 사용하는 구조화 에러 타입과 카테고리/코드 체계를 정의합니다.
// 타입 이름은 역사적 사유로 CrawlerError 이지만, 크롤러뿐 아니라 storage/queue/processor 등
// 다른 internal/ 레이어 경계에서도 동일하게 사용합니다(레이어별 사용 규칙은
// .claude/rules/04-error-handling.md 참고).
//
// Although the type is named CrawlerError for historical reasons, it is the system-wide
// structured error used at any internal/ boundary (crawler, storage, queue, processor, ...).
package core

import "fmt"

// ErrorCategory는 에러의 카테고리를 나타냅니다. retry 가능 여부, 라우팅 규칙 등은 카테고리 기준으로 결정됩니다.
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

// 에러 코드 상수 — .claude/rules/04-error-handling.md 의 코드 카탈로그와 1:1 대응합니다.
// 코드는 로그/메트릭에서 카디널리티가 안정적이도록 문자열 상수로 고정합니다.
const (
	// Network / HTTP
	CodeNetConnRefused = "NET_001" // 연결 거부 / 즉시 실패
	CodeNetTimeout     = "NET_002" // 응답 본문 읽기 또는 연결 타임아웃
	CodeNetDNSFailure  = "NET_003" // DNS 해상 실패
	CodeReqBuild       = "REQ_001" // http.Request 생성 실패

	// Parse
	CodeParseHTML     = "PARSE_001" // 잘못된 HTML 구조
	CodeParseSelector = "PARSE_002" // 필수 셀렉터 누락
	CodeParseEncoding = "PARSE_003" // 인코딩 변환 실패
	CodeParseJSON     = "PARSE_004" // JSON 파싱 실패

	// Validation
	CodeValMissingField  = "VAL_001" // 필수 필드 누락
	CodeValInvalidFormat = "VAL_002" // 필드 형식 불일치
	CodeValContentShort  = "VAL_003" // 본문 길이 미달
	CodeValContentLong   = "VAL_004" // 본문 길이 초과
	CodeValQualityLow    = "VAL_005" // 품질 점수 임계 미달
	CodeValSpam          = "VAL_006" // 스팸/도배 패턴 탐지

	// Database
	CodeDBConnFail     = "DB_001" // 커넥션 실패
	CodeDBQueryTimeout = "DB_002" // 쿼리 타임아웃
	CodeDBConstraint   = "DB_003" // 제약 위반
	CodeDBDeadlock     = "DB_004" // 데드락

	// Queue
	CodeQueuePublish   = "QUEUE_001" // 발행 실패
	CodeQueueFull      = "QUEUE_002" // 큐 포화
	CodeQueueMalformed = "QUEUE_003" // 메시지 형식 오류

	// Storage (서비스 계층, content/raw_content 추상)
	CodeStorageRead   = "STORAGE_001" // 조회 실패
	CodeStorageWrite  = "STORAGE_002" // 저장 실패
	CodeStorageDelete = "STORAGE_003" // 삭제 실패

	// Config
	CodeConfigParse   = "CONFIG_001" // 설정 파싱 실패
	CodeConfigMissing = "CONFIG_002" // 필수 설정 누락

	// Internal
	CodeInternal = "INTERNAL_001" // 그 외 내부 오류
)

// CrawlerError는 시스템 전반에서 사용하는 구조화 에러입니다.
// 타입 이름은 역사적 사유로 유지되지만, 크롤러뿐 아니라 storage/queue/processor 등
// 모든 internal/ 레이어 경계에서 동일하게 사용합니다.
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
	if e.Err != nil {
		return fmt.Sprintf("[%s:%s] %s: %v", e.Category, e.Code, e.Message, e.Err)
	}
	return fmt.Sprintf("[%s:%s] %s", e.Category, e.Code, e.Message)
}

func (e *CrawlerError) Unwrap() error {
	return e.Err
}

// Is는 errors.Is 와 호환되며, target 에 지정된 식별 필드(Code, Category)가 모두 일치할 때 true 를 반환합니다.
// target 의 필드 중 빈 값("")은 비교에서 제외되어, Code 만 / Category 만 / 둘 다 명시한 모든 패턴을 정확히 처리합니다.
//
// 예) errors.Is(err, &CrawlerError{Code: CodeNetTimeout})         → Code 만 매칭
//
//	errors.Is(err, &CrawlerError{Category: ErrCategoryNetwork}) → Category 만 매칭
//	errors.Is(err, &CrawlerError{Code: "X", Category: "Y"})     → Code AND Category 모두 매칭
func (e *CrawlerError) Is(target error) bool {
	t, ok := target.(*CrawlerError)
	if !ok {
		return false
	}
	if t.Code != "" && t.Code != e.Code {
		return false
	}
	if t.Category != "" && t.Category != e.Category {
		return false
	}
	// target 에 두 필드 모두 비어있으면 임의의 CrawlerError 와 일치하지 않도록 false 처리
	return t.Code != "" || t.Category != ""
}

// ──────────────────────────────────────────────────────────────────────
// 카테고리별 생성자 — internal/ 레이어 경계에서 호출합니다.
// pkg/ 의 generic 유틸 패키지(pkg/links, pkg/queue, pkg/redis 등)는
// 의도적으로 fmt.Errorf 를 유지하고, 호출하는 internal/ 경계에서 변환합니다.
// ──────────────────────────────────────────────────────────────────────

// NewNetworkError는 network 에러를 생성합니다 (retryable).
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

// NewTimeoutError는 timeout 에러를 생성합니다 (retryable).
func NewTimeoutError(code, message, url string, err error) *CrawlerError {
	return &CrawlerError{
		Category:  ErrCategoryTimeout,
		Code:      code,
		Message:   message,
		URL:       url,
		Retryable: true,
		Err:       err,
	}
}

// NewRateLimitError는 rate limit 에러를 생성합니다 (retryable).
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

// NewParseError는 parse 에러를 생성합니다 (non-retryable).
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

// NewValidationError는 validation 에러를 생성합니다 (non-retryable).
// 검증 임계 미달 / 필수 필드 누락 등 컨텐츠 자체 결함을 나타냅니다.
func NewValidationError(code, message string, err error) *CrawlerError {
	return &CrawlerError{
		Category:  ErrCategoryValidation,
		Code:      code,
		Message:   message,
		Retryable: false,
		Err:       err,
	}
}

// NewNotFoundError는 404 에러를 생성합니다 (non-retryable).
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

// NewForbiddenError는 403 에러를 생성합니다 (non-retryable).
func NewForbiddenError(url string) *CrawlerError {
	return &CrawlerError{
		Category:   ErrCategoryForbidden,
		Code:       "HTTP_403",
		Message:    "forbidden",
		URL:        url,
		StatusCode: 403,
		Retryable:  false,
	}
}

// NewHTTPServerError는 5xx 서버 에러를 생성합니다 (retryable).
func NewHTTPServerError(url string, statusCode int) *CrawlerError {
	return &CrawlerError{
		Category:   ErrCategoryNetwork,
		Code:       fmt.Sprintf("HTTP_%d", statusCode),
		Message:    "server error",
		URL:        url,
		StatusCode: statusCode,
		Retryable:  true,
		Err:        fmt.Errorf("status code: %d", statusCode),
	}
}

// NewHTTPClientError는 4xx 클라이언트 에러를 생성합니다 (non-retryable).
func NewHTTPClientError(url string, statusCode int) *CrawlerError {
	return &CrawlerError{
		Category:   ErrCategoryInternal,
		Code:       fmt.Sprintf("HTTP_%d", statusCode),
		Message:    "client error",
		URL:        url,
		StatusCode: statusCode,
		Retryable:  false,
		Err:        fmt.Errorf("status code: %d", statusCode),
	}
}

// inheritRetryable은 inner err 가 *CrawlerError 이면 그 Retryable 을 상속하고,
// 아니면 호출자가 지정한 default 값을 반환합니다.
// 의도: 시스템 계층 wrapper(Storage/Database/Queue)가 inner 의 retry 시맨틱을
// 무의식적으로 덮어쓰는 것을 방지합니다(예: DB constraint violation 의 Retryable=false 보존).
func inheritRetryable(err error, defaultRetryable bool) bool {
	if ce, ok := err.(*CrawlerError); ok {
		return ce.Retryable
	}
	return defaultRetryable
}

// NewDatabaseError는 database 에러를 생성합니다.
// retryable 은 호출자가 결정하되, inner err 가 이미 *CrawlerError 이면 그 Retryable 을 상속합니다.
// (예: 일시적 connection failure 는 retryable, constraint violation 은 false)
func NewDatabaseError(code, message string, retryable bool, err error) *CrawlerError {
	return &CrawlerError{
		Category:  ErrCategoryDatabase,
		Code:      code,
		Message:   message,
		Retryable: inheritRetryable(err, retryable),
		Err:       err,
	}
}

// NewQueueError는 message queue(Kafka 등) 에러를 생성합니다.
// publish 실패 등 일시적 오류는 retryable=true 로, 메시지 형식 오류는 false 로 호출자가 결정합니다.
// inner err 가 *CrawlerError 이면 그 Retryable 을 상속합니다.
func NewQueueError(code, message string, retryable bool, err error) *CrawlerError {
	return &CrawlerError{
		Category:  ErrCategoryQueue,
		Code:      code,
		Message:   message,
		Retryable: inheritRetryable(err, retryable),
		Err:       err,
	}
}

// NewStorageError는 storage(content service 등) 경계 에러를 생성합니다.
// 내부 cause(주로 DatabaseError 또는 raw repo 에러)를 wrap 하여 errors.As 로 추출 가능하게 합니다.
// inner err 가 *CrawlerError 이면 그 Retryable 을 상속합니다 — 예: DatabaseError(Retryable=false)
// 를 NewStorageError(..., true, err) 로 wrap 해도 false 가 보존됩니다.
//
// NOTE: Category 는 Storage 로 고정됩니다. retry 정책에서 storage 카테고리를 retryable
// 대상으로 포함하지 않으면 inner 가 retryable 이어도 결과적으로 재시도되지 않습니다.
// (.claude/rules/04-error-handling.md 의 layer rules 참고)
func NewStorageError(code, message string, retryable bool, err error) *CrawlerError {
	return &CrawlerError{
		Category:  ErrCategoryStorage,
		Code:      code,
		Message:   message,
		Retryable: inheritRetryable(err, retryable),
		Err:       err,
	}
}

// NewConfigError는 설정 로딩/검증 단계의 에러를 생성합니다 (non-retryable, fail-fast 의도).
func NewConfigError(code, message string, err error) *CrawlerError {
	return &CrawlerError{
		Category:  ErrCategoryConfig,
		Code:      code,
		Message:   message,
		Retryable: false,
		Err:       err,
	}
}

// NewInternalError는 분류 불가능한 내부 로직 오류를 생성합니다.
// 가급적 더 구체적인 카테고리를 우선 사용하고, 마지막 fallback 으로만 사용합니다.
func NewInternalError(code, message string, err error) *CrawlerError {
	return &CrawlerError{
		Category:  ErrCategoryInternal,
		Code:      code,
		Message:   message,
		Retryable: false,
		Err:       err,
	}
}
