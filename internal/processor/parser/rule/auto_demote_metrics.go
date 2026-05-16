// auto_demote_metrics.go 는 index-only 자동 강등 metric collector 입니다 (이슈 #477).
//
// Prometheus collector — 본 패키지의 다른 collector (refiner Metrics 등) 와 동일 패턴.
//
// Label 정책:
//   - host: site 호스트명. 카디널리티는 등록된 site 수 (≈수십~수백) — bounded.

package rule

import (
	"errors"

	"github.com/prometheus/client_golang/prometheus"
)

// AutoDemoteMetrics 는 index-only 자동 강등의 Prometheus collector 입니다.
//
// nil-Metrics 또는 nil 내부 collector 는 모든 Record* 메소드가 noop —
// 호출자는 nil 검사 없이 항상 호출 가능. NewAutoDemoteMetrics(nil) 또는
// NewAutoDemoteMetrics(registry) 둘 다 허용.
type AutoDemoteMetrics struct {
	autoDemoted *prometheus.CounterVec // labels: host
}

// NewAutoDemoteMetrics 는 AutoDemoteMetrics 를 생성합니다.
//
// registry 가 nil 이면 모든 collector 가 nil — Record* 호출이 noop 으로 동작
// (METRICS 비활성 환경 cover).
//
// 동일 registry 에 두 번 호출돼도 idempotent — 기존 collector 재사용 (panic 회피,
// pkg/llm.MeasuredFactory / refiner.Metrics 동일 정책).
func NewAutoDemoteMetrics(registry *prometheus.Registry) *AutoDemoteMetrics {
	if registry == nil {
		return &AutoDemoteMetrics{}
	}
	return &AutoDemoteMetrics{
		autoDemoted: registerOrReuseAutoDemoteCounter(registry, prometheus.CounterOpts{
			Name: "parser_index_page_auto_demoted_total",
			Help: "Pages auto-demoted to extract_links_only by index-only heuristic, labeled by host.",
		}, []string{"host"}),
	}
}

// RecordAutoDemote 는 host 의 자동 강등 1건을 기록합니다 (Insert 성공 시).
func (m *AutoDemoteMetrics) RecordAutoDemote(host string) {
	if m == nil || m.autoDemoted == nil {
		return
	}
	m.autoDemoted.WithLabelValues(host).Inc()
}

// registerOrReuseAutoDemoteCounter 는 collector 를 등록하거나 기존 collector 를 재사용합니다.
// 동일 registry 에 두 번 NewAutoDemoteMetrics 호출 시 panic 회피.
func registerOrReuseAutoDemoteCounter(registry *prometheus.Registry, opts prometheus.CounterOpts, labels []string) *prometheus.CounterVec {
	counter := prometheus.NewCounterVec(opts, labels)
	if err := registry.Register(counter); err != nil {
		var are prometheus.AlreadyRegisteredError
		if errors.As(err, &are) {
			if existing, ok := are.ExistingCollector.(*prometheus.CounterVec); ok {
				return existing
			}
		}
		panic(err) // incompatible collector 충돌 — 운영자 개입 필요
	}
	return counter
}
