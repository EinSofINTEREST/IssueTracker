// Package policy implements LLM routing policies that order Providers per-request.
//
// Package policy 는 매 호출마다 비용 / 성능 / 작업 특성을 입력으로 적절한 provider 를 동적으로
// 선택하는 routing policy 들을 제공합니다 (이슈 #144).
//
// Policy 는 후보 provider 슬라이스를 입력받아 우선순위 순서로 정렬해 반환합니다 — 단일 선택이 아닌
// 정렬된 슬라이스를 반환하여 chain 합성 (chain.NewWithPolicy) 과 자연스럽게 어울립니다.
//
// 사용 예시:
//
//	caps := llm.NewStaticCapabilitiesProvider()
//	pol := policy.NewCheapestFirst(caps)
//	ordered, _ := pol.Select(ctx, req, []llm.Provider{p1, p2, p3})
//	for _, p := range ordered {
//	    if resp, err := p.Generate(ctx, req); err == nil {
//	        return resp
//	    }
//	}
package policy

import (
	"context"

	"issuetracker/pkg/llm"
)

// Policy orders the given candidates by suitability for the request.
//
// Policy 는 request 에 가장 적합한 순서로 candidates 를 정렬해 반환합니다.
// 반환된 슬라이스는 candidates 의 부분집합일 수 있습니다 (예: filter 적용).
// 빈 슬라이스를 반환해도 panic 없이 caller 가 처리해야 합니다.
//
// 구현체는 goroutine-safe 해야 합니다 (단일 인스턴스를 여러 worker 가 공유).
type Policy interface {
	Select(ctx context.Context, req llm.Request, candidates []llm.Provider) ([]llm.Provider, error)
}

// CapabilityResolver returns a provider's capabilities for the given request.
//
// 호출자 (정책 구현) 가 candidate provider 의 model 을 어떻게 결정할지 의존성이 분리되어 있어 — 보통:
//   - req.Model 이 비어있지 않으면 그대로 사용
//   - 비어있으면 provider 의 default model — 현재 Provider 인터페이스에 노출 X 라
//     향후 provider 가 자기 default 를 노출하는 메소드를 추가할 때 본 헬퍼가 그 메소드를 호출
//
// 본 PR scope: req.Model 만 사용. req.Model 이 비어있고 caps lookup 실패하면 zero Capabilities 사용.
func capabilityFor(caps llm.CapabilitiesProvider, p llm.Provider, req llm.Request) llm.Capabilities {
	if caps == nil {
		return llm.Capabilities{}
	}
	c, ok := caps.Get(p.Name(), req.Model)
	if !ok {
		return llm.Capabilities{}
	}
	return c
}
