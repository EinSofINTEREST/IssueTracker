package llm_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"

	"issuetracker/pkg/llm"
)

// stubProvider — 테스트용 fixed-latency provider.
type stubProvider struct {
	name      string
	latency   time.Duration
	failTimes int
	calls     int
}

func (s *stubProvider) Name() string { return s.name }
func (s *stubProvider) Generate(ctx context.Context, _ llm.Request) (*llm.Response, error) {
	s.calls++
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(s.latency):
	}
	if s.failTimes > 0 {
		s.failTimes--
		return nil, errors.New("stub failure")
	}
	return &llm.Response{Content: "ok", Model: s.name}, nil
}

func TestMeasuredProvider_RecordsLatencyAndCalls(t *testing.T) {
	stub := &stubProvider{name: "stub", latency: 5 * time.Millisecond}
	mp := llm.NewMeasuredFactory(nil, "test").Wrap(stub)

	for i := 0; i < 3; i++ {
		_, err := mp.Generate(context.Background(), llm.Request{})
		assert.NoError(t, err)
	}

	stats := mp.Stats()
	assert.Equal(t, uint64(3), stats.Calls())
	assert.Equal(t, uint64(0), stats.Failures())
	assert.Greater(t, stats.LatencyMs(), 0.0)
	assert.Equal(t, 0.0, stats.FailureRate())
}

func TestMeasuredProvider_RecordsFailures(t *testing.T) {
	stub := &stubProvider{name: "stub", latency: time.Millisecond, failTimes: 2}
	mp := llm.NewMeasuredFactory(nil, "test").Wrap(stub)

	for i := 0; i < 5; i++ {
		_, _ = mp.Generate(context.Background(), llm.Request{})
	}

	stats := mp.Stats()
	assert.Equal(t, uint64(5), stats.Calls())
	assert.Equal(t, uint64(2), stats.Failures())
	assert.InDelta(t, 0.4, stats.FailureRate(), 0.001)
}

func TestMeasuredProvider_RegistersPrometheusMetrics(t *testing.T) {
	stub := &stubProvider{name: "stub", latency: time.Millisecond}
	registry := prometheus.NewRegistry()
	mp := llm.NewMeasuredFactory(registry, "llm").Wrap(stub)

	_, err := mp.Generate(context.Background(), llm.Request{})
	assert.NoError(t, err)

	mfs, err := registry.Gather()
	assert.NoError(t, err)
	names := make([]string, 0, len(mfs))
	for _, m := range mfs {
		names = append(names, m.GetName())
	}
	assert.Contains(t, names, "llm_provider_call_total")
	assert.Contains(t, names, "llm_provider_latency_seconds")
}

// 동일 registry 에 두 번 호출되어도 panic 없이 동일 collector 재사용 — idempotent 보장 (PR #167 CodeRabbit 피드백).
func TestMeasuredFactory_IdempotentOnDuplicateRegistration(t *testing.T) {
	registry := prometheus.NewRegistry()

	var f1, f2 *llm.MeasuredFactory
	assert.NotPanics(t, func() {
		f1 = llm.NewMeasuredFactory(registry, "llm")
		f2 = llm.NewMeasuredFactory(registry, "llm")
	}, "동일 registry 에 두 번 호출되어도 panic 안 됨")

	stub1 := &stubProvider{name: "a", latency: time.Millisecond}
	stub2 := &stubProvider{name: "b", latency: time.Millisecond}
	_, _ = f1.Wrap(stub1).Generate(context.Background(), llm.Request{})
	_, _ = f2.Wrap(stub2).Generate(context.Background(), llm.Request{})

	// 두 factory 가 같은 collector 를 공유하므로 metric 이름은 1회만 등록.
	mfs, err := registry.Gather()
	assert.NoError(t, err)
	counts := map[string]int{}
	for _, m := range mfs {
		counts[m.GetName()]++
	}
	assert.Equal(t, 1, counts["llm_provider_call_total"])
	assert.Equal(t, 1, counts["llm_provider_latency_seconds"])
}
