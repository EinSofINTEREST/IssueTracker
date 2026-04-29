package chain_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/pkg/llm"
	"issuetracker/pkg/llm/chain"
)

// fakeProvider 는 미리 정해진 결과를 반환하는 mock provider 입니다.
// 호출 횟수를 atomic counter 로 추적하여 chain 위임 시점을 검증합니다.
type fakeProvider struct {
	name    string
	resp    *llm.Response
	err     error
	callCnt int64
	onCall  func(ctx context.Context, req llm.Request) // optional hook
}

func (f *fakeProvider) Name() string { return f.name }

func (f *fakeProvider) Generate(ctx context.Context, req llm.Request) (*llm.Response, error) {
	atomic.AddInt64(&f.callCnt, 1)
	if f.onCall != nil {
		f.onCall(ctx, req)
	}
	return f.resp, f.err
}

func (f *fakeProvider) calls() int { return int(atomic.LoadInt64(&f.callCnt)) }

func sampleRequest() llm.Request {
	return llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "ping"}},
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 정상 흐름
// ─────────────────────────────────────────────────────────────────────────────

func TestChain_FirstHandlerSucceeds_ShortCircuits(t *testing.T) {
	want := &llm.Response{Content: "ok-1", Model: "m1"}
	h1 := &fakeProvider{name: "h1", resp: want}
	h2 := &fakeProvider{name: "h2", resp: &llm.Response{Content: "ok-2"}}
	h3 := &fakeProvider{name: "h3", resp: &llm.Response{Content: "ok-3"}}

	p := chain.New(h1, h2, h3)
	resp, err := p.Generate(context.Background(), sampleRequest())

	require.NoError(t, err)
	assert.Same(t, want, resp)
	assert.Equal(t, 1, h1.calls())
	assert.Equal(t, 0, h2.calls(), "후속 handler 는 호출되면 안 됨")
	assert.Equal(t, 0, h3.calls())
}

func TestChain_FirstFailsRetryable_FallsBackToSecond(t *testing.T) {
	h1 := &fakeProvider{name: "h1", err: &llm.Error{Code: llm.ErrCodeServer, Provider: "h1", Retryable: true}}
	want := &llm.Response{Content: "from-h2"}
	h2 := &fakeProvider{name: "h2", resp: want}

	p := chain.New(h1, h2)
	resp, err := p.Generate(context.Background(), sampleRequest())

	require.NoError(t, err)
	assert.Same(t, want, resp)
	assert.Equal(t, 1, h1.calls())
	assert.Equal(t, 1, h2.calls())
}

func TestChain_AllHandlersFail_ReturnsLastError(t *testing.T) {
	first := &llm.Error{Code: llm.ErrCodeRateLimit, Provider: "h1", Message: "first failure"}
	last := &llm.Error{Code: llm.ErrCodeServer, Provider: "h2", Message: "last failure"}

	h1 := &fakeProvider{name: "h1", err: first}
	h2 := &fakeProvider{name: "h2", err: last}

	p := chain.New(h1, h2)
	_, err := p.Generate(context.Background(), sampleRequest())

	require.Error(t, err)
	var lerr *llm.Error
	require.ErrorAs(t, err, &lerr)
	assert.Equal(t, "h2", lerr.Provider, "마지막 handler 의 에러가 반환되어야 함")
	assert.Equal(t, llm.ErrCodeServer, lerr.Code)
}

// ─────────────────────────────────────────────────────────────────────────────
// 위임 정책 (shouldDelegate)
// ─────────────────────────────────────────────────────────────────────────────

func TestChain_BadRequest_TerminatesImmediately(t *testing.T) {
	// h1 의 BadRequest 는 입력 자체 문제 — 다른 handler 도 동일 실패. 즉시 종결.
	h1 := &fakeProvider{name: "h1", err: &llm.Error{Code: llm.ErrCodeBadRequest, Provider: "h1"}}
	h2 := &fakeProvider{name: "h2", resp: &llm.Response{Content: "should-not-be-called"}}

	p := chain.New(h1, h2)
	_, err := p.Generate(context.Background(), sampleRequest())

	require.Error(t, err)
	var lerr *llm.Error
	require.ErrorAs(t, err, &lerr)
	assert.Equal(t, llm.ErrCodeBadRequest, lerr.Code)
	assert.Equal(t, 1, h1.calls())
	assert.Equal(t, 0, h2.calls(), "BadRequest 는 위임 무의미 — 후속 handler 호출되면 안 됨")
}

func TestChain_ContextLimit_Delegates(t *testing.T) {
	// ContextLimit 은 provider/모델 별 context window 차이로 위임 가치 있음
	// (예: gpt-4o-mini 128k → claude-opus-4-7 200k → gemini-1.5-pro 2M).
	// 현재 정책: 다음 handler 가 더 큰 window 를 가질 수 있어 시도.
	h1 := &fakeProvider{name: "h1", err: &llm.Error{Code: llm.ErrCodeContextLimit, Provider: "h1"}}
	h2 := &fakeProvider{name: "h2", resp: &llm.Response{Content: "recovered-with-larger-window"}}

	p := chain.New(h1, h2)
	resp, err := p.Generate(context.Background(), sampleRequest())

	require.NoError(t, err)
	assert.Equal(t, "recovered-with-larger-window", resp.Content)
	assert.Equal(t, 1, h2.calls(), "ContextLimit 는 다음 handler 위임 — 더 큰 context window 가능성")
}

func TestChain_DelegateOnAllExceptBadRequest(t *testing.T) {
	delegatable := []llm.ErrorCode{
		llm.ErrCodeAuth,
		llm.ErrCodeRateLimit,
		llm.ErrCodeServer,
		llm.ErrCodeNetwork,
		llm.ErrCodeUnknown,
		llm.ErrCodeContextLimit, // 다른 모델의 더 큰 context window 가능성
	}
	for _, code := range delegatable {
		t.Run(string(code), func(t *testing.T) {
			h1 := &fakeProvider{name: "h1", err: &llm.Error{Code: code, Provider: "h1"}}
			h2 := &fakeProvider{name: "h2", resp: &llm.Response{Content: "recovered"}}

			p := chain.New(h1, h2)
			resp, err := p.Generate(context.Background(), sampleRequest())

			require.NoError(t, err)
			assert.Equal(t, "recovered", resp.Content)
			assert.Equal(t, 1, h2.calls(), "code=%s 는 위임되어야 함", code)
		})
	}
}

func TestChain_NonLLMError_StillDelegates(t *testing.T) {
	// 비-llm.Error (raw error) → 보수적으로 다음 handler 시도
	h1 := &fakeProvider{name: "h1", err: errors.New("raw network blip")}
	h2 := &fakeProvider{name: "h2", resp: &llm.Response{Content: "recovered"}}

	p := chain.New(h1, h2)
	resp, err := p.Generate(context.Background(), sampleRequest())

	require.NoError(t, err)
	assert.Equal(t, "recovered", resp.Content)
}

// ─────────────────────────────────────────────────────────────────────────────
// edge cases
// ─────────────────────────────────────────────────────────────────────────────

func TestChain_EmptyHandlers_ReturnsBadRequest(t *testing.T) {
	p := chain.New()
	_, err := p.Generate(context.Background(), sampleRequest())

	require.Error(t, err)
	var lerr *llm.Error
	require.ErrorAs(t, err, &lerr)
	assert.Equal(t, llm.ErrCodeBadRequest, lerr.Code)
	assert.Equal(t, "chain", lerr.Provider)
}

func TestChain_PrecanceledContext_FailsBeforeAnyHandler(t *testing.T) {
	h1 := &fakeProvider{name: "h1", resp: &llm.Response{Content: "x"}}
	p := chain.New(h1)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 호출 전에 이미 cancel

	_, err := p.Generate(ctx, sampleRequest())
	require.Error(t, err)
	var lerr *llm.Error
	require.ErrorAs(t, err, &lerr)
	assert.Equal(t, llm.ErrCodeNetwork, lerr.Code)
	assert.Equal(t, 0, h1.calls(), "ctx 이미 cancel 이면 첫 handler 도 호출 안 함")
}

func TestChain_ContextCanceledMidway_ReturnsExplicitNetworkError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	// h1 이 호출되는 동안 ctx 를 cancel — 그 후 h2 는 호출되면 안 됨
	h1 := &fakeProvider{
		name: "h1",
		err:  &llm.Error{Code: llm.ErrCodeServer, Provider: "h1", Retryable: true},
		onCall: func(_ context.Context, _ llm.Request) {
			cancel()
		},
	}
	h2 := &fakeProvider{name: "h2", resp: &llm.Response{Content: "should-not-call"}}

	p := chain.New(h1, h2)
	_, err := p.Generate(ctx, sampleRequest())

	require.Error(t, err)
	var lerr *llm.Error
	require.ErrorAs(t, err, &lerr)
	// ctx cancel 감지 시 명시적 chain 의 ErrCodeNetwork — 호출자가 "취소" 즉답 가능
	// (이전 handler 의 server 에러로 가리지 않음, Gemini code review #2)
	assert.Equal(t, "chain", lerr.Provider)
	assert.Equal(t, llm.ErrCodeNetwork, lerr.Code)
	assert.Equal(t, 1, h1.calls())
	assert.Equal(t, 0, h2.calls(), "h1 이후 ctx cancel 감지 → h2 호출 안 함")
}

func TestChain_Name_ReturnsChain(t *testing.T) {
	assert.Equal(t, "chain", chain.New().Name())
}

// ─────────────────────────────────────────────────────────────────────────────
// 합성 가능성 — chain in a chain
// ─────────────────────────────────────────────────────────────────────────────

func TestChain_NestedChain_BehavesAsSingleProvider(t *testing.T) {
	// inner chain: [h1(rate_limit), h2(success)]
	h1 := &fakeProvider{name: "h1", err: &llm.Error{Code: llm.ErrCodeRateLimit, Provider: "h1"}}
	h2 := &fakeProvider{name: "h2", resp: &llm.Response{Content: "from-inner-h2"}}
	inner := chain.New(h1, h2)

	// outer chain: [extraFail, inner]
	extra := &fakeProvider{name: "extra", err: &llm.Error{Code: llm.ErrCodeServer, Provider: "extra"}}
	outer := chain.New(extra, inner)

	resp, err := outer.Generate(context.Background(), sampleRequest())
	require.NoError(t, err)
	assert.Equal(t, "from-inner-h2", resp.Content)
	assert.Equal(t, 1, extra.calls())
	assert.Equal(t, 1, h1.calls())
	assert.Equal(t, 1, h2.calls())
}
