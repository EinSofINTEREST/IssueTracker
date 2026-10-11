// Package api 는 REST API 서버의 HTTP 계층 (라우팅 / 응답 형식 / 헬스체크) 을 제공합니다.
//
// Package api provides the HTTP layer (routing, response shaping, health checks)
// for the REST API server.
//
// 본 패키지는 storage repository 를 주입받아 HTTP 로 노출하는 얇은 계층이며,
// 비즈니스 로직을 갖지 않습니다.
package api

import (
	"encoding/json"
	"net/http"

	"issuetracker/pkg/logger"
)

// ErrorCode 는 클라이언트가 분기에 쓸 수 있는 기계 판독 에러 코드입니다.
//
// 04-error-handling.md 의 내부 에러 코드 (NET_001 등) 와 달리 본 코드는 **외부 계약** 입니다.
// 내부 코드 체계가 바뀌어도 여기 값은 호환을 유지해야 합니다.
type ErrorCode string

const (
	// CodeBadRequest: 요청 파라미터가 형식에 맞지 않음.
	CodeBadRequest ErrorCode = "BAD_REQUEST"
	// CodeNotFound: 경로 또는 리소스 부재.
	CodeNotFound ErrorCode = "NOT_FOUND"
	// CodeInternal: 서버 내부 오류 — 원인은 응답에 싣지 않고 로그에만 남김.
	CodeInternal ErrorCode = "INTERNAL"
	// CodeTimeout: 하위 저장소 응답 지연 — 재시도 가치가 있음.
	//
	// INTERNAL 과 분리하는 이유: 둘은 대응이 다르다. INTERNAL 은 코드 결함이라 배포가 필요하고,
	// TIMEOUT 은 DB 부하라 스케일/쿼리 튜닝 문제다. 같은 코드로 뭉뚱그리면 알림이 엉뚱한 곳으로 간다.
	CodeTimeout ErrorCode = "TIMEOUT"
)

// ErrorBody 는 에러 응답의 본문입니다.
type ErrorBody struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
}

// errorEnvelope 는 모든 에러 응답의 최상위 형태입니다: {"error": {...}}.
type errorEnvelope struct {
	Error ErrorBody `json:"error"`
}

// WriteError 는 통일된 에러 응답을 기록합니다.
//
// cause 는 **응답에 포함되지 않고 로그로만** 남습니다 — DB 스키마나 쿼리 문자열이 에러
// 메시지를 타고 외부로 새는 것을 막기 위함입니다. message 는 호출자가 직접 쓴,
// 노출해도 되는 문장이어야 합니다.
func WriteError(w http.ResponseWriter, log *logger.Logger, status int, code ErrorCode, message string, cause error) {
	if cause != nil && log != nil {
		log.WithFields(map[string]interface{}{
			"status_code": status,
			"error_code":  string(code),
		}).WithError(cause).Error("api request failed")
	}

	writeJSON(w, log, status, errorEnvelope{Error: ErrorBody{Code: code, Message: message}})
}

// writeJSON 은 v 를 JSON 으로 직렬화해 기록합니다.
//
// 헤더를 먼저 쓴 뒤 인코딩이 실패하면 상태 코드를 바꿀 수 없으므로, 실패는 로그로만 남깁니다
// — 클라이언트는 잘린 본문을 받습니다. 이를 피하려면 호출자가 직렬화 가능한 타입만 넘겨야
// 합니다 (map[string]any 에 chan / func 을 넣지 말 것).
func writeJSON(w http.ResponseWriter, log *logger.Logger, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)

	if err := json.NewEncoder(w).Encode(v); err != nil && log != nil {
		log.WithError(err).Error("failed to encode api response")
	}
}

// WriteJSON 은 성공 응답을 기록합니다.
func WriteJSON(w http.ResponseWriter, log *logger.Logger, status int, v interface{}) {
	writeJSON(w, log, status, v)
}
