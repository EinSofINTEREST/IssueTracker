// cost_metrics.go 는 enrichment 호출 비용 관측 collector 입니다 (이슈 #456).
//
// 비용 cap 이 실제로 언제 얼마나 발동했는지 알 수 없으면, 한도를 잘못 잡아 enrichment 가
// 조용히 대부분 건너뛰어져도 알아차릴 수 없습니다.
//
//	건너뛴 비율 = enrich_calls_dropped_budget_total / (enrich_calls_total + dropped)
//
// Label 정책:
//   - reason: SkipReason 상수 집합 (daily_budget / backlog) — bounded.

package worker

import (
	"errors"

	"github.com/prometheus/client_golang/prometheus"
)

// CostMetrics 는 enrichment 호출/차단 Prometheus collector 입니다.
//
// nil-CostMetrics 또는 nil 내부 collector 는 Record* 가 noop — 호출자는 nil 검사 불필요.
type CostMetrics struct {
	calls   prometheus.Counter
	dropped *prometheus.CounterVec // labels: reason
}

// NewCostMetrics 는 CostMetrics 를 생성합니다. registry 가 nil 이면 noop collector.
//
// 동일 registry 에 두 번 호출돼도 idempotent — 기존 collector 재사용 (panic 회피).
func NewCostMetrics(registry *prometheus.Registry) *CostMetrics {
	if registry == nil {
		return &CostMetrics{}
	}
	calls := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "enrich_calls_total",
		Help: "Enrichment runs that passed the cost guard.",
	})
	dropped := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "enrich_calls_dropped_budget_total",
		Help: "Enrichment runs skipped by the cost guard, labeled by reason.",
	}, []string{"reason"})

	return &CostMetrics{
		calls:   registerOrReuseCounter(registry, calls),
		dropped: registerOrReuseCounterVec(registry, dropped),
	}
}

// RecordCall 은 가드를 통과한 enrichment 1건을 기록합니다.
func (m *CostMetrics) RecordCall() {
	if m == nil || m.calls == nil {
		return
	}
	m.calls.Inc()
}

// RecordDropped 는 가드가 건너뛴 enrichment 1건을 기록합니다.
func (m *CostMetrics) RecordDropped(reason string) {
	if m == nil || m.dropped == nil {
		return
	}
	m.dropped.WithLabelValues(reason).Inc()
}

func registerOrReuseCounter(registry *prometheus.Registry, c prometheus.Counter) prometheus.Counter {
	if err := registry.Register(c); err != nil {
		var are prometheus.AlreadyRegisteredError
		if errors.As(err, &are) {
			if existing, ok := are.ExistingCollector.(prometheus.Counter); ok {
				return existing
			}
		}
		panic(err) // incompatible collector 충돌 — 운영자 개입 필요
	}
	return c
}

func registerOrReuseCounterVec(registry *prometheus.Registry, c *prometheus.CounterVec) *prometheus.CounterVec {
	if err := registry.Register(c); err != nil {
		var are prometheus.AlreadyRegisteredError
		if errors.As(err, &are) {
			if existing, ok := are.ExistingCollector.(*prometheus.CounterVec); ok {
				return existing
			}
		}
		panic(err)
	}
	return c
}
