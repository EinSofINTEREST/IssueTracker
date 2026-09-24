// resolve_metrics.go 는 파싱 룰 resolve 결과 collector 입니다 (이슈 #558, #543 후속).
//
// "룰 캐시로 LLM 호출을 줄인다" 는 설계 주장을 수치로 뒷받침하기 위한 지표입니다.
//
// 토큰 사용량으로는 이를 말할 수 없습니다 — Response.InputTokens / OutputTokens 는 HTTP
// provider (gemini / openai / anthropic) 만 채우고, 룰 생성이 쓰는 claudegen 경로는
// docker exec + stdout 이라 토큰 정보가 애초에 없습니다 (이슈 #539 조사). 따라서 정직하게
// 말할 수 있는 것은 **호출 수 비율** 뿐이고, 그 분자·분모가 본 파일의 두 지표입니다.
//
//	LLM 호출 비율 = parser_rule_resolve_total{result="llm_generated"} / parser_pages_processed_total
//
// Label 정책:
//   - target_type: model.TargetType 상수 집합 (article / category …) — bounded.
//   - result: 아래 resolveResult* 상수 3종 — bounded.
//
// host 는 label 에 넣지 않습니다. auto_demote 와 달리 본 지표는 모든 페이지 처리마다
// 증가하므로, host 를 붙이면 시계열이 (host × target_type × result) 로 불어납니다.

package rule

import (
	"github.com/prometheus/client_golang/prometheus"
)

// resolve 결과 label 값.
//
// hit / miss 는 **룰의 존재 여부** 를 뜻하며 in-memory 캐시 적중과는 다릅니다.
// 캐시는 DB roundtrip 을 줄이는 최적화일 뿐, LLM 호출 여부를 가르는 것은 "쓸 수 있는 룰이
// 있는가" 이기 때문입니다.
const (
	// ResolveResultHit: 매칭되는 활성 룰을 찾음 — LLM 호출 불필요.
	ResolveResultHit = "hit"
	// ResolveResultMiss: 매칭 룰 없음 — llmgen 후보가 된다.
	ResolveResultMiss = "miss"
	// ResolveResultLLMGenerated: llmgen 이 새 룰을 만들어 저장함 — LLM 호출 1회 발생.
	ResolveResultLLMGenerated = "llm_generated"
)

// ResolveMetrics 는 룰 resolve 결과와 처리 페이지 수의 Prometheus collector 입니다.
//
// nil-ResolveMetrics 또는 nil 내부 collector 는 모든 Record* 가 noop —
// 호출자는 nil 검사 없이 항상 호출 가능 (본 패키지의 AutoDemoteMetrics 동일 정책).
type ResolveMetrics struct {
	resolves       *prometheus.CounterVec // labels: target_type, result
	pagesProcessed *prometheus.CounterVec // labels: target_type
}

// NewResolveMetrics 는 ResolveMetrics 를 생성합니다.
//
// registry 가 nil 이면 모든 collector 가 nil — METRICS 비활성 환경을 cover.
// 동일 registry 에 두 번 호출돼도 idempotent (기존 collector 재사용).
func NewResolveMetrics(registry *prometheus.Registry) *ResolveMetrics {
	if registry == nil {
		return &ResolveMetrics{}
	}
	return &ResolveMetrics{
		resolves: registerOrReuseAutoDemoteCounter(registry, prometheus.CounterOpts{
			Name: "parser_rule_resolve_total",
			Help: "Parser rule resolve outcomes, labeled by target type and result (hit/miss/llm_generated).",
		}, []string{"target_type", "result"}),
		pagesProcessed: registerOrReuseAutoDemoteCounter(registry, prometheus.CounterOpts{
			Name: "parser_pages_processed_total",
			Help: "Pages handed to the parser stage, labeled by target type. Denominator for the LLM call ratio.",
		}, []string{"target_type"}),
	}
}

// RecordResolve 는 resolve 결과 1건을 기록합니다.
func (m *ResolveMetrics) RecordResolve(targetType, result string) {
	if m == nil || m.resolves == nil {
		return
	}
	m.resolves.WithLabelValues(targetType, result).Inc()
}

// RecordPageProcessed 는 parser stage 가 받은 페이지 1건을 기록합니다.
func (m *ResolveMetrics) RecordPageProcessed(targetType string) {
	if m == nil || m.pagesProcessed == nil {
		return
	}
	m.pagesProcessed.WithLabelValues(targetType).Inc()
}
