package llm

import "fmt"

// ErrorCode 는 LLM 호출 실패의 표준화된 분류입니다.
//
// ErrorCode is the normalized failure category across providers.
// Provider 마다 HTTP status / error body 가 다르지만, 호출자는 ErrorCode 만으로
// 재시도 / fallback / 알림을 결정할 수 있습니다.
type ErrorCode string

const (
	// ErrCodeAuth: API key 오류, 권한 부족 등 (HTTP 401/403). 재시도 무의미.
	ErrCodeAuth ErrorCode = "auth"

	// ErrCodeRateLimit: 분당/시간당 호출 한도 초과 (HTTP 429). 재시도 가치 있음 (backoff).
	ErrCodeRateLimit ErrorCode = "rate_limit"

	// ErrCodeServer: provider 측 일시 오류 (HTTP 5xx). 재시도 가치 있음.
	ErrCodeServer ErrorCode = "server"

	// ErrCodeBadRequest: 요청 형식 오류 / 유효하지 않은 파라미터 (HTTP 400). 재시도 무의미.
	ErrCodeBadRequest ErrorCode = "bad_request"

	// ErrCodeContextLimit: 입력이 모델 context window 초과. 재시도 무의미 — 입력 단축 필요.
	ErrCodeContextLimit ErrorCode = "context_limit"

	// ErrCodeNetwork: 연결 / DNS / 타임아웃 등 클라이언트 측 네트워크 오류. 재시도 가치 있음.
	ErrCodeNetwork ErrorCode = "network"

	// ErrCodeUnknown: 매핑되지 않은 오류. Retryable=false (보수적으로).
	ErrCodeUnknown ErrorCode = "unknown"
)

// Error 는 LLM 호출 실패의 공통 표현입니다.
//
// Error is the common error type returned by all Provider implementations.
// 호출자는 errors.As 로 추출하여 Code / Retryable 로 분기할 수 있습니다.
type Error struct {
	// Code 는 표준화된 실패 분류.
	Code ErrorCode

	// Provider 는 실패를 보고한 backend 이름 ("gemini" / "openai" / "anthropic").
	Provider string

	// Message 는 사람이 읽을 수 있는 설명 (provider raw message 가능).
	Message string

	// Retryable 은 동일 입력으로 재시도가 의미 있는지 여부.
	Retryable bool

	// Err 는 wrap 된 원본 에러 (network / json decode 실패 등). nil 가능.
	Err error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("[llm:%s:%s] %s: %v", e.Provider, e.Code, e.Message, e.Err)
	}
	return fmt.Sprintf("[llm:%s:%s] %s", e.Provider, e.Code, e.Message)
}

// Unwrap 은 errors.Is / errors.As 가 wrap chain 을 따라가도록 합니다.
func (e *Error) Unwrap() error {
	return e.Err
}

// IsRetryable 은 에러가 retry 가치가 있는지 빠르게 판단하는 헬퍼입니다.
//
// IsRetryable returns true if the error is a *llm.Error with Retryable=true.
// 비-llm.Error 는 false 를 반환합니다 (보수적 정책 — 모르는 에러는 재시도하지 않음).
func IsRetryable(err error) bool {
	if err == nil {
		return false
	}
	var lerr *Error
	if !errorsAs(err, &lerr) {
		return false
	}
	return lerr.Retryable
}

// errorsAs 는 errors.As 의 thin wrapper 입니다 (테스트 친화적, std lib import 회피).
func errorsAs(err error, target **Error) bool {
	for err != nil {
		if e, ok := err.(*Error); ok {
			*target = e
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
