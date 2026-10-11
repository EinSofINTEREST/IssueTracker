package api

import (
	"crypto/subtle"
	"net"
	"net/http"
	"strings"

	"issuetracker/pkg/logger"
)

// bearerScheme: Authorization 헤더의 인증 스킴 (비교 시 대소문자 무시).
const bearerScheme = "Bearer"

// withBearerAuth 는 Authorization: Bearer <token> 검사를 붙입니다.
//
// token 이 빈 문자열이면 미들웨어를 붙이지 않습니다 — 인증 비활성이 기본입니다.
//
// exempt 로 지정한 경로는 검사하지 않습니다. /health 가 그 대상인데, k8s probe 나
// 로드밸런서 헬스체크는 인증 헤더를 붙이지 않기 때문입니다 — 면제하지 않으면 인증을 켜는
// 순간 probe 가 전부 실패해 인스턴스가 죽은 것으로 판정됩니다.
func withBearerAuth(next http.Handler, token string, exempt map[string]bool, log *logger.Logger) http.Handler {
	if token == "" {
		return next
	}

	expected := []byte(token)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if exempt[r.URL.Path] {
			next.ServeHTTP(w, r)
			return
		}

		if !validBearer(r.Header.Get("Authorization"), expected) {
			// 요청별 식별자는 싣지 않는다 — 스캐닝으로 flood 되면 로그가 쓸모없어진다.
			// 운영자가 알아야 할 신호이므로 Warn (04-error-handling.md 의 레벨 기준).
			if log != nil {
				log.WithField("path", r.URL.Path).Warn("api request rejected: invalid bearer token")
			}
			w.Header().Set("WWW-Authenticate", "Bearer")
			WriteError(w, log, http.StatusUnauthorized, CodeUnauthorized, "invalid or missing bearer token", nil)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// validBearer 는 Authorization 헤더가 기대 토큰과 일치하는지 상수 시간으로 비교합니다.
//
// subtle.ConstantTimeCompare 를 쓰는 이유: 바이트 단위로 일찍 끊는 비교는 응답 시간 차이로
// 토큰을 한 글자씩 알아낼 여지를 줍니다.
//
// 스킴은 대소문자를 가리지 않습니다 — RFC 7235 가 auth-scheme 을 case-insensitive 로
// 규정합니다. 엄격히 보면 `bearer` 를 보내는 클라이언트가 원인 불명으로 401 을 받습니다.
func validBearer(header string, expected []byte) bool {
	scheme, rest, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, bearerScheme) {
		return false
	}

	got := []byte(strings.TrimSpace(rest))

	// 길이가 다르면 ConstantTimeCompare 가 0 을 반환하므로 별도 분기가 필요 없습니다.
	return subtle.ConstantTimeCompare(got, expected) == 1
}

// InsecureExposure 는 인증 없이 loopback 밖에 노출되는 구성인지 판단합니다.
//
// 기동 시 경고용입니다. fatal 로 막지 않는 이유 — 리버스 프록시가 인증을 담당하는 구성이
// 정당하고, fatal 은 그 구성을 깹니다. 알리되 판단은 운영자에게 남깁니다.
func InsecureExposure(addr, token string) bool {
	if addr == "" || token != "" {
		return false
	}
	return !isLoopbackAddr(addr)
}

// isLoopbackAddr 는 listen 주소가 loopback 에만 바인딩되는지 판단합니다.
//
// 호스트가 비어 있으면 (":8080") 모든 인터페이스에 바인딩되므로 loopback 이 아닙니다.
func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		// 포트가 없는 형태 등 — 판단할 수 없으면 안전한 쪽(노출됨)으로 본다.
		return false
	}
	if host == "" {
		return false
	}
	if host == "localhost" {
		return true
	}

	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
