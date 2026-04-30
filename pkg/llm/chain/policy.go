package chain

import (
	"context"

	"issuetracker/pkg/llm"
	"issuetracker/pkg/llm/policy"
	"issuetracker/pkg/logger"
)

// PolicyProvider composes a Policy with a chain — order is decided per-request by Policy.Select.
//
// PolicyProvider 는 매 호출마다 policy.Select 가 결정한 순서로 chain 동작을 수행합니다 (이슈 #144 Phase 3).
// 정적 chain.Provider 와 달리 호출별 dynamic ordering 이 가능하여 비용 / latency / 작업 특성에 따라
// 다른 provider 가 우선 시도됩니다.
//
// 호출자 코드는 chain.Provider 와 동일 인터페이스 (llm.Provider) — 투명한 wrapper.
type PolicyProvider struct {
	policy     policy.Policy
	candidates []llm.Provider
	log        *logger.Logger
}

// PolicyOption 은 PolicyProvider 생성 옵션입니다.
type PolicyOption func(*PolicyProvider)

// WithPolicyLogger 는 위임 / 종결 / 고갈 시 로그를 출력할 logger 를 주입합니다 (chain.WithLogger 와 동일).
func WithPolicyLogger(log *logger.Logger) PolicyOption {
	return func(p *PolicyProvider) { p.log = log }
}

// NewWithPolicy 는 policy 가 매 호출마다 결정한 순서로 candidates 를 시도하는 PolicyProvider 를 반환합니다.
//
// candidates 가 비어있어도 panic 없이 생성됩니다 — 호출 시 ErrCodeBadRequest.
// policy 가 nil 이면 호출 시 ErrCodeBadRequest.
func NewWithPolicy(p policy.Policy, candidates []llm.Provider, opts ...PolicyOption) *PolicyProvider {
	pp := &PolicyProvider{policy: p, candidates: candidates}
	for _, o := range opts {
		o(pp)
	}
	return pp
}

// Name returns the chain identifier (llm.Provider 구현).
func (p *PolicyProvider) Name() string { return providerName }

// Generate 는 policy 가 정렬한 순서로 candidates 를 시도합니다 (llm.Provider 구현).
//
// 흐름:
//  1. policy / candidates 비어있음 / nil → ErrCodeBadRequest
//  2. ctx 이미 cancel → ErrCodeNetwork
//  3. policy.Select 호출하여 순서 결정 (실패 시 ErrCodeUnknown)
//  4. 정렬된 순서대로 chain 동작 (chain.Provider.Generate 와 동일 위임 정책)
func (p *PolicyProvider) Generate(ctx context.Context, req llm.Request) (*llm.Response, error) {
	if p.policy == nil {
		return nil, &llm.Error{
			Code:     llm.ErrCodeBadRequest,
			Provider: providerName,
			Message:  "policy chain has no policy",
		}
	}
	if len(p.candidates) == 0 {
		return nil, &llm.Error{
			Code:     llm.ErrCodeBadRequest,
			Provider: providerName,
			Message:  "policy chain has no candidates",
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, &llm.Error{
			Code:     llm.ErrCodeNetwork,
			Provider: providerName,
			Message:  "context already canceled before policy select",
			Err:      err,
		}
	}

	ordered, err := p.policy.Select(ctx, req, p.candidates)
	if err != nil {
		return nil, &llm.Error{
			Code:     llm.ErrCodeUnknown,
			Provider: providerName,
			Message:  "policy.Select failed",
			Err:      err,
		}
	}
	if len(ordered) == 0 {
		return nil, &llm.Error{
			Code:     llm.ErrCodeBadRequest,
			Provider: providerName,
			Message:  "policy returned empty candidate list",
		}
	}

	// chain.Provider.Generate 와 동일 위임 / cancel 정책. 코드 중복을 줄이기 위해 inner Provider 재사용.
	delegate := &Provider{handlers: ordered, log: p.log}
	return delegate.Generate(ctx, req)
}

// Compile-time check — PolicyProvider 가 llm.Provider 인터페이스를 만족하는지 확인.
var _ llm.Provider = (*PolicyProvider)(nil)
