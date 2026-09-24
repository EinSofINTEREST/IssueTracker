// ResolveMetrics 의 계측 동작 검증 (이슈 #558).
//
// "룰 캐시로 LLM 호출을 줄인다" 는 주장을 뒷받침하는 지표이므로, 라벨과 증가 시점이
// 어긋나면 비율 계산이 조용히 틀린다. Prometheus registry 에서 값을 직접 읽어 확인한다.
package rule_test

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/processor/parser/rule"
)

// counterValue 는 registry 에서 (name, labels) 카운터 값을 읽습니다. 없으면 0.
func counterValue(t *testing.T, reg *prometheus.Registry, name string, labels map[string]string) float64 {
	t.Helper()
	families, err := reg.Gather()
	require.NoError(t, err)
	for _, mf := range families {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			got := map[string]string{}
			for _, lp := range m.GetLabel() {
				got[lp.GetName()] = lp.GetValue()
			}
			match := true
			for k, v := range labels {
				if got[k] != v {
					match = false
					break
				}
			}
			if match {
				return m.GetCounter().GetValue()
			}
		}
	}
	return 0
}

func TestResolveMetrics_NilRegistry_IsNoop(t *testing.T) {
	m := rule.NewResolveMetrics(nil)
	// panic 없이 통과해야 한다 — METRICS 비활성 환경.
	assert.NotPanics(t, func() {
		m.RecordResolve("article", rule.ResolveResultHit)
		m.RecordPageProcessed("article")
	})
}

func TestResolveMetrics_NilReceiver_IsNoop(t *testing.T) {
	var m *rule.ResolveMetrics
	assert.NotPanics(t, func() {
		m.RecordResolve("article", rule.ResolveResultMiss)
		m.RecordPageProcessed("article")
	})
}

func TestResolveMetrics_RecordsByTargetTypeAndResult(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := rule.NewResolveMetrics(reg)

	m.RecordResolve("article", rule.ResolveResultHit)
	m.RecordResolve("article", rule.ResolveResultHit)
	m.RecordResolve("article", rule.ResolveResultMiss)
	m.RecordResolve("category", rule.ResolveResultLLMGenerated)

	assert.Equal(t, 2.0, counterValue(t, reg, "parser_rule_resolve_total",
		map[string]string{"target_type": "article", "result": "hit"}))
	assert.Equal(t, 1.0, counterValue(t, reg, "parser_rule_resolve_total",
		map[string]string{"target_type": "article", "result": "miss"}))
	assert.Equal(t, 1.0, counterValue(t, reg, "parser_rule_resolve_total",
		map[string]string{"target_type": "category", "result": "llm_generated"}))
	// 다른 target_type 으로는 새지 않아야 한다.
	assert.Equal(t, 0.0, counterValue(t, reg, "parser_rule_resolve_total",
		map[string]string{"target_type": "category", "result": "hit"}))
}

func TestResolveMetrics_PagesProcessed_IsDenominator(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := rule.NewResolveMetrics(reg)

	for i := 0; i < 5; i++ {
		m.RecordPageProcessed("article")
	}
	m.RecordResolve("article", rule.ResolveResultLLMGenerated)

	pages := counterValue(t, reg, "parser_pages_processed_total",
		map[string]string{"target_type": "article"})
	llm := counterValue(t, reg, "parser_rule_resolve_total",
		map[string]string{"target_type": "article", "result": "llm_generated"})

	require.Equal(t, 5.0, pages)
	require.Equal(t, 1.0, llm)
	// 이 비율이 이슈 #558 이 말하려는 "페이지 대비 LLM 호출" 이다.
	assert.InDelta(t, 0.2, llm/pages, 1e-9)
}

// TestResolveMetrics_SameRegistryTwice_Idempotent 는 같은 registry 에 두 번 생성해도
// panic 하지 않는지 확인합니다 — 본 패키지의 AutoDemoteMetrics 와 동일 정책.
func TestResolveMetrics_SameRegistryTwice_Idempotent(t *testing.T) {
	reg := prometheus.NewRegistry()
	first := rule.NewResolveMetrics(reg)
	var second *rule.ResolveMetrics
	require.NotPanics(t, func() { second = rule.NewResolveMetrics(reg) })

	first.RecordResolve("article", rule.ResolveResultHit)
	second.RecordResolve("article", rule.ResolveResultHit)

	assert.Equal(t, 2.0, counterValue(t, reg, "parser_rule_resolve_total",
		map[string]string{"target_type": "article", "result": "hit"}),
		"두 인스턴스가 같은 collector 를 공유해야 한다")
}
