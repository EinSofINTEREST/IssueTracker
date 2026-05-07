package refiner

import (
	"errors"

	"github.com/prometheus/client_golang/prometheus"
)

// Metrics 는 refiner 의 Prometheus collector 모음입니다.
//
// nil-Metrics 또는 nil 내부 collector 는 모든 Record* 메소드가 noop —
// 호출자는 nil 검사 없이 항상 호출 가능. NewMetrics(nil) 또는 NewMetrics(registry) 둘 다 허용.
//
// Label 정책:
//   - attempts {result, method}
//   - result: "success" | "skipped" | "error"
//   - method: "algorithm" | "llm" | "none"
//   - llmCalls {status}
//   - status: "success" | "error"
//
// host / rule_id 는 cardinality 폭증 우려로 label 에 넣지 않음 — 운영자가 발견 시 로그로 조회.
type Metrics struct {
	attempts *prometheus.CounterVec
	llmCalls *prometheus.CounterVec
}

// 결과 카테고리 상수 — 호출처에서 오타 방지.
const (
	ResultSuccess = "success"
	ResultSkipped = "skipped"
	ResultError   = "error"

	MethodAlgorithm = "algorithm"
	MethodLLM       = "llm"
	MethodNone      = "none"

	LLMStatusSuccess = "success"
	LLMStatusError   = "error"
)

// NewMetrics 는 refiner Metrics 를 생성합니다. registry 가 nil 이면 모든 collector 가 nil —
// Record* 호출이 noop 으로 동작 (REFINEMENT 활성 + METRICS 비활성 환경 cover).
//
// 동일 registry 에 두 번 호출돼도 idempotent — 기존 collector 재사용 (panic 회피, pkg/llm.MeasuredFactory 와 동일 정책).
func NewMetrics(registry *prometheus.Registry) *Metrics {
	if registry == nil {
		return &Metrics{}
	}
	return &Metrics{
		attempts: registerOrReuseCounter(registry, prometheus.CounterOpts{
			Name: "refinement_attempts_total",
			Help: "path_pattern refinement attempts labeled by result/method.",
		}, []string{"result", "method"}),
		llmCalls: registerOrReuseCounter(registry, prometheus.CounterOpts{
			Name: "refinement_llm_calls_total",
			Help: "LLM Generate calls invoked from refiner labeled by status.",
		}, []string{"status"}),
	}
}

// RecordAttempt 는 정밀화 시도 1건의 결과를 기록합니다.
func (m *Metrics) RecordAttempt(result, method string) {
	if m == nil || m.attempts == nil {
		return
	}
	m.attempts.WithLabelValues(result, method).Inc()
}

// RecordLLMCall 은 LLM Generate 호출 1건의 결과를 기록합니다 (algorithm fallback 시).
func (m *Metrics) RecordLLMCall(status string) {
	if m == nil || m.llmCalls == nil {
		return
	}
	m.llmCalls.WithLabelValues(status).Inc()
}

// registerOrReuseCounter 는 collector 를 등록하거나 기존 collector 를 재사용합니다.
// 동일 registry 에 두 번 NewMetrics 호출 시 panic 회피 (pkg/llm.MeasuredFactory 동일 패턴).
func registerOrReuseCounter(registry *prometheus.Registry, opts prometheus.CounterOpts, labels []string) *prometheus.CounterVec {
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
