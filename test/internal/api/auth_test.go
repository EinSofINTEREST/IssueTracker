package api_test

// auth_test.go — 이슈 #650: 선택적 bearer 토큰 인증.

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/api"
)

const testToken = "s3cr3t-token"

func authRouter(token string) http.Handler {
	return api.NewRouter(api.Deps{
		DB:        &stubPinger{},
		Contents:  &fakeContentRepo{},
		AuthToken: token,
		Log:       quietLogger(),
	})
}

func requestWith(t *testing.T, handler http.Handler, target, authHeader string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, target, nil)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	return rec
}

// ─────────────────────────────────────────────────────────────────────────────
// 비활성 (기본)
// ─────────────────────────────────────────────────────────────────────────────

// 토큰 미설정이 기본이다 — 기존 동작이 바뀌면 안 된다.
func TestAuth_NoTokenConfigured_AllowsRequests(t *testing.T) {
	handler := authRouter("")

	rec := requestWith(t, handler, "/api/contents", "")

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestAuth_NoTokenConfigured_IgnoresProvidedHeader(t *testing.T) {
	handler := authRouter("")

	rec := requestWith(t, handler, "/api/contents", "Bearer whatever")

	assert.Equal(t, http.StatusOK, rec.Code)
}

// ─────────────────────────────────────────────────────────────────────────────
// 활성
// ─────────────────────────────────────────────────────────────────────────────

func TestAuth_ValidToken_Allows(t *testing.T) {
	handler := authRouter(testToken)

	rec := requestWith(t, handler, "/api/contents", "Bearer "+testToken)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestAuth_RejectedRequests_Return401(t *testing.T) {
	tests := []struct {
		name   string
		header string
	}{
		{"헤더 없음", ""},
		{"토큰 불일치", "Bearer wrong-token"},
		{"접두사 없음", testToken},
		{"다른 스킴", "Basic " + testToken},
		{"빈 토큰", "Bearer "},
		{"접두사 중복", "Bearer Bearer " + testToken},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := authRouter(testToken)

			rec := requestWith(t, handler, "/api/contents", tt.header)

			require.Equal(t, http.StatusUnauthorized, rec.Code, "body: %s", rec.Body.String())
			assert.Equal(t, "Bearer", rec.Header().Get("WWW-Authenticate"))
			assert.Equal(t, api.CodeUnauthorized, decodeError(t, rec.Body).Code)
		})
	}
}

// RFC 7235 — auth-scheme 은 대소문자를 가리지 않는다.
func TestAuth_LowercaseScheme_IsAccepted(t *testing.T) {
	handler := authRouter(testToken)

	rec := requestWith(t, handler, "/api/contents", "bearer "+testToken)

	assert.Equal(t, http.StatusOK, rec.Code)
}

// 토큰의 접두사만 맞아도 통과하면 한 글자씩 알아낼 수 있다.
func TestAuth_TokenPrefix_IsRejected(t *testing.T) {
	handler := authRouter(testToken)

	rec := requestWith(t, handler, "/api/contents", "Bearer "+testToken[:4])

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestAuth_DetailEndpoint_AlsoProtected(t *testing.T) {
	handler := authRouter(testToken)

	rec := requestWith(t, handler, "/api/contents/some-id", "")

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

// 미등록 경로도 인증을 거친다 — 404 와 401 이 섞이면 경로 존재 여부가 새어 나간다.
func TestAuth_UnknownPath_Returns401(t *testing.T) {
	handler := authRouter(testToken)

	rec := requestWith(t, handler, "/nope", "")

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

// ─────────────────────────────────────────────────────────────────────────────
// /health 면제
// ─────────────────────────────────────────────────────────────────────────────

// probe 는 인증 헤더를 붙이지 않는다 — 면제하지 않으면 인증을 켜는 순간 인스턴스가
// 죽은 것으로 판정된다.
func TestAuth_HealthExempt_AllowsWithoutToken(t *testing.T) {
	handler := authRouter(testToken)

	rec := requestWith(t, handler, "/health", "")

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestAuth_HealthExempt_StillWorksWithToken(t *testing.T) {
	handler := authRouter(testToken)

	rec := requestWith(t, handler, "/health", "Bearer "+testToken)

	assert.Equal(t, http.StatusOK, rec.Code)
}

// ─────────────────────────────────────────────────────────────────────────────
// 기동 시 노출 경고
// ─────────────────────────────────────────────────────────────────────────────

func TestInsecureExposure(t *testing.T) {
	tests := []struct {
		name  string
		addr  string
		token string
		want  bool
	}{
		{"loopback + 토큰 없음", "127.0.0.1:8080", "", false},
		{"localhost + 토큰 없음", "localhost:8080", "", false},
		{"IPv6 loopback + 토큰 없음", "[::1]:8080", "", false},
		{"모든 인터페이스 + 토큰 없음", ":8080", "", true},
		{"0.0.0.0 + 토큰 없음", "0.0.0.0:8080", "", true},
		{"외부 IP + 토큰 없음", "10.0.0.5:8080", "", true},
		{"외부 IP + 토큰 있음", "0.0.0.0:8080", testToken, false},
		{"서버 비활성", "", "", false},
		{"포트 없는 형태", "0.0.0.0", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, api.InsecureExposure(tt.addr, tt.token))
		})
	}
}
