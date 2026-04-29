package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultTimeout 은 LLM HTTP 호출의 기본 timeout 입니다.
// 모델 응답이 길어지면 길어질 수 있으므로 호출자가 Config 또는 ctx 로 override 가능합니다.
const DefaultTimeout = 60 * time.Second

// httpDoer 는 net/http.Client 를 추상화하여 테스트에서 mock 으로 교체 가능하게 합니다.
type httpDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// HTTPClient 는 모든 provider 가 공유하는 HTTP 호출 헬퍼입니다.
//
// HTTPClient is the shared HTTP transport used by all provider adapters.
// Provider 구현은 공통 timeout / 에러 매핑을 재사용하면서 자기 endpoint URL /
// auth header / request·response 직렬화만 책임지면 됩니다.
type HTTPClient struct {
	doer    httpDoer
	timeout time.Duration
}

// NewHTTPClient 는 HTTPClient 를 생성합니다. timeout=0 이면 DefaultTimeout 사용.
func NewHTTPClient(timeout time.Duration) *HTTPClient {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return &HTTPClient{
		doer:    &http.Client{Timeout: timeout},
		timeout: timeout,
	}
}

// PostJSON 은 url 에 JSON body 를 POST 하고 응답 body 를 raw bytes 로 반환합니다.
//
// providerName 은 에러 발생 시 *llm.Error.Provider 에 채워집니다.
// headers 는 Content-Type: application/json 외 추가할 헤더 (예: Authorization).
//
// 응답 status 가 2xx 이 아니면 mapHTTPError 로 *llm.Error 를 만들어 반환합니다.
// 호출자는 2xx 응답의 body 를 자기 schema 로 unmarshal 만 하면 됩니다.
func (c *HTTPClient) PostJSON(ctx context.Context, providerName, endpoint string, headers map[string]string, body any) ([]byte, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, &Error{
			Code:     ErrCodeBadRequest,
			Provider: providerName,
			Message:  "marshal request body",
			Err:      err,
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, &Error{
			Code:     ErrCodeBadRequest,
			Provider: providerName,
			Message:  "build http request",
			Err:      err,
		}
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := c.doer.Do(req)
	if err != nil {
		return nil, classifyTransportError(providerName, err)
	}
	defer resp.Body.Close()

	respBody, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return nil, &Error{
			Code:      ErrCodeNetwork,
			Provider:  providerName,
			Message:   "read response body",
			Retryable: true,
			Err:       readErr,
		}
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, mapHTTPError(providerName, resp.StatusCode, respBody)
	}

	return respBody, nil
}

// classifyTransportError 는 http.Client.Do 의 에러를 *llm.Error 로 정규화합니다.
//
// context 취소는 재시도 가치가 없는 호출자 의도이므로 Retryable=false.
// 그 외 (DNS / connection refused / timeout) 는 일시적 가능성이 높아 Retryable=true.
func classifyTransportError(providerName string, err error) *Error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		// DeadlineExceeded 도 ctx 의 timeout 이라면 호출자가 의도한 종료 — 재시도 무의미.
		// 다만 실제로 server 가 늦은 케이스면 재시도 가치 있을 수 있어 운영자 판단에 맡김.
		return &Error{
			Code:     ErrCodeNetwork,
			Provider: providerName,
			Message:  "request canceled or timed out",
			Err:      err,
		}
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return &Error{
			Code:      ErrCodeNetwork,
			Provider:  providerName,
			Message:   "network timeout",
			Retryable: true,
			Err:       err,
		}
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return &Error{
			Code:      ErrCodeNetwork,
			Provider:  providerName,
			Message:   "transport error",
			Retryable: true,
			Err:       err,
		}
	}
	return &Error{
		Code:      ErrCodeNetwork,
		Provider:  providerName,
		Message:   "transport error",
		Retryable: true,
		Err:       err,
	}
}

// mapHTTPError 는 HTTP status + body 를 *llm.Error 의 Code 로 매핑합니다.
//
// 매핑 규칙 (provider 별 미세 차이는 호출자가 추가 분류 가능):
//   - 401 / 403       → ErrCodeAuth (Retryable=false)
//   - 429             → ErrCodeRateLimit (Retryable=true)
//   - 5xx             → ErrCodeServer (Retryable=true)
//   - 400             → ErrCodeBadRequest 또는 ErrCodeContextLimit (body keyword)
//   - 그 외 4xx       → ErrCodeBadRequest (Retryable=false)
func mapHTTPError(providerName string, status int, body []byte) *Error {
	msg := strings.TrimSpace(string(body))
	if msg == "" {
		msg = http.StatusText(status)
	}
	switch {
	case status == http.StatusUnauthorized, status == http.StatusForbidden:
		return &Error{Code: ErrCodeAuth, Provider: providerName, Message: msg}
	case status == http.StatusTooManyRequests:
		return &Error{Code: ErrCodeRateLimit, Provider: providerName, Message: msg, Retryable: true}
	case status >= 500 && status < 600:
		return &Error{Code: ErrCodeServer, Provider: providerName, Message: msg, Retryable: true}
	case status == http.StatusBadRequest:
		// context window 초과는 provider 별 keyword 로 별도 분류 — 호출자가 입력 단축 결정 가능.
		lower := strings.ToLower(msg)
		if strings.Contains(lower, "context length") ||
			strings.Contains(lower, "maximum context") ||
			strings.Contains(lower, "too many tokens") ||
			strings.Contains(lower, "context window") {
			return &Error{Code: ErrCodeContextLimit, Provider: providerName, Message: msg}
		}
		return &Error{Code: ErrCodeBadRequest, Provider: providerName, Message: msg}
	case status >= 400 && status < 500:
		return &Error{Code: ErrCodeBadRequest, Provider: providerName, Message: msg}
	default:
		return &Error{Code: ErrCodeUnknown, Provider: providerName, Message: fmt.Sprintf("HTTP %d: %s", status, msg)}
	}
}
