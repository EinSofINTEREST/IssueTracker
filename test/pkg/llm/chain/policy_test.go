package chain_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	"issuetracker/pkg/llm"
	"issuetracker/pkg/llm/chain"
	"issuetracker/pkg/llm/policy"
)

// trackedProvider — 호출 횟수 + 응답 / 에러 제어.
type trackedProvider struct {
	name  string
	resp  *llm.Response
	err   error
	calls int
}

func (t *trackedProvider) Name() string { return t.name }
func (t *trackedProvider) Generate(_ context.Context, _ llm.Request) (*llm.Response, error) {
	t.calls++
	return t.resp, t.err
}

// fixedPolicy — Select 결과를 고정 — 테스트 결정성 위해.
type fixedPolicy struct{ order []llm.Provider }

func (p *fixedPolicy) Select(_ context.Context, _ llm.Request, _ []llm.Provider) ([]llm.Provider, error) {
	return p.order, nil
}

func TestPolicyProvider_UsesPolicyOrdering(t *testing.T) {
	a := &trackedProvider{name: "a", err: &llm.Error{Code: llm.ErrCodeServer, Message: "fail a"}}
	b := &trackedProvider{name: "b", resp: &llm.Response{Content: "ok b"}}
	c := &trackedProvider{name: "c", resp: &llm.Response{Content: "ok c"}}

	// policy 가 [b, a, c] 순으로 정렬한다고 가정 — b 가 첫 번째라 즉시 성공, a/c 는 호출 X.
	pol := &fixedPolicy{order: []llm.Provider{b, a, c}}
	pp := chain.NewWithPolicy(pol, []llm.Provider{a, b, c})

	resp, err := pp.Generate(context.Background(), llm.Request{})
	assert.NoError(t, err)
	assert.Equal(t, "ok b", resp.Content)
	assert.Equal(t, 1, b.calls)
	assert.Equal(t, 0, a.calls)
	assert.Equal(t, 0, c.calls)
}

func TestPolicyProvider_FallsBackOnDelegatableError(t *testing.T) {
	a := &trackedProvider{name: "a", err: &llm.Error{Code: llm.ErrCodeServer, Message: "fail a"}}
	b := &trackedProvider{name: "b", resp: &llm.Response{Content: "ok b"}}

	// policy 가 [a, b] 순. a 실패 (delegatable) → b 시도.
	pol := &fixedPolicy{order: []llm.Provider{a, b}}
	pp := chain.NewWithPolicy(pol, []llm.Provider{a, b})

	resp, err := pp.Generate(context.Background(), llm.Request{})
	assert.NoError(t, err)
	assert.Equal(t, "ok b", resp.Content)
	assert.Equal(t, 1, a.calls)
	assert.Equal(t, 1, b.calls)
}

func TestPolicyProvider_NoCandidates(t *testing.T) {
	pol := &fixedPolicy{order: nil}
	pp := chain.NewWithPolicy(pol, nil)

	_, err := pp.Generate(context.Background(), llm.Request{})
	var lerr *llm.Error
	assert.ErrorAs(t, err, &lerr)
	assert.Equal(t, llm.ErrCodeBadRequest, lerr.Code)
}

func TestPolicyProvider_NilPolicy(t *testing.T) {
	pp := chain.NewWithPolicy(nil, []llm.Provider{&trackedProvider{name: "a"}})
	_, err := pp.Generate(context.Background(), llm.Request{})
	var lerr *llm.Error
	assert.ErrorAs(t, err, &lerr)
	assert.Equal(t, llm.ErrCodeBadRequest, lerr.Code)
}

func TestPolicyProvider_ContextCanceled(t *testing.T) {
	pol := policy.NewCheapestFirst(nil)
	pp := chain.NewWithPolicy(pol, []llm.Provider{&trackedProvider{name: "a"}})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := pp.Generate(ctx, llm.Request{})
	var lerr *llm.Error
	assert.ErrorAs(t, err, &lerr)
	assert.Equal(t, llm.ErrCodeNetwork, lerr.Code)
}

// compile-time stub
var _ = errors.New("compile check")
