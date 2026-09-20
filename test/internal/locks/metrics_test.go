package locks_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/locks"
	"issuetracker/pkg/logger"
)

// registry 가 nil 인 환경 (METRICS 비활성) 에서도 호출자가 nil 검사 없이 쓸 수 있어야 한다.
func TestGateMetrics_NilRegistry_RecordIsNoop(t *testing.T) {
	m := locks.NewGateMetrics(nil)

	assert.NotPanics(t, func() {
		m.RecordSkip(locks.StageParser)
		m.RecordReleaseFailure(locks.StageEnricher, locks.ReleaseFailNotOwned)
	})
}

// nil 포인터 수신자도 안전해야 한다 — StageGate 가 metrics 미주입 시 nil 을 그대로 호출한다.
func TestGateMetrics_NilReceiver_RecordIsNoop(t *testing.T) {
	var m *locks.GateMetrics

	assert.NotPanics(t, func() {
		m.RecordSkip(locks.StageFetcher)
		m.RecordReleaseFailure(locks.StageFetcher, locks.ReleaseFailInfra)
	})
}

func TestGateMetrics_RecordsSkipByStage(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := locks.NewGateMetrics(reg)

	m.RecordSkip(locks.StageParser)
	m.RecordSkip(locks.StageParser)
	m.RecordSkip(locks.StageValidator)

	assert.Equal(t, 2.0, testutil.ToFloat64(
		mustCounter(t, reg, "stage_gate_skipped_total", map[string]string{"stage": locks.StageParser})))
	assert.Equal(t, 1.0, testutil.ToFloat64(
		mustCounter(t, reg, "stage_gate_skipped_total", map[string]string{"stage": locks.StageValidator})))
}

// release 실패 사유가 나뉘어야 "TTL 튜닝 대상" 과 "Redis 장애" 를 구분할 수 있다.
func TestGateMetrics_SeparatesReleaseFailureReasons(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := locks.NewGateMetrics(reg)

	m.RecordReleaseFailure(locks.StageEnricher, locks.ReleaseFailNotOwned)
	m.RecordReleaseFailure(locks.StageEnricher, locks.ReleaseFailInfra)
	m.RecordReleaseFailure(locks.StageEnricher, locks.ReleaseFailInfra)

	assert.Equal(t, 1.0, testutil.ToFloat64(mustCounter(t, reg, "stage_gate_release_failed_total",
		map[string]string{"stage": locks.StageEnricher, "reason": locks.ReleaseFailNotOwned})))
	assert.Equal(t, 2.0, testutil.ToFloat64(mustCounter(t, reg, "stage_gate_release_failed_total",
		map[string]string{"stage": locks.StageEnricher, "reason": locks.ReleaseFailInfra})))
}

// 같은 registry 에 두 번 생성해도 panic 없이 기존 collector 를 재사용해야 한다.
func TestGateMetrics_DuplicateRegistration_ReusesCollector(t *testing.T) {
	reg := prometheus.NewRegistry()
	first := locks.NewGateMetrics(reg)

	var second *locks.GateMetrics
	require.NotPanics(t, func() { second = locks.NewGateMetrics(reg) })

	first.RecordSkip(locks.StageFetcher)
	second.RecordSkip(locks.StageFetcher)

	assert.Equal(t, 2.0, testutil.ToFloat64(
		mustCounter(t, reg, "stage_gate_skipped_total", map[string]string{"stage": locks.StageFetcher})),
		"두 인스턴스가 같은 collector 를 가리켜야 함")
}

// mustCounter 는 registry 에서 이름 + 라벨이 일치하는 counter 를 찾아 반환합니다.
func mustCounter(t *testing.T, reg *prometheus.Registry, name string, labels map[string]string) prometheus.Counter {
	t.Helper()
	families, err := reg.Gather()
	require.NoError(t, err)

	for _, f := range families {
		if f.GetName() != name {
			continue
		}
		for _, metric := range f.GetMetric() {
			match := true
			for _, lp := range metric.GetLabel() {
				if want, ok := labels[lp.GetName()]; ok && want != lp.GetValue() {
					match = false
					break
				}
			}
			if match && len(metric.GetLabel()) == len(labels) {
				c := prometheus.NewCounter(prometheus.CounterOpts{Name: "probe"})
				c.Add(metric.GetCounter().GetValue())
				return c
			}
		}
	}
	t.Fatalf("counter %s with labels %v not found", name, labels)
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// StageGate 배선 검증 — collector 가 정의만 되고 호출되지 않는 사고 방지.
// (PR #555 Copilot 지적: not_owned 라벨이 실제 경로에서 생성되지 않았음)
// ─────────────────────────────────────────────────────────────────────────────

// gate 선점 시 skip 이 실제로 기록되어야 한다.
func TestStageGate_RecordsSkip(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := locks.NewGateMetrics(reg)
	gate := locks.BuildStageGate(locks.StageParser, 1, newStubLock(false),
		logger.New(logger.DefaultConfig()), locks.WithGateMetrics(m))

	_, acquired, err := gate.Acquire(context.Background(), "https://example.com/skip")
	require.NoError(t, err)
	require.False(t, acquired)

	assert.Equal(t, 1.0, testutil.ToFloat64(
		mustCounter(t, reg, "stage_gate_skipped_total", map[string]string{"stage": locks.StageParser})))
}

// 소유권 상실(ErrLockNotOwned)은 not_owned 로 기록되어야 한다 — infra 와 섞이면
// "TTL 튜닝 대상" 인지 "Redis 장애" 인지 구분할 수 없다.
func TestStageGate_RecordsReleaseFailure_NotOwned(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := locks.NewGateMetrics(reg)
	lk := newStubLock(true)
	lk.releaseErr = fmt.Errorf("wrapped: %w", locks.ErrLockNotOwned)

	gate := locks.BuildStageGate(locks.StageEnricher, 1, lk,
		logger.New(logger.DefaultConfig()), locks.WithGateMetrics(m))

	release, acquired, err := gate.Acquire(context.Background(), "https://example.com/expired")
	require.NoError(t, err)
	require.True(t, acquired)
	release()

	assert.Equal(t, 1.0, testutil.ToFloat64(mustCounter(t, reg, "stage_gate_release_failed_total",
		map[string]string{"stage": locks.StageEnricher, "reason": locks.ReleaseFailNotOwned})))
}

// 그 외 실패는 infra 로 기록되어야 한다.
func TestStageGate_RecordsReleaseFailure_Infra(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := locks.NewGateMetrics(reg)
	lk := newStubLock(true)
	lk.releaseErr = errors.New("redis timeout")

	gate := locks.BuildStageGate(locks.StageValidator, 1, lk,
		logger.New(logger.DefaultConfig()), locks.WithGateMetrics(m))

	release, acquired, err := gate.Acquire(context.Background(), "https://example.com/infra")
	require.NoError(t, err)
	require.True(t, acquired)
	release()

	assert.Equal(t, 1.0, testutil.ToFloat64(mustCounter(t, reg, "stage_gate_release_failed_total",
		map[string]string{"stage": locks.StageValidator, "reason": locks.ReleaseFailInfra})))
}
