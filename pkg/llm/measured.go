package llm

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// MeasuredProvider wraps a Provider, recording per-call latency and success/failure metrics.
//
// MeasuredProvider 는 다른 Provider 를 wrap 하여 호출 시 latency / 성공·실패 metric 을 기록합니다 (이슈 #144).
//
//   - in-memory EMA (LatencyMs) — routing policy 가 dynamic 가중치로 활용
//   - Prometheus metric (히스토그램 / counter) — /metrics endpoint 로 export (이슈 #165 의존)
//
// Prometheus registry 는 호출자가 주입 — nil 이면 metric 등록 skip (in-memory EMA 만 동작).
type MeasuredProvider struct {
	inner Provider
	name  string
	stats *Stats

	// Prometheus metrics (registry 가 nil 이면 nil)
	latencyHist *prometheus.HistogramVec
	callCounter *prometheus.CounterVec
}

// Stats holds in-memory rolling metrics for a wrapped provider.
//
// Stats 는 wrap 된 provider 의 in-memory rolling metric 입니다.
// LatencyMs 는 EMA (alpha 가중치) 로, Calls / Failures 는 atomic counter 로 누적됩니다.
type Stats struct {
	// Calls 는 누적 호출 수 (성공 + 실패).
	Calls atomic.Uint64

	// Failures 는 누적 실패 호출 수.
	Failures atomic.Uint64

	// latency EMA — float64 직접 atomic 어렵기에 mu 보호.
	mu        sync.RWMutex
	latencyMs float64
}

// LatencyEMAAlpha: EMA 가중치 (0~1). 0.2 = 새 측정치 20%, 기존 EMA 80%.
// 작을수록 안정적, 클수록 최근 변화에 빠르게 반응. 0.2 는 일반적인 운영 default.
const LatencyEMAAlpha = 0.2

// LatencyMs returns the current EMA latency in milliseconds (0 if no calls yet).
func (s *Stats) LatencyMs() float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.latencyMs
}

// FailureRate returns the cumulative failure ratio in [0, 1] (0 if no calls yet).
func (s *Stats) FailureRate() float64 {
	calls := s.Calls.Load()
	if calls == 0 {
		return 0
	}
	return float64(s.Failures.Load()) / float64(calls)
}

func (s *Stats) record(latencyMs float64, failed bool) {
	s.Calls.Add(1)
	if failed {
		s.Failures.Add(1)
	}
	s.mu.Lock()
	if s.latencyMs == 0 {
		s.latencyMs = latencyMs
	} else {
		s.latencyMs = LatencyEMAAlpha*latencyMs + (1-LatencyEMAAlpha)*s.latencyMs
	}
	s.mu.Unlock()
}

// NewMeasuredProvider wraps inner with latency + success metrics.
//
// registry 가 nil 이면 Prometheus 등록 skip — in-memory Stats 는 항상 동작.
// labelPrefix 는 metric 이름 prefix (예: "llm" → "llm_provider_call_total" / "llm_provider_latency_seconds").
func NewMeasuredProvider(inner Provider, registry *prometheus.Registry, labelPrefix string) *MeasuredProvider {
	if labelPrefix == "" {
		labelPrefix = "llm"
	}
	mp := &MeasuredProvider{
		inner: inner,
		name:  inner.Name(),
		stats: &Stats{},
	}
	if registry != nil {
		mp.latencyHist = prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    labelPrefix + "_provider_latency_seconds",
			Help:    "LLM provider call latency in seconds.",
			Buckets: []float64{0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60},
		}, []string{"provider", "status"})
		mp.callCounter = prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: labelPrefix + "_provider_call_total",
			Help: "LLM provider call counter labeled by provider/status.",
		}, []string{"provider", "status"})
		registry.MustRegister(mp.latencyHist, mp.callCounter)
	}
	return mp
}

// Name returns the wrapped provider's name (transparent wrapping).
func (m *MeasuredProvider) Name() string { return m.name }

// Stats returns the in-memory rolling stats — routing policy 가 직접 읽음.
func (m *MeasuredProvider) Stats() *Stats { return m.stats }

// Generate delegates to the inner provider, recording latency + status metrics.
func (m *MeasuredProvider) Generate(ctx context.Context, req Request) (*Response, error) {
	start := time.Now()
	resp, err := m.inner.Generate(ctx, req)
	elapsed := time.Since(start)

	failed := err != nil
	m.stats.record(float64(elapsed.Milliseconds()), failed)

	if m.latencyHist != nil {
		status := "success"
		if failed {
			status = "failure"
		}
		m.latencyHist.WithLabelValues(m.name, status).Observe(elapsed.Seconds())
		m.callCounter.WithLabelValues(m.name, status).Inc()
	}
	return resp, err
}
