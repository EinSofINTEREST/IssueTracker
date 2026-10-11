package metrics_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/pkg/logger"
	"issuetracker/pkg/metrics"
)

// ─────────────────────────────────────────────────────────────────────────────
// 헬퍼
// ─────────────────────────────────────────────────────────────────────────────

// syncBuffer는 goroutine 안전한 로그 수집 버퍼입니다.
//
// Serve가 띄우는 goroutine이 같은 Writer에 기록하므로 -race 하에서 필요합니다.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// newTestLogger는 출력을 buf로 돌린 logger를 반환합니다 (Debug 레벨 — 모든 기록 포착).
func newTestLogger(buf io.Writer) *logger.Logger {
	cfg := logger.DefaultConfig()
	cfg.Level = logger.LevelDebug
	cfg.Output = buf
	return logger.New(cfg)
}

// freeAddr는 바인딩 가능한 loopback 주소를 반환합니다.
//
// Serve는 실제 listen 주소를 돌려주지 않으므로 (:0 을 넘기면 포트를 알 수 없다)
// 포트를 미리 확보했다가 닫아서 알아낸다.
func freeAddr(t *testing.T) string {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())

	return addr
}

// scrape는 /metrics를 1회 호출해 본문을 반환합니다.
func scrape(t *testing.T, addr string) (int, string) {
	t.Helper()

	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("http://%s/metrics", addr), nil)
	require.NoError(t, err)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	return resp.StatusCode, string(body)
}

// waitUntilRefused는 addr가 더 이상 연결을 받지 않을 때까지 대기합니다.
func waitUntilRefused(t *testing.T, addr string, timeout time.Duration) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err != nil {
			return
		}
		_ = conn.Close()
		time.Sleep(20 * time.Millisecond)
	}

	t.Fatalf("endpoint %s still accepting connections after %s", addr, timeout)
}

// ─────────────────────────────────────────────────────────────────────────────
// NewRegistry
// ─────────────────────────────────────────────────────────────────────────────

func TestNewRegistry_RegistersGoAndProcessCollectors(t *testing.T) {
	reg := metrics.NewRegistry()

	families, err := reg.Gather()
	require.NoError(t, err)

	var hasGo, hasProcess bool
	for _, f := range families {
		if strings.HasPrefix(f.GetName(), "go_") {
			hasGo = true
		}
		if strings.HasPrefix(f.GetName(), "process_") {
			hasProcess = true
		}
	}

	assert.True(t, hasGo, "Go runtime collector가 등록되지 않았습니다")
	assert.True(t, hasProcess, "process collector가 등록되지 않았습니다")
}

func TestNewRegistry_AcceptsCustomMetric(t *testing.T) {
	reg := metrics.NewRegistry()

	counter := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "issuetracker_test_custom_total",
		Help: "test metric",
	})
	reg.MustRegister(counter)
	counter.Add(3)

	families, err := reg.Gather()
	require.NoError(t, err)

	var got float64
	var found bool
	for _, f := range families {
		if f.GetName() != "issuetracker_test_custom_total" {
			continue
		}
		found = true
		require.Len(t, f.GetMetric(), 1)
		got = f.GetMetric()[0].GetCounter().GetValue()
	}

	require.True(t, found, "등록한 custom metric이 Gather 결과에 없습니다")
	assert.Equal(t, 3.0, got)
}

func TestNewRegistry_ReturnsIndependentRegistries(t *testing.T) {
	first := metrics.NewRegistry()
	second := metrics.NewRegistry()

	// 같은 이름을 두 registry에 각각 등록할 수 있어야 한다 — 전역 공유가 아니라는 확인.
	opts := prometheus.CounterOpts{Name: "issuetracker_test_independent_total", Help: "test metric"}
	first.MustRegister(prometheus.NewCounter(opts))

	assert.NotPanics(t, func() {
		second.MustRegister(prometheus.NewCounter(opts))
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Serve — 비활성화 경로
// ─────────────────────────────────────────────────────────────────────────────

func TestServe_EmptyAddr_DisablesEndpoint(t *testing.T) {
	buf := &syncBuffer{}

	stop, err := metrics.Serve(context.Background(), "", metrics.NewRegistry(), newTestLogger(buf))
	require.NoError(t, err)
	require.NotNil(t, stop, "비활성화 시에도 noop stop을 반환해야 합니다 (caller의 defer가 nil panic 없이 동작)")

	assert.NoError(t, stop())
	assert.NoError(t, stop(), "noop stop은 중복 호출해도 안전해야 합니다")

	logs := buf.String()
	assert.Contains(t, logs, "metrics endpoint disabled")
	assert.NotContains(t, logs, "metrics endpoint started",
		"addr가 비었는데 서버가 기동됐습니다")
}

// ─────────────────────────────────────────────────────────────────────────────
// Serve — fail-fast bind
// ─────────────────────────────────────────────────────────────────────────────

// 포트 충돌이 동기 error로 드러나는지 — goroutine 안으로 들어가면 caller는 성공한 줄 알고
// metric이 조용히 누락된다. Serve doc comment의 fail-fast 정책이 곧 이 테스트다.
func TestServe_AddrInUse_ReturnsErrorSynchronously(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = occupied.Close() }()

	addr := occupied.Addr().String()
	buf := &syncBuffer{}

	stop, err := metrics.Serve(context.Background(), addr, metrics.NewRegistry(), newTestLogger(buf))

	require.Error(t, err, "이미 점유된 주소인데 error가 반환되지 않았습니다")
	assert.Nil(t, stop, "error와 함께 stop을 반환하면 caller가 nil 검사 없이 호출할 위험이 있습니다")
	assert.Contains(t, err.Error(), addr, "error에 실패한 주소가 담겨야 원인 파악이 됩니다")
	assert.NotContains(t, buf.String(), "metrics endpoint started")
}

// ─────────────────────────────────────────────────────────────────────────────
// Serve — 정상 노출
// ─────────────────────────────────────────────────────────────────────────────

func TestServe_ExposesRegisteredMetrics(t *testing.T) {
	addr := freeAddr(t)
	reg := metrics.NewRegistry()

	counter := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "issuetracker_test_served_total",
		Help: "test metric",
	})
	reg.MustRegister(counter)
	counter.Add(7)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stop, err := metrics.Serve(ctx, addr, reg, newTestLogger(&syncBuffer{}))
	require.NoError(t, err)
	defer func() { _ = stop() }()

	status, body := scrape(t, addr)

	assert.Equal(t, http.StatusOK, status)
	assert.Contains(t, body, "issuetracker_test_served_total 7")
	assert.Contains(t, body, "go_goroutines", "기본 collector가 노출되지 않았습니다")
}

func TestServe_UnknownPath_ReturnsNotFound(t *testing.T) {
	addr := freeAddr(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stop, err := metrics.Serve(ctx, addr, metrics.NewRegistry(), newTestLogger(&syncBuffer{}))
	require.NoError(t, err)
	defer func() { _ = stop() }()

	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("http://%s/", addr), nil)
	require.NoError(t, err)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusNotFound, resp.StatusCode,
		"/metrics 외 경로까지 응답하면 의도치 않은 노출면이 생깁니다")
}

// ─────────────────────────────────────────────────────────────────────────────
// Serve — 종료
// ─────────────────────────────────────────────────────────────────────────────

func TestServe_ContextCancel_ShutsDownEndpoint(t *testing.T) {
	addr := freeAddr(t)
	buf := &syncBuffer{}

	ctx, cancel := context.WithCancel(context.Background())

	stop, err := metrics.Serve(ctx, addr, metrics.NewRegistry(), newTestLogger(buf))
	require.NoError(t, err)
	defer func() { _ = stop() }()

	status, _ := scrape(t, addr)
	require.Equal(t, http.StatusOK, status)

	cancel()
	waitUntilRefused(t, addr, 5*time.Second)

	assert.Eventually(t, func() bool {
		return strings.Contains(buf.String(), "metrics endpoint stopped")
	}, 2*time.Second, 20*time.Millisecond, "graceful shutdown 완료 로그가 없습니다")
}

func TestServe_Stop_ShutsDownEndpoint(t *testing.T) {
	addr := freeAddr(t)

	// ctx는 살아 있는 상태 — stop() 만으로 종료되는지 확인한다.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stop, err := metrics.Serve(ctx, addr, metrics.NewRegistry(), newTestLogger(&syncBuffer{}))
	require.NoError(t, err)

	status, _ := scrape(t, addr)
	require.Equal(t, http.StatusOK, status)

	require.NoError(t, stop())
	waitUntilRefused(t, addr, 5*time.Second)
}

func TestServe_Stop_IsIdempotent(t *testing.T) {
	addr := freeAddr(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stop, err := metrics.Serve(ctx, addr, metrics.NewRegistry(), newTestLogger(&syncBuffer{}))
	require.NoError(t, err)

	require.NoError(t, stop())
	assert.NoError(t, stop(), "stop 중복 호출이 error를 내면 defer stop + 명시 stop 조합이 깨집니다")
}

// ctx가 이미 취소된 상태로 들어와도 shutdown 경로가 즉시 만료되지 않아야 한다.
// (WithoutCancel 없이 ctx를 그대로 쓰면 grace 윈도우가 0이 되어 in-flight scrape가 끊긴다.)
func TestServe_Stop_AfterContextCancel_StillGraceful(t *testing.T) {
	addr := freeAddr(t)
	buf := &syncBuffer{}

	ctx, cancel := context.WithCancel(context.Background())

	stop, err := metrics.Serve(ctx, addr, metrics.NewRegistry(), newTestLogger(buf))
	require.NoError(t, err)

	cancel()
	waitUntilRefused(t, addr, 5*time.Second)

	// ctx goroutine 의 Shutdown 이 끝난 뒤에 stop 을 부른다 — 동시에 부르면 아직 untrack 되지
	// 않은 listener 를 양쪽이 Close 해 "use of closed network connection" 이 뜬다.
	require.Eventually(t, func() bool {
		return strings.Contains(buf.String(), "metrics endpoint stopped")
	}, 5*time.Second, 20*time.Millisecond, "graceful shutdown 이 완료되지 않았습니다")

	assert.NoError(t, stop(), "취소된 ctx로 shutdown하면 DeadlineExceeded가 새어 나옵니다")
	assert.NotContains(t, buf.String(), "shutdown error")
}
