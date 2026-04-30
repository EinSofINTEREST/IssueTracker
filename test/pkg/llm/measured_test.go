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
