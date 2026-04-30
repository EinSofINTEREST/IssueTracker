package policy_test

import (
	"context"
	"errors"
	"math/rand/v2"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"issuetracker/pkg/llm"
	"issuetracker/pkg/llm/policy"
)

// fakeProvider — 테스트용 noop provider.
type fakeProvider struct{ name string }

func (f *fakeProvider) Name() string { return f.name }
func (f *fakeProvider) Generate(_ context.Context, _ llm.Request) (*llm.Response, error) {
	return &llm.Response{Content: f.name}, nil
}

// stubLatencyProvider — 고정 latency 응답.
type stubLatencyProvider struct {
	name    string
	latency time.Duration
}

func (s *stubLatencyProvider) Name() string { return s.name }
func (s *stubLatencyProvider) Generate(_ context.Context, _ llm.Request) (*llm.Response, error) {
	time.Sleep(s.latency)
	return &llm.Response{Content: s.name}, nil
}

func TestCheapestFirst_OrdersByInputCost(t *testing.T) {
	caps := llm.NewStaticCapabilitiesProviderFrom(map[string]map[string]llm.Capabilities{
		"a": {"m": {CostInputPer1M: 5.0, CostOutputPer1M: 20.0}},
		"b": {"m": {CostInputPer1M: 1.0, CostOutputPer1M: 4.0}},
		"c": {"m": {CostInputPer1M: 3.0, CostOutputPer1M: 12.0}},
	})
	pol := policy.NewCheapestFirst(caps)

	pa, pb, pc := &fakeProvider{name: "a"}, &fakeProvider{name: "b"}, &fakeProvider{name: "c"}
	out, err := pol.Select(context.Background(), llm.Request{Model: "m"}, []llm.Provider{pa, pb, pc})
	assert.NoError(t, err)
	assert.Equal(t, []llm.Provider{pb, pc, pa}, out)
}

func TestCheapestFirst_TieBreakerByOutputCost(t *testing.T) {
	caps := llm.NewStaticCapabilitiesProviderFrom(map[string]map[string]llm.Capabilities{
		"a": {"m": {CostInputPer1M: 1.0, CostOutputPer1M: 10.0}},
		"b": {"m": {CostInputPer1M: 1.0, CostOutputPer1M: 5.0}},
	})
	pol := policy.NewCheapestFirst(caps)

	pa, pb := &fakeProvider{name: "a"}, &fakeProvider{name: "b"}
	out, _ := pol.Select(context.Background(), llm.Request{Model: "m"}, []llm.Provider{pa, pb})
	assert.Equal(t, []llm.Provider{pb, pa}, out)
}

func TestLatencyWeighted_OrdersByLatency(t *testing.T) {
	caps := llm.NewStaticCapabilitiesProviderFrom(map[string]map[string]llm.Capabilities{
		"a": {"m": {AvgLatencyMs: 1000}},
		"b": {"m": {AvgLatencyMs: 200}},
		"c": {"m": {AvgLatencyMs: 500}},
	})
	pol := policy.NewLatencyWeighted(caps)

	pa, pb, pc := &fakeProvider{name: "a"}, &fakeProvider{name: "b"}, &fakeProvider{name: "c"}
	out, _ := pol.Select(context.Background(), llm.Request{Model: "m"}, []llm.Provider{pa, pb, pc})
	assert.Equal(t, []llm.Provider{pb, pc, pa}, out)
}

func TestLatencyWeighted_PrefersMeasuredEMAOverStatic(t *testing.T) {
	// MeasuredProvider 가 실제 5ms latency 를 EMA 로 측정하면 static 200ms 무시.
	stub := &stubLatencyProvider{name: "slow", latency: 5 * time.Millisecond}
	mp := llm.NewMeasuredFactory(nil, "test").Wrap(stub)

	// 한 번 호출하여 EMA 값 채움.
	_, _ = mp.Generate(context.Background(), llm.Request{})
	assert.Greater(t, mp.Stats().LatencyMs(), 0.0)

	// fast 는 static 200ms (높은 값) 인데 EMA 미측정 → static 사용 → 200 vs measured EMA(~5)
	caps2 := llm.NewStaticCapabilitiesProviderFrom(map[string]map[string]llm.Capabilities{
		"slow": {"m": {AvgLatencyMs: 100}},
		"fast": {"m": {AvgLatencyMs: 200}},
	})
	pol2 := policy.NewLatencyWeighted(caps2)
	fast := &fakeProvider{name: "fast"}
	out, _ := pol2.Select(context.Background(), llm.Request{Model: "m"}, []llm.Provider{fast, mp})
	assert.Equal(t, mp, out[0], "measured EMA(~5ms) 가 static 200ms 보다 빠르므로 mp 가 첫 번째")
}

func TestHybrid_BalancesCostAndLatency(t *testing.T) {
	caps := llm.NewStaticCapabilitiesProviderFrom(map[string]map[string]llm.Capabilities{
		"cheap_slow":  {"m": {CostInputPer1M: 0.1, AvgLatencyMs: 5000}},
		"mid":         {"m": {CostInputPer1M: 1.0, AvgLatencyMs: 1000}},
		"fast_pricey": {"m": {CostInputPer1M: 10.0, AvgLatencyMs: 200}},
	})
	// 균형 가중치 — mid 가 양 시그널의 normalized score 가 가장 낮음 (가운데)
	pol := policy.NewHybrid(caps, policy.HybridWeights{Cost: 1.0, Latency: 1.0})

	pa := &fakeProvider{name: "cheap_slow"}
	pb := &fakeProvider{name: "mid"}
	pc := &fakeProvider{name: "fast_pricey"}
	out, _ := pol.Select(context.Background(), llm.Request{Model: "m"}, []llm.Provider{pa, pb, pc})
	assert.Equal(t, pb, out[0], "비용·latency 균형 가중치에서 mid 가 가장 낮은 score")
}

func TestHybrid_AllZeroWeightsPreservesOrder(t *testing.T) {
	pol := policy.NewHybrid(nil, policy.HybridWeights{})
	pa, pb, pc := &fakeProvider{name: "a"}, &fakeProvider{name: "b"}, &fakeProvider{name: "c"}
	in := []llm.Provider{pc, pa, pb}
	out, _ := pol.Select(context.Background(), llm.Request{}, in)
	assert.Equal(t, in, out)
}

// stochastic + 주입 rng 가 다중 goroutine 에서 race 없이 동작하는지 검증 — `go test -race` 필요 (PR #167 CodeRabbit 피드백).
func TestLatencyWeighted_StochasticConcurrentSafe(t *testing.T) {
	caps := llm.NewStaticCapabilitiesProviderFrom(map[string]map[string]llm.Capabilities{
		"a": {"m": {AvgLatencyMs: 100}},
		"b": {"m": {AvgLatencyMs: 200}},
		"c": {"m": {AvgLatencyMs: 300}},
	})
	rng := rand.New(rand.NewPCG(7, 7))
	pol := policy.NewLatencyWeighted(caps, policy.WithStochastic(true), policy.WithRand(rng))

	pa, pb, pc := &fakeProvider{name: "a"}, &fakeProvider{name: "b"}, &fakeProvider{name: "c"}
	candidates := []llm.Provider{pa, pb, pc}

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				out, err := pol.Select(context.Background(), llm.Request{Model: "m"}, candidates)
				assert.NoError(t, err)
				assert.Len(t, out, 3)
			}
		}()
	}
	wg.Wait()
}

func TestLatencyWeighted_StochasticDeterministicWithSeededRand(t *testing.T) {
	caps := llm.NewStaticCapabilitiesProviderFrom(map[string]map[string]llm.Capabilities{
		"a": {"m": {AvgLatencyMs: 1000}},
		"b": {"m": {AvgLatencyMs: 200}},
	})
	rng := rand.New(rand.NewPCG(42, 42))
	pol := policy.NewLatencyWeighted(caps, policy.WithStochastic(true), policy.WithRand(rng))

	pa, pb := &fakeProvider{name: "a"}, &fakeProvider{name: "b"}
	// stochastic 라도 동일 seed 로 두 번 호출 시 동일 순서 (regression 방지).
	first, _ := pol.Select(context.Background(), llm.Request{Model: "m"}, []llm.Provider{pa, pb})
	rng2 := rand.New(rand.NewPCG(42, 42))
	pol2 := policy.NewLatencyWeighted(caps, policy.WithStochastic(true), policy.WithRand(rng2))
	second, _ := pol2.Select(context.Background(), llm.Request{Model: "m"}, []llm.Provider{pa, pb})
	assert.Equal(t, first, second)
}

func TestFixedOrder_PinsToSingleProvider(t *testing.T) {
	pol := policy.NewFixedOrder("gemini")
	pa := &fakeProvider{name: "openai"}
	pb := &fakeProvider{name: "gemini"}
	pc := &fakeProvider{name: "anthropic"}
	out, err := pol.Select(context.Background(), llm.Request{}, []llm.Provider{pa, pb, pc})
	assert.NoError(t, err)
	assert.Equal(t, []llm.Provider{pb}, out, "gemini 만 결과에 포함되어야 함")
}

func TestFixedOrder_RespectsExplicitOrder(t *testing.T) {
	pol := policy.NewFixedOrder("gemini", "openai")
	pa := &fakeProvider{name: "openai"}
	pb := &fakeProvider{name: "gemini"}
	out, _ := pol.Select(context.Background(), llm.Request{}, []llm.Provider{pa, pb})
	assert.Equal(t, []llm.Provider{pb, pa}, out, "names 순서 (gemini→openai) 가 입력 순서 (openai→gemini) 보다 우선")
}

func TestFixedOrder_FiltersUnmatched(t *testing.T) {
	pol := policy.NewFixedOrder("gemini")
	pa := &fakeProvider{name: "openai"}
	out, _ := pol.Select(context.Background(), llm.Request{}, []llm.Provider{pa})
	assert.Empty(t, out, "매칭되는 provider 없으면 빈 슬라이스")
}

func TestFixedOrder_EmptyNamesPreservesInput(t *testing.T) {
	pol := policy.NewFixedOrder()
	pa, pb := &fakeProvider{name: "a"}, &fakeProvider{name: "b"}
	in := []llm.Provider{pb, pa}
	out, _ := pol.Select(context.Background(), llm.Request{}, in)
	assert.Equal(t, in, out, "names 비어있으면 입력 순서 그대로 반환")
}

// stub for compile-time check
var _ error = errors.New("compile check")
