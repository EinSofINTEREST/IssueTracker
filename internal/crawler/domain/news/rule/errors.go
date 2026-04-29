package rule

import "fmt"

// ErrorCode 는 rule 패키지의 정규화된 에러 분류입니다.
//
// ErrorCode classifies failures from Resolver / Parser. 호출자는 errors.As 로 *Error 를
// 추출해 Code 로 분기합니다 (예: ErrNoRule → LLM 자동 생성 fallback).
type ErrorCode string

const (
	// ErrInvalidURL: URL parse 실패 / host 미존재. 호출자가 입력 검증 책임.
	ErrInvalidURL ErrorCode = "invalid_url"

	// ErrNoRule: host + target_type 매칭 활성 rule 없음.
	// 향후 LLM 자동 생성 fallback 진입점 — 호출자가 errors.Is 로 분기 가능.
	ErrNoRule ErrorCode = "no_rule"

	// ErrEmptySelector: rule 의 selector 가 핵심 필드에 대해 비어있음.
	// 예: article 인데 Title selector 없음 → 무의미한 결과 회피 위해 명시 실패.
	ErrEmptySelector ErrorCode = "empty_selector"

	// ErrParseFailure: HTML 파싱 / selector 매칭 실패 (필드 0건 추출 등).
	ErrParseFailure ErrorCode = "parse_failure"
)

// Error 는 rule 패키지의 공통 에러 타입입니다.
type Error struct {
	Code       ErrorCode
	Message    string
	Host       string // 진단용 (resolver) — 비어있을 수 있음
	URL        string // 진단용 (resolver) — 비어있을 수 있음
	TargetType string // 진단용 — 비어있을 수 있음
	Err        error  // wrap 된 원본
}

func (e *Error) Error() string {
	parts := fmt.Sprintf("[rule:%s] %s", e.Code, e.Message)
	if e.Host != "" {
		parts += fmt.Sprintf(" (host=%s)", e.Host)
	}
	if e.URL != "" {
		parts += fmt.Sprintf(" (url=%s)", e.URL)
	}
	if e.TargetType != "" {
		parts += fmt.Sprintf(" (type=%s)", e.TargetType)
	}
	if e.Err != nil {
		parts += fmt.Sprintf(": %v", e.Err)
	}
	return parts
}

// Unwrap 은 errors.As / errors.Is 가 wrap chain 을 따라가도록 합니다.
func (e *Error) Unwrap() error { return e.Err }

// Is 는 errors.Is 호환 비교 — Code 가 같으면 true (다른 필드 무시).
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	if !ok {
		return false
	}
	return t.Code == "" || e.Code == t.Code
}
