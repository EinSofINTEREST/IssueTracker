package wiring_test

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/pkg/llm/wiring"
)

// TestBuildProvider_PolicyDefaultIsFixedOrder 는 LLM_POLICY 미설정 시 default 가 FixedOrder 임을 검증합니다.
func TestBuildProvider_PolicyDefaultIsFixedOrder(t *testing.T) {
	clearLLMEnv(t)
	t.Setenv("LLM_ENABLED", "true")
	t.Setenv("GEMINI_API_KEY", "fake-gemini-key")

	log, buf := captureLog(t)
	p := wiring.BuildProvider(log)
	require.NotNil(t, p)

	entry := findChainEnabledLog(t, buf)
	assert.Equal(t, "FixedOrder", entry["policy"])
}

// TestBuildProvider_PolicyChainAlias 는 LLM_POLICY=chain 도 FixedOrder 로 매핑됨을 검증합니다.
func TestBuildProvider_PolicyChainAlias(t *testing.T) {
	clearLLMEnv(t)
	t.Setenv("LLM_ENABLED", "true")
	t.Setenv("LLM_POLICY", "chain")
	t.Setenv("GEMINI_API_KEY", "fake-gemini-key")

	log, buf := captureLog(t)
	p := wiring.BuildProvider(log)
	require.NotNil(t, p)

	entry := findChainEnabledLog(t, buf)
	assert.Equal(t, "FixedOrder", entry["policy"])
}

// TestBuildProvider_PolicyCheapest 는 LLM_POLICY=cheapest 시 CheapestFirst 정책이 선택됨을 검증합니다.
func TestBuildProvider_PolicyCheapest(t *testing.T) {
	clearLLMEnv(t)
	t.Setenv("LLM_ENABLED", "true")
	t.Setenv("LLM_POLICY", "cheapest")
	t.Setenv("GEMINI_API_KEY", "fake-gemini-key")

	log, buf := captureLog(t)
	p := wiring.BuildProvider(log)
	require.NotNil(t, p)

	entry := findChainEnabledLog(t, buf)
	assert.Equal(t, "CheapestFirst", entry["policy"])
}

// TestBuildProvider_PolicyLatency 는 LLM_POLICY=latency 시 LatencyWeighted 정책이 선택됨을 검증합니다.
func TestBuildProvider_PolicyLatency(t *testing.T) {
	clearLLMEnv(t)
	t.Setenv("LLM_ENABLED", "true")
	t.Setenv("LLM_POLICY", "latency")
	t.Setenv("GEMINI_API_KEY", "fake-gemini-key")

	log, buf := captureLog(t)
	p := wiring.BuildProvider(log)
	require.NotNil(t, p)

	entry := findChainEnabledLog(t, buf)
	assert.Equal(t, "LatencyWeighted", entry["policy"])
}

// TestBuildProvider_PolicyHybrid 는 LLM_POLICY=hybrid 시 Hybrid 정책이 선택됨을 검증합니다.
func TestBuildProvider_PolicyHybrid(t *testing.T) {
	clearLLMEnv(t)
	t.Setenv("LLM_ENABLED", "true")
	t.Setenv("LLM_POLICY", "hybrid")
	t.Setenv("GEMINI_API_KEY", "fake-gemini-key")

	log, buf := captureLog(t)
	p := wiring.BuildProvider(log)
	require.NotNil(t, p)

	entry := findChainEnabledLog(t, buf)
	assert.Equal(t, "Hybrid", entry["policy"])
}

// TestBuildProvider_PolicyHybridWithCustomWeights 는 LLM_HYBRID_WEIGHTS 가 valid 일 때 정상 통과를 검증합니다.
func TestBuildProvider_PolicyHybridWithCustomWeights(t *testing.T) {
	clearLLMEnv(t)
	t.Setenv("LLM_ENABLED", "true")
	t.Setenv("LLM_POLICY", "hybrid")
	t.Setenv("LLM_HYBRID_WEIGHTS", "2.0,1.5,0.25")
	t.Setenv("GEMINI_API_KEY", "fake-gemini-key")

	log, buf := captureLog(t)
	p := wiring.BuildProvider(log)
	require.NotNil(t, p)

	entry := findChainEnabledLog(t, buf)
	assert.Equal(t, "Hybrid", entry["policy"])
	// "invalid LLM_HYBRID_WEIGHTS" warn 로그가 없어야 함 — 출력 전체에서 substring 미발견 확인.
	assert.NotContains(t, buf.String(), "invalid LLM_HYBRID_WEIGHTS")
}

// TestBuildProvider_PolicyHybridWithInvalidWeights 는 LLM_HYBRID_WEIGHTS 가 invalid 일 때 default 로 fallback 됨을 검증합니다.
func TestBuildProvider_PolicyHybridWithInvalidWeights(t *testing.T) {
	clearLLMEnv(t)
	t.Setenv("LLM_ENABLED", "true")
	t.Setenv("LLM_POLICY", "hybrid")
	t.Setenv("LLM_HYBRID_WEIGHTS", "not,a,float")
	t.Setenv("GEMINI_API_KEY", "fake-gemini-key")

	log, buf := captureLog(t)
	p := wiring.BuildProvider(log)
	require.NotNil(t, p, "invalid weights → default fallback, provider 정상 생성")

	entry := findChainEnabledLog(t, buf)
	assert.Equal(t, "Hybrid", entry["policy"])
	assert.Contains(t, buf.String(), "invalid LLM_HYBRID_WEIGHTS")
}

// TestBuildProvider_PolicyHybridWithWrongCount 는 LLM_HYBRID_WEIGHTS comma 개수가 잘못된 경우 default fallback 검증.
func TestBuildProvider_PolicyHybridWithWrongCount(t *testing.T) {
	clearLLMEnv(t)
	t.Setenv("LLM_ENABLED", "true")
	t.Setenv("LLM_POLICY", "hybrid")
	t.Setenv("LLM_HYBRID_WEIGHTS", "1.0,2.0")
	t.Setenv("GEMINI_API_KEY", "fake-gemini-key")

	log, buf := captureLog(t)
	p := wiring.BuildProvider(log)
	require.NotNil(t, p)

	assert.Contains(t, buf.String(), "invalid LLM_HYBRID_WEIGHTS")
}

// TestBuildProvider_PolicyHybridNegativeWeightRejected 는 음수 가중치를 invalid 로 거부함을 검증합니다.
func TestBuildProvider_PolicyHybridNegativeWeightRejected(t *testing.T) {
	clearLLMEnv(t)
	t.Setenv("LLM_ENABLED", "true")
	t.Setenv("LLM_POLICY", "hybrid")
	t.Setenv("LLM_HYBRID_WEIGHTS", "1.0,-0.5,0.5")
	t.Setenv("GEMINI_API_KEY", "fake-gemini-key")

	log, buf := captureLog(t)
	p := wiring.BuildProvider(log)
	require.NotNil(t, p)

	assert.Contains(t, buf.String(), "invalid LLM_HYBRID_WEIGHTS")
}

// TestBuildProvider_UnknownPolicyFallsBackToFixed 는 invalid LLM_POLICY 가 FixedOrder 로 fallback 됨을 검증합니다.
func TestBuildProvider_UnknownPolicyFallsBackToFixed(t *testing.T) {
	clearLLMEnv(t)
	t.Setenv("LLM_ENABLED", "true")
	t.Setenv("LLM_POLICY", "nonexistent-policy")
	t.Setenv("GEMINI_API_KEY", "fake-gemini-key")

	log, buf := captureLog(t)
	p := wiring.BuildProvider(log)
	require.NotNil(t, p)

	entry := findChainEnabledLog(t, buf)
	assert.Equal(t, "FixedOrder", entry["policy"])
	assert.Contains(t, buf.String(), "unknown LLM_POLICY")
}

// TestBuildProviderWithOptions_PrometheusRegistryWired 는 registry 가 전달되면 collector 등록이
// 실패 없이 진행되고 log 의 metrics_registered=true 가 기록됨을 검증합니다.
func TestBuildProviderWithOptions_PrometheusRegistryWired(t *testing.T) {
	clearLLMEnv(t)
	t.Setenv("LLM_ENABLED", "true")
	t.Setenv("GEMINI_API_KEY", "fake-gemini-key")

	reg := prometheus.NewRegistry()
	log, buf := captureLog(t)

	p := wiring.BuildProviderWithOptions(log, wiring.Options{PrometheusRegistry: reg})
	require.NotNil(t, p)

	entry := findChainEnabledLog(t, buf)
	assert.Equal(t, true, entry["metrics_registered"])

	// 등록된 collector 가 동일 registry 에 두 번째 호출되어도 (idempotent) panic 안 함을 검증 —
	// HistogramVec / CounterVec 은 observe 전엔 Gather() 가 빈 슬라이스 반환할 수 있어
	// 등록 자체의 sanity 는 두번째 wiring 으로 확인.
	assert.NotPanics(t, func() {
		_ = wiring.BuildProviderWithOptions(log, wiring.Options{PrometheusRegistry: reg})
	}, "동일 registry 재사용 시 panic 없음 (collector 중복 등록 보호)")
}

// TestBuildProviderWithOptions_NilRegistry 는 registry 가 nil 이어도 정상 wiring 됨을 검증합니다.
func TestBuildProviderWithOptions_NilRegistry(t *testing.T) {
	clearLLMEnv(t)
	t.Setenv("LLM_ENABLED", "true")
	t.Setenv("GEMINI_API_KEY", "fake-gemini-key")

	log, buf := captureLog(t)
	p := wiring.BuildProviderWithOptions(log, wiring.Options{PrometheusRegistry: nil})
	require.NotNil(t, p)

	entry := findChainEnabledLog(t, buf)
	assert.Equal(t, false, entry["metrics_registered"])
}
