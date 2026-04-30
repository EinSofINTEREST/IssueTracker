package policy

import (
	"context"
	"sort"

	"issuetracker/pkg/llm"
)

// HybridWeights controls the relative importance of cost / latency / failure rate signals.
//
// HybridWeights 는 비용 / latency / 실패율 시그널의 상대 가중치입니다 (이슈 #144 Phase 2.C).
// 합이 1.0 일 필요는 없음 — 각 시그널을 normalize 후 가중 합산하므로 절대값보다 비율이 의미.
//
// 모두 0 이면 입력 순서가 보존됩니다 (panic 없이 graceful no-op).
type HybridWeights struct {
	Cost        float64 // 비용 (CostInputPer1M) 가중치
	Latency     float64 // EMA / Capabilities latency 가중치
	FailureRate float64 // MeasuredProvider failure rate 가중치 (이력 없으면 0)
}

// DefaultHybridWeights returns balanced weights — 운영자가 정책 변경 전 시작점.
// Cost 와 Latency 가 동일 가중, FailureRate 는 그 절반 (long-term 안정성 보조 신호).
func DefaultHybridWeights() HybridWeights {
	return HybridWeights{Cost: 1.0, Latency: 1.0, FailureRate: 0.5}
}

// Hybrid orders candidates by a weighted score combining cost / latency / failure rate.
//
// 각 시그널은 후보 슬라이스 내 max 값으로 normalize 되어 [0, 1] 범위에서 합산됩니다.
// 가중치가 모두 0 이면 입력 순서가 그대로 반환됩니다.
//
// **score 가 낮을수록 우선** — cost / latency / failure rate 모두 낮은 게 좋다는 의미.
type Hybrid struct {
	caps    llm.CapabilitiesProvider
	weights HybridWeights
}

// NewHybrid returns a Hybrid policy with the given weights.
func NewHybrid(caps llm.CapabilitiesProvider, weights HybridWeights) *Hybrid {
	return &Hybrid{caps: caps, weights: weights}
}

// Select orders candidates by ascending hybrid score.
func (p *Hybrid) Select(_ context.Context, req llm.Request, candidates []llm.Provider) ([]llm.Provider, error) {
	if len(candidates) == 0 {
		return nil, nil
	}
	if p.weights.Cost == 0 && p.weights.Latency == 0 && p.weights.FailureRate == 0 {
		out := make([]llm.Provider, len(candidates))
		copy(out, candidates)
		return out, nil
	}

	costs := make([]float64, len(candidates))
	latencies := make([]float64, len(candidates))
	failures := make([]float64, len(candidates))
	for i, c := range candidates {
		caps := capabilityFor(p.caps, c, req)
		costs[i] = caps.CostInputPer1M
		if mp, ok := c.(*llm.MeasuredProvider); ok {
			// Calls() > 0 으로 측정 여부 판정 — LatencyMs()==0 도 valid 측정값일 수 있음
			// (sub-ms 호출이 ms 단위로 round 된 경우).
			stats := mp.Stats()
			if stats.Calls() > 0 {
				latencies[i] = stats.LatencyMs()
			} else {
				latencies[i] = float64(caps.AvgLatencyMs)
			}
			failures[i] = stats.FailureRate()
		} else {
			latencies[i] = float64(caps.AvgLatencyMs)
		}
	}

	costMax := maxNonZero(costs)
	latMax := maxNonZero(latencies)
	failMax := maxNonZero(failures)

	scored := make([]scoredProvider, len(candidates))
	for i, c := range candidates {
		score := 0.0
		score += p.weights.Cost * normalize(costs[i], costMax)
		score += p.weights.Latency * normalize(latencies[i], latMax)
		score += p.weights.FailureRate * normalize(failures[i], failMax)
		scored[i] = scoredProvider{provider: c, score: score}
	}

	sort.SliceStable(scored, func(i, j int) bool {
		return scored[i].score < scored[j].score
	})

	out := make([]llm.Provider, len(scored))
	for i, s := range scored {
		out[i] = s.provider
	}
	return out, nil
}

// normalize divides v by max, returning 0 if max is 0 (avoid div-by-zero).
func normalize(v, max float64) float64 {
	if max == 0 {
		return 0
	}
	return v / max
}

// maxNonZero returns the maximum value in xs, or 0 if all are zero / empty.
func maxNonZero(xs []float64) float64 {
	var m float64
	for _, x := range xs {
		if x > m {
			m = x
		}
	}
	return m
}
