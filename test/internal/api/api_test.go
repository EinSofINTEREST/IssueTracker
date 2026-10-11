package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/api"
	"issuetracker/pkg/logger"
)

// ─────────────────────────────────────────────────────────────────────────────
// 헬퍼
// ─────────────────────────────────────────────────────────────────────────────

// stubPinger 는 Ping 결과와 소요 시간을 제어합니다.
type stubPinger struct {
	err   error
	delay time.Duration
	// ctxErr 는 Ping 이 관측한 ctx 취소 사유를 기록합니다 (pingTimeout 검증용).
	mu     sync.Mutex
	sawCtx error
}

func (p *stubPinger) Ping(ctx context.Context) error {
	if p.delay > 0 {
		select {
		case <-time.After(p.delay):
		case <-ctx.Done():
			p.mu.Lock()
			p.sawCtx = ctx.Err()
			p.mu.Unlock()
			return ctx.Err()
		}
	}
	return p.err
}

func quietLogger() *logger.Logger {
	cfg := logger.DefaultConfig()
	cfg.Level = logger.LevelError
	return logger.New(cfg)
}

// decodeError 는 응답 body 를 에러 envelope 으로 디코딩합니다.
func decodeError(t *testing.T, body io.Reader) api.ErrorBody {
	t.Helper()

	var envelope struct {
		Error api.ErrorBody `json:"error"`
	}
	require.NoError(t, json.NewDecoder(body).Decode(&envelope))

	return envelope.Error
}

func freeAddr(t *testing.T) string {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())

	return addr
}

// ─────────────────────────────────────────────────────────────────────────────
// 에러 응답 규약
// ─────────────────────────────────────────────────────────────────────────────

func TestWriteError_UsesUnifiedEnvelope(t *testing.T) {
	rec := httptest.NewRecorder()

	api.WriteError(rec, quietLogger(), http.StatusBadRequest, api.CodeBadRequest, "invalid country", nil)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "application/json; charset=utf-8", rec.Header().Get("Content-Type"))

	body := decodeError(t, rec.Body)
	assert.Equal(t, api.CodeBadRequest, body.Code)
	assert.Equal(t, "invalid country", body.Message)
}

// 내부 에러 문자열이 응답으로 새면 DB 스키마 / 쿼리가 외부에 노출된다.
func TestWriteError_DoesNotLeakInternalCause(t *testing.T) {
	rec := httptest.NewRecorder()
	cause := errors.New(`pq: relation "content_bodies" does not exist`)

	api.WriteError(rec, quietLogger(), http.StatusInternalServerError, api.CodeInternal, "internal error", cause)

	raw := rec.Body.String()
	assert.NotContains(t, raw, "content_bodies")
	assert.NotContains(t, raw, "pq:")

	body := decodeError(t, rec.Body)
	assert.Equal(t, api.CodeInternal, body.Code)
	assert.Equal(t, "internal error", body.Message)
}

// ─────────────────────────────────────────────────────────────────────────────
// /health
// ─────────────────────────────────────────────────────────────────────────────

func doHealth(t *testing.T, db api.Pinger) (*httptest.ResponseRecorder, api.HealthResponse) {
	t.Helper()

	handler := api.NewRouter(api.Deps{DB: db, Log: quietLogger()})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	var resp api.HealthResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))

	return rec, resp
}

func TestHealth_DBUp_ReturnsHealthy(t *testing.T) {
	rec, resp := doHealth(t, &stubPinger{})

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, api.StateHealthy, resp.Status)

	db, ok := resp.Components["database"]
	require.True(t, ok, "database 컴포넌트가 응답에 없습니다")
	assert.Equal(t, api.StateHealthy, db.Status)
	assert.False(t, db.CheckedAt.IsZero())
}

func TestHealth_DBDown_ReturnsServiceUnavailable(t *testing.T) {
	rec, resp := doHealth(t, &stubPinger{err: errors.New("dial tcp 10.0.0.1:5432: connect: refused")})

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code,
		"DB 장애가 200 이면 오케스트레이터가 인스턴스를 빼지 못합니다")
	assert.Equal(t, api.StateUnhealthy, resp.Status)

	// 접속 정보가 담긴 에러 문자열을 그대로 싣지 않는다.
	assert.NotContains(t, resp.Components["database"].Message, "10.0.0.1")
}

func TestHealth_DBNil_ReturnsUnhealthy(t *testing.T) {
	rec, resp := doHealth(t, nil)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Equal(t, api.StateUnhealthy, resp.Status)
}

// degraded 는 200 이어야 한다 — 느릴 뿐 요청은 처리되므로 인스턴스를 빼면 남은 쪽 부하가 늘어난다.
func TestHealth_SlowDB_ReturnsDegradedWith200(t *testing.T) {
	rec, resp := doHealth(t, &stubPinger{delay: 150 * time.Millisecond})

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, api.StateDegraded, resp.Status)
	assert.GreaterOrEqual(t, resp.Components["database"].LatencyMs, int64(100))
}

// health 가 DB 지연에 무한정 끌려가면 살아있는지 죽었는지 판단할 수 없게 된다.
func TestHealth_HangingDB_TimesOutAsUnhealthy(t *testing.T) {
	db := &stubPinger{delay: 30 * time.Second}

	start := time.Now()
	rec, resp := doHealth(t, db)
	elapsed := time.Since(start)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Equal(t, api.StateUnhealthy, resp.Status)
	assert.Less(t, elapsed, 10*time.Second, "ping timeout 이 적용되지 않았습니다")

	db.mu.Lock()
	defer db.mu.Unlock()
	assert.ErrorIs(t, db.sawCtx, context.DeadlineExceeded)
}

// ─────────────────────────────────────────────────────────────────────────────
// 라우팅
// ─────────────────────────────────────────────────────────────────────────────

func TestRouter_UnknownPath_ReturnsUnifiedNotFound(t *testing.T) {
	handler := api.NewRouter(api.Deps{DB: &stubPinger{}, Log: quietLogger()})
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nope", nil))

	assert.Equal(t, http.StatusNotFound, rec.Code)
	// 표준 평문 404 가 아니라 통일된 JSON envelope 이어야 한다.
	assert.Equal(t, "application/json; charset=utf-8", rec.Header().Get("Content-Type"))
	assert.Equal(t, api.CodeNotFound, decodeError(t, rec.Body).Code)
}

func TestRouter_NilLogger_DoesNotPanic(t *testing.T) {
	handler := api.NewRouter(api.Deps{DB: &stubPinger{}})
	rec := httptest.NewRecorder()

	assert.NotPanics(t, func() {
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	})
	assert.Equal(t, http.StatusOK, rec.Code)
}

// ─────────────────────────────────────────────────────────────────────────────
// Serve — lifecycle
// ─────────────────────────────────────────────────────────────────────────────

func TestServe_EmptyAddr_DisablesServer(t *testing.T) {
	stop, err := api.Serve(context.Background(), "", http.NotFoundHandler(), time.Second, quietLogger())

	require.NoError(t, err)
	require.NotNil(t, stop)
	assert.NoError(t, stop())
}

// bind 실패가 goroutine 안으로 들어가면 caller 는 성공한 줄 알고 API 가 조용히 안 뜬다.
func TestServe_AddrInUse_ReturnsErrorSynchronously(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = occupied.Close() }()

	addr := occupied.Addr().String()

	stop, err := api.Serve(context.Background(), addr, http.NotFoundHandler(), time.Second, quietLogger())

	require.Error(t, err)
	assert.Nil(t, stop)
	assert.Contains(t, err.Error(), addr)
}

func TestServe_ServesHealthOverHTTP(t *testing.T) {
	addr := freeAddr(t)
	handler := api.NewRouter(api.Deps{DB: &stubPinger{}, Log: quietLogger()})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stop, err := api.Serve(ctx, addr, handler, 2*time.Second, quietLogger())
	require.NoError(t, err)
	defer func() { _ = stop() }()

	// 타임아웃 있는 client — 서버가 응답하지 않을 때 go test 전체 타임아웃까지 매달리지 않도록.
	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://%s/health", addr), nil)
	require.NoError(t, err)

	resp, err := client.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.True(t, strings.Contains(string(body), `"healthy"`), "응답: %s", string(body))
}

func TestServe_Stop_ShutsDownServer(t *testing.T) {
	addr := freeAddr(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stop, err := api.Serve(ctx, addr, http.NotFoundHandler(), 2*time.Second, quietLogger())
	require.NoError(t, err)

	require.NoError(t, stop())

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, derr := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if derr != nil {
			return
		}
		_ = conn.Close()
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("api server still accepting connections on %s", addr)
}

// shutdownTimeout 0 을 그대로 쓰면 grace 윈도우가 즉시 만료되어 in-flight 요청이 끊긴다.
func TestServe_ZeroShutdownTimeout_FallsBackToDefault(t *testing.T) {
	addr := freeAddr(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stop, err := api.Serve(ctx, addr, http.NotFoundHandler(), 0, quietLogger())
	require.NoError(t, err)

	assert.NoError(t, stop(), "0 이 그대로 쓰이면 DeadlineExceeded 가 반환됩니다")
}
