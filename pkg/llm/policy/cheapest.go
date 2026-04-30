package policy

import (
	"context"
	"sort"

	"issuetracker/pkg/llm"
)

// CheapestFirst orders candidates by ascending input token cost.
//
// CheapestFirst 는 입력 토큰 단가 (Capabilities.CostInputPer1M) 가 낮은 순으로 후보를 정렬합니다.
// 출력 단가도 보조 키로 사용 — 입력 단가 동률 시 출력 단가가 낮은 쪽 우선.
//
// Capabilities lookup 실패 (cost == 0) 인 후보는 zero cost 로 간주되어 가장 앞으로 옵니다 —
// "free" 또는 "unknown" 둘 다 일단 시도가 합리적이라는 정책. 운영자가 필요 시 명시 unknown
// 페널티는 별도 정책으로 구현 가능.
type CheapestFirst struct {
	caps llm.CapabilitiesProvider
}

// NewCheapestFirst returns a CheapestFirst policy backed by the given capabilities provider.
// caps 가 nil 이면 모든 후보가 zero cost 로 평가되어 입력 순서가 보존됩니다.
func NewCheapestFirst(caps llm.CapabilitiesProvider) *CheapestFirst {
	return &CheapestFirst{caps: caps}
}

// Select orders candidates by (CostInputPer1M, CostOutputPer1M) ascending.
func (p *CheapestFirst) Select(_ context.Context, req llm.Request, candidates []llm.Provider) ([]llm.Provider, error) {
	out := make([]llm.Provider, len(candidates))
	copy(out, candidates)

	sort.SliceStable(out, func(i, j int) bool {
		ci := capabilityFor(p.caps, out[i], req)
		cj := capabilityFor(p.caps, out[j], req)
		if ci.CostInputPer1M != cj.CostInputPer1M {
			return ci.CostInputPer1M < cj.CostInputPer1M
		}
		return ci.CostOutputPer1M < cj.CostOutputPer1M
	})
	return out, nil
}
