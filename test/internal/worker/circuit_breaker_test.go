package worker_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/crawler/worker"
)

// cbPollTimeout은 OpenTimeout 경과를 기다리는 require.Eventually 상한입니다.
// CI 부하 상황에서 간헐적 실패를 방지하기 위해 실제 OpenTimeout 대비 충분히 크게 설정합니다.
const (
	cbPollTimeout = 500 * time.Millisecond
	cbPollTick    = 2 * time.Millisecond
)

func newTestCBConfig(maxFailures int, openTimeout time.Duration) worker.CircuitBreakerConfig {
	return worker.CircuitBreakerConfig{
		MaxFailures: maxFailures,
		OpenTimeout: openTimeout,
	}
}

// TestCircuitBreaker_InitialState_AllowsRequests는
// 초기 Closed 상태에서 요청을 허용하는지 검증합니다.
func TestCircuitBreaker_InitialState_AllowsRequests(t *testing.T) {
	registry := worker.NewCircuitBreakerRegistry(newTestCBConfig(3, time.Minute), nil)
	cb := registry.Get("cnn")

	assert.True(t, cb.Allow())
	assert.Equal(t, "closed", cb.State())
}

// TestCircuitBreaker_ExceedsMaxFailures_OpensCircuit는
// MaxFailures 연속 실패 후 Open 상태로 전환되는지 검증합니다.
func TestCircuitBreaker_ExceedsMaxFailures_OpensCircuit(t *testing.T) {
	registry := worker.NewCircuitBreakerRegistry(newTestCBConfig(3, time.Minute), nil)
	cb := registry.Get("cnn")

	cb.RecordFailure()
	cb.RecordFailure()
	assert.Equal(t, "closed", cb.State())
	assert.Equal(t, 2, cb.Failures())

	cb.RecordFailure() // 3번째 실패 → Open
	assert.Equal(t, "open", cb.State())
	assert.False(t, cb.Allow())
}

// TestCircuitBreaker_SuccessResetFailures_StaysClosed는
// 성공 기록 시 실패 카운터가 초기화되는지 검증합니다.
func TestCircuitBreaker_SuccessResetFailures_StaysClosed(t *testing.T) {
	registry := worker.NewCircuitBreakerRegistry(newTestCBConfig(3, time.Minute), nil)
	cb := registry.Get("cnn")

	cb.RecordFailure()
	cb.RecordFailure()
	cb.RecordSuccess() // 실패 카운터 초기화

	assert.Equal(t, "closed", cb.State())
	assert.Equal(t, 0, cb.Failures())
	assert.True(t, cb.Allow())
}

// TestCircuitBreaker_OpenTimeout_TransitionsToHalfOpen는
// OpenTimeout 경과 후 HalfOpen으로 전환되어 probe를 허용하는지 검증합니다.
func TestCircuitBreaker_OpenTimeout_TransitionsToHalfOpen(t *testing.T) {
	registry := worker.NewCircuitBreakerRegistry(newTestCBConfig(1, 10*time.Millisecond), nil)
	cb := registry.Get("naver")

	cb.RecordFailure() // 1회 실패 → Open
	assert.Equal(t, "open", cb.State())
	assert.False(t, cb.Allow())

	// OpenTimeout 경과 후 Allow()가 HalfOpen probe로 전환되기를 폴링합니다.
	// time.Sleep 대신 Eventually를 사용하여 CI 부하로 인한 flakiness 제거.
	require.Eventually(t, func() bool {
		return cb.Allow()
	}, cbPollTimeout, cbPollTick, "allow should return true after OpenTimeout")
	assert.Equal(t, "half_open", cb.State())

	// probe 진행 중에는 추가 요청 차단
	assert.False(t, cb.Allow())
}

// TestCircuitBreaker_HalfOpenProbeSuccess_ClosesCircuit는
// HalfOpen probe 성공 시 Closed로 전환되는지 검증합니다.
func TestCircuitBreaker_HalfOpenProbeSuccess_ClosesCircuit(t *testing.T) {
	registry := worker.NewCircuitBreakerRegistry(newTestCBConfig(1, 10*time.Millisecond), nil)
	cb := registry.Get("naver")

	cb.RecordFailure()
	require.Eventually(t, cb.Allow, cbPollTimeout, cbPollTick, "half_open entry expected after OpenTimeout")

	cb.RecordSuccess() // probe 성공 → Closed
	assert.Equal(t, "closed", cb.State())
	assert.True(t, cb.Allow())
}

// TestCircuitBreaker_HalfOpenProbeFailure_ReopensCircuit는
// HalfOpen probe 실패 시 다시 Open으로 전환되는지 검증합니다.
func TestCircuitBreaker_HalfOpenProbeFailure_ReopensCircuit(t *testing.T) {
	registry := worker.NewCircuitBreakerRegistry(newTestCBConfig(1, 10*time.Millisecond), nil)
	cb := registry.Get("naver")

	cb.RecordFailure()
	require.Eventually(t, cb.Allow, cbPollTimeout, cbPollTick, "half_open entry expected after OpenTimeout")

	cb.RecordFailure() // probe 실패 → 다시 Open
	assert.Equal(t, "open", cb.State())
	assert.False(t, cb.Allow())
}

// TestCircuitBreakerRegistry_IsolatesPerSource는
// 소스별로 독립적인 circuit breaker가 관리되는지 검증합니다.
func TestCircuitBreakerRegistry_IsolatesPerSource(t *testing.T) {
	registry := worker.NewCircuitBreakerRegistry(newTestCBConfig(2, time.Minute), nil)

	cnn := registry.Get("cnn")
	naver := registry.Get("naver")

	// CNN만 실패
	cnn.RecordFailure()
	cnn.RecordFailure()

	assert.Equal(t, "open", cnn.State())
	assert.False(t, cnn.Allow())

	// Naver는 영향 없음
	assert.Equal(t, "closed", naver.State())
	assert.True(t, naver.Allow())
}

// TestCircuitBreakerRegistry_GetReturnsSameInstance는
// 동일 소스에 대해 동일 인스턴스를 반환하는지 검증합니다.
func TestCircuitBreakerRegistry_GetReturnsSameInstance(t *testing.T) {
	registry := worker.NewCircuitBreakerRegistry(worker.DefaultCircuitBreakerConfig, nil)

	cb1 := registry.Get("cnn")
	cb2 := registry.Get("cnn")

	assert.Same(t, cb1, cb2)
}
