// metrics.go 는 LLM rule generator 운영 지표입니다 (이슈 #169, #165 기반).
//
// pkg/llm 의 provider 지표(`*_provider_call_total` / `*_provider_latency_seconds`)는
// **provider 호출 단위** 라, "룰 생성이 몇 번 시도되고 몇 번 성공했는지" 를 말해주지
// 않습니다. 같은 provider 를 enrich 도 쓰기 때문입니다. 본 지표는 rule generator 관점입니다.
//
// Label 정책:
//   - status: 아래 GenStatus* 상수 (success / validation_fail / llm_error / cap_exceeded) — bounded
//   - target_type: model.TargetType 상수 집합 — bounded
//   - window: daily / hourly — bounded
//
// host 는 넣지 않습니다. 룰 생성은 host 마다 한 번씩 일어나 카디널리티가 host 수만큼
// 늘어나며, host 별 추적은 이미 auto_demote / breaker 쪽이 담당합니다.

package llmgen

import (
	"errors"

	"github.com/prometheus/client_golang/prometheus"
)

// 룰 생성 시도의 결과 label.
const (
	GenStatusSuccess        = "success"
	GenStatusValidationFail = "validation_fail"
	GenStatusLLMError       = "llm_error"
	GenStatusCapExceeded    = "cap_exceeded"
)

// GeneratorMetrics 는 rule generator Prometheus collector 입니다.
//
// nil-GeneratorMetrics 또는 nil 내부 collector 는 모든 Record*/Set* 이 noop —
// 호출자는 nil 검사 없이 항상 호출 가능.
type GeneratorMetrics struct {
	calls        *prometheus.CounterVec   // labels: status
	latency      *prometheus.HistogramVec // labels: status
	inflight     prometheus.Gauge
	inserted     *prometheus.CounterVec // labels: target_type
	capRemaining *prometheus.GaugeVec   // labels: window
}

// NewGeneratorMetrics 는 GeneratorMetrics 를 생성합니다. registry 가 nil 이면 noop.
//
// 동일 registry 에 두 번 호출돼도 idempotent — 기존 collector 재사용 (panic 회피).
func NewGeneratorMetrics(registry *prometheus.Registry) *GeneratorMetrics {
	if registry == nil {
		return &GeneratorMetrics{}
	}
	return &GeneratorMetrics{
		calls: regCounterVec(registry, prometheus.CounterOpts{
			Name: "llm_rule_generator_calls_total",
			Help: "Rule generation attempts, labeled by outcome.",
		}, []string{"status"}),
		latency: regHistogramVec(registry, prometheus.HistogramOpts{
			Name:    "llm_rule_generator_latency_seconds",
			Help:    "Rule generation wall time, labeled by outcome.",
			Buckets: prometheus.ExponentialBuckets(0.5, 2, 8), // 0.5s ~ 64s — claudegen 세션은 초 단위
		}, []string{"status"}),
		inflight: regGauge(registry, prometheus.GaugeOpts{
			Name: "llm_rule_generator_inflight",
			Help: "Rule generations currently running.",
		}),
		inserted: regCounterVec(registry, prometheus.CounterOpts{
			Name: "llm_rule_generator_inserted_total",
			Help: "Rules inserted and enabled, labeled by target type.",
		}, []string{"target_type"}),
		capRemaining: regGaugeVec(registry, prometheus.GaugeOpts{
			Name: "llm_rule_generator_cap_remaining",
			Help: "Remaining LLM calls in the sliding window before the cap blocks generation.",
		}, []string{"window"}),
	}
}

// RecordCall 은 룰 생성 시도 1건과 소요 시간을 기록합니다.
func (m *GeneratorMetrics) RecordCall(status string, seconds float64) {
	if m == nil {
		return
	}
	if m.calls != nil {
		m.calls.WithLabelValues(status).Inc()
	}
	if m.latency != nil {
		m.latency.WithLabelValues(status).Observe(seconds)
	}
}

// RecordInserted 는 룰 INSERT 성공 1건을 기록합니다.
func (m *GeneratorMetrics) RecordInserted(targetType string) {
	if m == nil || m.inserted == nil {
		return
	}
	m.inserted.WithLabelValues(targetType).Inc()
}

// IncInflight / DecInflight 는 진행 중인 생성 수를 추적합니다.
func (m *GeneratorMetrics) IncInflight() {
	if m == nil || m.inflight == nil {
		return
	}
	m.inflight.Inc()
}

func (m *GeneratorMetrics) DecInflight() {
	if m == nil || m.inflight == nil {
		return
	}
	m.inflight.Dec()
}

// SetCapRemaining 은 window 의 잔여 호출 수를 기록합니다 — 알림 임계 설정용.
func (m *GeneratorMetrics) SetCapRemaining(window string, remaining float64) {
	if m == nil || m.capRemaining == nil {
		return
	}
	m.capRemaining.WithLabelValues(window).Set(remaining)
}

func regCounterVec(r *prometheus.Registry, opts prometheus.CounterOpts, labels []string) *prometheus.CounterVec {
	c := prometheus.NewCounterVec(opts, labels)
	if err := r.Register(c); err != nil {
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

func regHistogramVec(r *prometheus.Registry, opts prometheus.HistogramOpts, labels []string) *prometheus.HistogramVec {
	h := prometheus.NewHistogramVec(opts, labels)
	if err := r.Register(h); err != nil {
		var are prometheus.AlreadyRegisteredError
		if errors.As(err, &are) {
			if existing, ok := are.ExistingCollector.(*prometheus.HistogramVec); ok {
				return existing
			}
		}
		panic(err)
	}
	return h
}

func regGauge(r *prometheus.Registry, opts prometheus.GaugeOpts) prometheus.Gauge {
	g := prometheus.NewGauge(opts)
	if err := r.Register(g); err != nil {
		var are prometheus.AlreadyRegisteredError
		if errors.As(err, &are) {
			if existing, ok := are.ExistingCollector.(prometheus.Gauge); ok {
				return existing
			}
		}
		panic(err)
	}
	return g
}

func regGaugeVec(r *prometheus.Registry, opts prometheus.GaugeOpts, labels []string) *prometheus.GaugeVec {
	g := prometheus.NewGaugeVec(opts, labels)
	if err := r.Register(g); err != nil {
		var are prometheus.AlreadyRegisteredError
		if errors.As(err, &are) {
			if existing, ok := are.ExistingCollector.(*prometheus.GaugeVec); ok {
				return existing
			}
		}
		panic(err)
	}
	return g
}
