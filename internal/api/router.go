package api

import (
	"net/http"
	"time"

	"issuetracker/internal/storage/repository"
	"issuetracker/pkg/logger"
)

// Deps 는 라우터가 핸들러에 주입하는 의존성입니다.
//
// 엔드포인트가 늘어나면 repository 를 필드로 추가합니다.
type Deps struct {
	DB       Pinger
	Contents repository.ContentRepository

	// AuthToken 이 비어 있으면 인증 비활성 (기본). 이슈 #650.
	AuthToken string

	Log *logger.Logger
}

// authExemptPaths: 인증을 면제하는 경로.
//
// /health — k8s probe / 로드밸런서 헬스체크는 인증 헤더를 붙이지 않는다. 면제하지 않으면
// 인증을 켜는 순간 probe 가 전부 실패해 인스턴스가 죽은 것으로 판정된다. DB 연결 상태와
// latency 만 노출하므로 무인증 노출 위험이 낮다.
var authExemptPaths = map[string]bool{"/health": true}

// NewRouter 는 API 라우터를 구성합니다.
//
// 표준 net/http.ServeMux 를 사용합니다 — Go 1.22+ 의 method + wildcard 패턴
// ("GET /api/contents/{id}") 으로 충분하므로 외부 라우터 의존성을 두지 않습니다 (규약 5).
func NewRouter(deps Deps) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", NewHealthHandler(deps.DB, deps.Log))
	mux.HandleFunc("GET /api/contents", NewContentListHandler(deps.Contents, deps.Log))
	mux.HandleFunc("GET /api/contents/{id}", NewContentDetailHandler(deps.Contents, deps.Log))

	// catch-all — 미등록 경로를 표준 평문 404 대신 통일된 에러 body 로 응답.
	//
	// 부작용: "GET /health" 에 POST 하면 ServeMux 의 405 대신 본 핸들러가 잡아 404 가 됩니다.
	// 응답 형식 일관성을 우선한 선택입니다.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		WriteError(w, deps.Log, http.StatusNotFound, CodeNotFound, "endpoint not found", nil)
	})

	// 순서: access log → auth → mux. access log 를 바깥에 둬야 401 로 거절된 요청도 기록된다.
	return withAccessLog(withBearerAuth(mux, deps.AuthToken, authExemptPaths, deps.Log), deps.Log)
}

// statusRecorder 는 access log 에 남길 상태 코드를 포착합니다.
//
// http.Flusher / Hijacker 를 전달하지 않습니다 — 본 API 는 JSON 단발 응답만 하므로
// streaming / websocket 핸들러를 추가하려면 이 래퍼부터 고쳐야 합니다.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

// withAccessLog 는 요청 단위 구조화 로그를 남깁니다.
func withAccessLog(next http.Handler, log *logger.Logger) http.Handler {
	if log == nil {
		return next
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(rec, r)

		log.WithFields(map[string]interface{}{
			"method":      r.Method,
			"path":        r.URL.Path,
			"status_code": rec.status,
			"duration_ms": time.Since(start).Milliseconds(),
		}).Debug("api request completed")
	})
}
