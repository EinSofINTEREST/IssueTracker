package policy

import (
	"context"
	"math/rand/v2"
	"sort"
	"sync"

	"issuetracker/pkg/llm"
)

// LatencyWeighted orders candidates by ascending observed latency (lower is better).
//
// LatencyWeighted 는 동적 EMA latency 가 낮은 후보를 우선합니다 (이슈 #144 Phase 2.B).
//
// Latency source 우선순위:
//  1. MeasuredProvider.Stats().LatencyMs() (실측 EMA) — 호출 이력이 있으면 본 값을 사용
//  2. Capabilities.AvgLatencyMs (정적 baseline) — 실측이 없으면 fallback
//  3. 0 (unknown) — 둘 다 없으면 0 으로 평가 (가장 빠름으로 간주)
//
// **확률 가중 무작위 선택 (옵션)**: WithStochastic(true) 시 latency 의 inverse 를 가중치로
// 무작위 선택 — 운영 중 한 provider 가 latency 가 점진 악화돼도 다른 후보가 가끔 시도될 기회를
// 보장 (단순 정렬은 lock-in 가능). default 는 deterministic 정렬.
type LatencyWeighted struct {
	caps       llm.CapabilitiesProvider
	stochastic bool

	// rng 는 *math/rand/v2.Rand 가 concurrent-unsafe 하므로 rngMu 로 보호 — Policy 인터페이스
	// contract (goroutine-safe) 준수. nil 이면 thread-safe global rand 사용 (lock 불필요).
	rngMu sync.Mutex
	rng   *rand.Rand
}

// LatencyWeightedOption 은 LatencyWeighted 생성 옵션입니다.
type LatencyWeightedOption func(*LatencyWeighted)

// WithStochastic 은 latency 의 inverse 가중치로 확률 무작위 선택을 활성화합니다.
// default false (단순 ascending 정렬).
func WithStochastic(enabled bool) LatencyWeightedOption {
	return func(p *LatencyWeighted) { p.stochastic = enabled }
}

// WithRand 는 결정적 테스트용 *rand.Rand 를 주입합니다. 미주입 시 global rand.
func WithRand(rng *rand.Rand) LatencyWeightedOption {
	return func(p *LatencyWeighted) { p.rng = rng }
}

// NewLatencyWeighted returns a LatencyWeighted policy.
func NewLatencyWeighted(caps llm.CapabilitiesProvider, opts ...LatencyWeightedOption) *LatencyWeighted {
	p := &LatencyWeighted{caps: caps}
	for _, o := range opts {
		o(p)
	}
	return p
}

// Select orders candidates by latency (ascending) or stochastic inverse-weighted random.
func (p *LatencyWeighted) Select(_ context.Context, req llm.Request, candidates []llm.Provider) ([]llm.Provider, error) {
	scored := make([]scoredProvider, len(candidates))
	for i, c := range candidates {
		scored[i] = scoredProvider{
			provider: c,
			score:    p.latencyMs(c, req),
		}
	}

	if !p.stochastic {
		sort.SliceStable(scored, func(i, j int) bool {
			return scored[i].score < scored[j].score
		})
	} else {
		p.shuffleByInverseWeight(scored)
	}

	out := make([]llm.Provider, len(scored))
	for i, s := range scored {
		out[i] = s.provider
	}
	return out, nil
}

// latencyMs returns observed EMA if MeasuredProvider has any calls, otherwise Capabilities baseline.
//
// Calls() > 0 으로 측정 여부 판정 — LatencyMs()==0 도 valid 측정값일 수 있음 (sub-ms 호출).
func (p *LatencyWeighted) latencyMs(provider llm.Provider, req llm.Request) float64 {
	if mp, ok := provider.(*llm.MeasuredProvider); ok {
		stats := mp.Stats()
		if stats.Calls() > 0 {
			return stats.LatencyMs()
		}
	}
	caps := capabilityFor(p.caps, provider, req)
	return float64(caps.AvgLatencyMs)
}

// shuffleByInverseWeight orders scored in-place by sampling without replacement using
// weights proportional to 1 / (latency + epsilon). 낮은 latency = 높은 가중치 = 자주 선택.
//
// 모든 latency 가 0 이면 균등 분포 (무작위 순서).
func (p *LatencyWeighted) shuffleByInverseWeight(scored []scoredProvider) {
	const epsilon = 1.0
	weights := make([]float64, len(scored))
	for i, s := range scored {
		weights[i] = 1.0 / (s.score + epsilon)
	}

	// 주입된 *rand.Rand 는 concurrent-unsafe — Float64() 호출을 rngMu 로 보호.
	// nil 인 경우 global rand.Float64() 는 thread-safe 하므로 lock 불필요.
	float64Fn := func() float64 {
		if p.rng == nil {
			return rand.Float64()
		}
		p.rngMu.Lock()
		defer p.rngMu.Unlock()
		return p.rng.Float64()
	}
	pickIdx := func(remainingWeights []float64) int {
		var total float64
		for _, w := range remainingWeights {
			total += w
		}
		r := float64Fn() * total
		var acc float64
		for i, w := range remainingWeights {
			acc += w
			if r <= acc {
				return i
			}
		}
		return len(remainingWeights) - 1
	}

	for i := 0; i < len(scored)-1; i++ {
		remaining := weights[i:]
		pick := i + pickIdx(remaining)
		scored[i], scored[pick] = scored[pick], scored[i]
		weights[i], weights[pick] = weights[pick], weights[i]
	}
}

// scoredProvider is an internal helper pairing a provider with its computed score.
type scoredProvider struct {
	provider llm.Provider
	score    float64
}
