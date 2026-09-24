// 본 파일은 룰 생성 시도의 audit 기록과, 그와 함께 메워진 지표 누락을 검증합니다 (이슈 #583).
package llmgen_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/processor/fetcher/core"
	"issuetracker/internal/processor/parser/rule/llmgen"
	"issuetracker/internal/storage/model"
)

// callsByStatus 는 llm_rule_generator_calls_total 을 status 별로 집계합니다.
//
// audit 이 지표를 기록하는지 확인할 유일한 외부 관측 지점이다 — audit 로그 자체는
// zerolog 출력이라 단정하기 번거롭고, 정작 회귀가 났던 것은 지표 쪽이다.
func callsByStatus(t *testing.T, reg *prometheus.Registry) map[string]float64 {
	t.Helper()
	families, err := reg.Gather()
	require.NoError(t, err)

	out := map[string]float64{}
	for _, mf := range families {
		if mf.GetName() != "llm_rule_generator_calls_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			status := ""
			for _, l := range m.GetLabel() {
				if l.GetName() == "status" {
					status = l.GetValue()
				}
			}
			out[status] += counterValue(m)
		}
	}
	return out
}

func counterValue(m *dto.Metric) float64 {
	if c := m.GetCounter(); c != nil {
		return c.GetValue()
	}
	return 0
}

// waitForStatus 는 비동기 생성 goroutine 이 지표를 남길 때까지 기다립니다.
func waitForStatus(t *testing.T, reg *prometheus.Registry, status string) map[string]float64 {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got := callsByStatus(t, reg)
		if got[status] > 0 {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
	return callsByStatus(t, reg)
}

func newGeneratorWithMetrics(t *testing.T, provider *fakeProvider, repo *recordingRepo) (*llmgen.Generator, *prometheus.Registry) {
	t.Helper()
	g, _ := newGenerator(t, provider, repo)
	reg := prometheus.NewRegistry()
	g.SetGeneratorMetrics(llmgen.NewGeneratorMetrics(reg))
	return g, reg
}

// TestAudit_Success_RecordsSuccessStatus 는 성공 경로가 한 번만 기록되는지 검증합니다.
//
// 구현 과정에서 성공이 **두 번** 세어질 뻔했다 — 기존 본문의 RecordCall 과 새 audit 이
// 겹쳤다. 중복은 성공률을 부풀리므로 개수까지 단정한다.
func TestAudit_Success_RecordsSuccessStatus(t *testing.T) {
	provider := &fakeProvider{
		name: "fake",
		response: `{
			"title": {"css": "h1"},
			"main_content": {"css": "article p"}
		}`,
	}
	repo := &recordingRepo{}
	g, reg := newGeneratorWithMetrics(t, provider, repo)

	g.Enqueue(context.Background(), "example.com", model.TargetTypePage, &core.RawContent{
		URL: "https://example.com/article/1", HTML: samplePageHTML,
	}, 0, "", 0)

	got := waitForStatus(t, reg, "success")
	assert.Equal(t, float64(1), got["success"], "성공은 정확히 1회 기록돼야 한다 (중복 금지)")
	assert.Zero(t, got["llm_error"])
	assert.Zero(t, got["validation_fail"])
}

// TestAudit_ValidationFail_RecordsStatus 는 검증 실패가 지표에 잡히는지 검증합니다.
//
// **이 상태는 상수로만 존재하고 한 번도 기록되지 않았다** (이슈 #583 에서 발견).
// 실패가 지표에 없으면 운영자가 실패율을 볼 수 없다.
func TestAudit_ValidationFail_RecordsStatus(t *testing.T) {
	provider := &fakeProvider{
		name: "fake",
		response: `{
			"title": {"css": "h1.does-not-exist"},
			"main_content": {"css": "article p"}
		}`,
	}
	repo := &recordingRepo{}
	g, reg := newGeneratorWithMetrics(t, provider, repo)

	g.Enqueue(context.Background(), "example.com", model.TargetTypePage, &core.RawContent{
		URL: "https://example.com/article/1", HTML: samplePageHTML,
	}, 0, "", 0)

	got := waitForStatus(t, reg, "validation_fail")
	assert.Equal(t, float64(1), got["validation_fail"])
	assert.Zero(t, got["success"])
	assert.Empty(t, repo.inserts(), "검증 실패 시 INSERT 없어야 한다")
}

// TestAudit_LLMError_RecordsStatus 는 LLM 오류가 지표에 잡히는지 검증합니다.
// validation_fail 과 마찬가지로 기록되지 않던 상태다.
func TestAudit_LLMError_RecordsStatus(t *testing.T) {
	provider := &fakeProvider{name: "fake", err: errors.New("network down")}
	repo := &recordingRepo{}
	g, reg := newGeneratorWithMetrics(t, provider, repo)

	g.Enqueue(context.Background(), "example.com", model.TargetTypePage, &core.RawContent{
		URL: "https://example.com/x", HTML: samplePageHTML,
	}, 0, "", 0)

	got := waitForStatus(t, reg, "llm_error")
	assert.Equal(t, float64(1), got["llm_error"])
	assert.Zero(t, got["success"])
	assert.Zero(t, got["validation_fail"], "LLM 오류가 검증 실패로 분류되면 안 된다")
}

// TestAudit_ValidationFailAndLLMError_Distinguished 는 두 실패가 서로 다른 status 로
// 분류되는지 검증합니다.
//
// 둘을 뭉뚱그리면 "LLM 이 응답을 못 준 것" 과 "응답은 왔는데 selector 가 안 맞은 것" 을
// 구별할 수 없어, 대응 (provider 교체 vs 프롬프트 개선) 을 고를 수 없다.
func TestAudit_ValidationFailAndLLMError_Distinguished(t *testing.T) {
	repo := &recordingRepo{}

	bad := &fakeProvider{name: "fake", response: `{"title": {"css": "h1.nope"}}`}
	gBad, regBad := newGeneratorWithMetrics(t, bad, repo)
	gBad.Enqueue(context.Background(), "a.example.com", model.TargetTypePage, &core.RawContent{
		URL: "https://a.example.com/1", HTML: samplePageHTML,
	}, 0, "", 0)

	down := &fakeProvider{name: "fake", err: errors.New("boom")}
	gDown, regDown := newGeneratorWithMetrics(t, down, repo)
	gDown.Enqueue(context.Background(), "b.example.com", model.TargetTypePage, &core.RawContent{
		URL: "https://b.example.com/1", HTML: samplePageHTML,
	}, 0, "", 0)

	gotBad := waitForStatus(t, regBad, "validation_fail")
	gotDown := waitForStatus(t, regDown, "llm_error")

	assert.Equal(t, float64(1), gotBad["validation_fail"])
	assert.Zero(t, gotBad["llm_error"])

	assert.Equal(t, float64(1), gotDown["llm_error"])
	assert.Zero(t, gotDown["validation_fail"])
}
