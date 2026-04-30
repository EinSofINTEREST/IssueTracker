package policy

import (
	"context"

	"issuetracker/pkg/llm"
)

// FixedOrder pins routing to an explicit list of provider names, ignoring others.
//
// FixedOrder 는 호출자가 지정한 provider 이름 슬라이스를 그대로 우선순위로 사용합니다 (이슈 #144).
// candidates 중 names 에 매칭되는 provider 만 지정 순서로 반환하며, 매칭되지 않는 candidate 는
// 결과에서 제외됩니다 — "이 provider 만 쓴다" 는 명시적 정책 (단일 provider 운영, 무료 한도 내
// 제한, A/B 비교용 강제 핀 등에 사용).
//
// 사용 예시 — Gemini 만 사용 (예: 무료 한도 내 운영):
//
//	pol := policy.NewFixedOrder("gemini")
//	p   := chain.NewWithPolicy(pol, []llm.Provider{geminiProvider, openaiProvider})
//	// openaiProvider 는 무시됨 — gemini 만 시도
//
// 사용 예시 — 우선순위 순서 명시 (gemini 우선, 실패 시 openai fallback):
//
//	pol := policy.NewFixedOrder("gemini", "openai")
//
// 동작:
//   - names 가 비어있으면 candidates 를 입력 순서 그대로 반환 (no-op).
//   - 매칭되는 provider 가 하나도 없으면 빈 슬라이스 반환 → chain 이 ErrCodeBadRequest 처리.
//   - 동일 이름의 candidate 가 둘 이상이면 마지막 것이 사용됨 (map overwrite).
type FixedOrder struct {
	names []string
}

// NewFixedOrder returns a policy that selects providers by exact Name() match in the given order.
func NewFixedOrder(names ...string) *FixedOrder {
	return &FixedOrder{names: names}
}

// Select returns providers whose Name() matches names, ordered by names sequence.
func (p *FixedOrder) Select(_ context.Context, _ llm.Request, candidates []llm.Provider) ([]llm.Provider, error) {
	if len(p.names) == 0 {
		out := make([]llm.Provider, len(candidates))
		copy(out, candidates)
		return out, nil
	}

	byName := make(map[string]llm.Provider, len(candidates))
	for _, c := range candidates {
		byName[c.Name()] = c
	}

	out := make([]llm.Provider, 0, len(p.names))
	for _, name := range p.names {
		if c, ok := byName[name]; ok {
			out = append(out, c)
		}
	}
	return out, nil
}
