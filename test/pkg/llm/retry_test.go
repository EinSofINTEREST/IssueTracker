package llm_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/pkg/llm"
)

// programmableProvider 는 test 가 호출 결과를 미리 정의할 수 있는 stub.
type programmableProvider struct {
	name    string
	results []error // 각 호출의 반환 에러 (nil 이면 성공)
	calls   int32
}

func (p *programmableProvider) Name() string { return p.name }

func (p *programmableProvider) Generate(ctx context.Context, _ llm.Request) (*llm.Response, error) {
	idx := atomic.AddInt32(&p.calls, 1) - 1
	if int(idx) >= len(p.results) {
		// 더 이상 정의된 결과 없음 — 마지막 결과 반복.
		idx = int32(len(p.results)) - 1
	}
	if idx < 0 {
		return &llm.Response{Content: "ok"}, nil
	}
	if err := p.results[idx]; err != nil {
		return nil, err
	}
	return &llm.Response{Content: "ok", Model: p.name}, nil
}

// fastPolicy 는 테스트 latency 를 최소화하기 위한 1ms 정책.
func fastPolicy(maxAttempts int) llm.RetryPolicy {
	return llm.RetryPolicy{MaxAttempts: maxAttempts, InitialDelay: time.Millisecond, Multiplier: 1.0}
}

func TestRetryProvider_SuccessFirstAttemptNoRetry(t *testing.T) {
	t.Parallel()
	stub := &programmableProvider{name: "p", results: []error{nil}}
	rp := llm.NewRetryProvider(stub, llm.RetryProviderOptions{
		RateLimitPolicy: fastPolicy(5),
		NetworkPolicy:   fastPolicy(3),
	})
	resp, err := rp.Generate(context.Background(), llm.Request{})
	require.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Equal(t, int32(1), atomic.LoadInt32(&stub.calls))
}

func TestRetryProvider_RateLimitRetriesThenSucceeds(t *testing.T) {
	t.Parallel()
	rateLimitErr := &llm.Error{Code: llm.ErrCodeRateLimit, Provider: "p", Retryable: true}
	stub := &programmableProvider{name: "p", results: []error{rateLimitErr, rateLimitErr, nil}}
	rp := llm.NewRetryProvider(stub, llm.RetryProviderOptions{
		RateLimitPolicy: fastPolicy(5),
		NetworkPolicy:   fastPolicy(3),
	})
	resp, err := rp.Generate(context.Background(), llm.Request{})
	require.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Equal(t, int32(3), atomic.LoadInt32(&stub.calls), "2회 rate_limit 후 3번째에 성공")
}

func TestRetryProvider_RateLimitExhaustedReturnsError(t *testing.T) {
	t.Parallel()
	rateLimitErr := &llm.Error{Code: llm.ErrCodeRateLimit, Provider: "p", Retryable: true}
	stub := &programmableProvider{name: "p", results: []error{rateLimitErr}}
	rp := llm.NewRetryProvider(stub, llm.RetryProviderOptions{
		RateLimitPolicy: fastPolicy(3),
		NetworkPolicy:   fastPolicy(3),
	})
	_, err := rp.Generate(context.Background(), llm.Request{})
	require.Error(t, err)
	var llmErr *llm.Error
	require.True(t, errors.As(err, &llmErr))
	assert.Equal(t, llm.ErrCodeRateLimit, llmErr.Code)
	assert.Equal(t, int32(3), atomic.LoadInt32(&stub.calls), "MaxAttempts(3) 회 모두 호출")
}

func TestRetryProvider_NetworkErrorRetriesIndependentOfRateLimit(t *testing.T) {
	t.Parallel()
	networkErr := &llm.Error{Code: llm.ErrCodeNetwork, Provider: "p", Retryable: true}
	stub := &programmableProvider{name: "p", results: []error{networkErr, nil}}
	rp := llm.NewRetryProvider(stub, llm.RetryProviderOptions{
		RateLimitPolicy: fastPolicy(5),
		NetworkPolicy:   fastPolicy(3),
	})
	resp, err := rp.Generate(context.Background(), llm.Request{})
	require.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Equal(t, int32(2), atomic.LoadInt32(&stub.calls))
}

func TestRetryProvider_NonRetryableImmediateReturn(t *testing.T) {
	t.Parallel()
	authErr := &llm.Error{Code: llm.ErrCodeAuth, Provider: "p", Retryable: false}
	stub := &programmableProvider{name: "p", results: []error{authErr}}
	rp := llm.NewRetryProvider(stub, llm.RetryProviderOptions{
		RateLimitPolicy: fastPolicy(5),
		NetworkPolicy:   fastPolicy(3),
	})
	_, err := rp.Generate(context.Background(), llm.Request{})
	require.Error(t, err)
	assert.Equal(t, int32(1), atomic.LoadInt32(&stub.calls), "non-retryable 은 즉시 반환")
}

func TestRetryProvider_PolicyAttemptsTrackedSeparately(t *testing.T) {
	t.Parallel()
	rateLimitErr := &llm.Error{Code: llm.ErrCodeRateLimit, Provider: "p", Retryable: true}
	networkErr := &llm.Error{Code: llm.ErrCodeNetwork, Provider: "p", Retryable: true}
	// rate_limit 2회 + network 2회 + 성공 — 둘 다 한도 (3) 안.
	stub := &programmableProvider{name: "p", results: []error{rateLimitErr, networkErr, rateLimitErr, networkErr, nil}}
	rp := llm.NewRetryProvider(stub, llm.RetryProviderOptions{
		RateLimitPolicy: fastPolicy(3),
		NetworkPolicy:   fastPolicy(3),
	})
	resp, err := rp.Generate(context.Background(), llm.Request{})
	require.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Equal(t, int32(5), atomic.LoadInt32(&stub.calls))
}

func TestRetryProvider_NameDelegatesToInner(t *testing.T) {
	t.Parallel()
	stub := &programmableProvider{name: "inner-name"}
	rp := llm.NewRetryProvider(stub, llm.RetryProviderOptions{
		RateLimitPolicy: fastPolicy(1),
		NetworkPolicy:   fastPolicy(1),
	})
	assert.Equal(t, "inner-name", rp.Name())
}

func TestRetryProvider_ContextCancelInterruptsBackoff(t *testing.T) {
	t.Parallel()
	rateLimitErr := &llm.Error{Code: llm.ErrCodeRateLimit, Provider: "p", Retryable: true}
	stub := &programmableProvider{name: "p", results: []error{rateLimitErr}}
	rp := llm.NewRetryProvider(stub, llm.RetryProviderOptions{
		// 의도적으로 긴 InitialDelay — ctx cancel 이 backoff 도중 끊을 수 있도록.
		RateLimitPolicy: llm.RetryPolicy{MaxAttempts: 5, InitialDelay: 200 * time.Millisecond, Multiplier: 1.0},
		NetworkPolicy:   fastPolicy(3),
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := rp.Generate(ctx, llm.Request{})
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.DeadlineExceeded), "ctx deadline 으로 backoff 중단")
}

func TestRetryProvider_DefaultPolicyAppliedWhenZeroValue(t *testing.T) {
	t.Parallel()
	stub := &programmableProvider{name: "p", results: []error{nil}}
	// MaxAttempts=0 → DefaultRateLimitRetryPolicy / DefaultNetworkRetryPolicy 적용.
	rp := llm.NewRetryProvider(stub, llm.RetryProviderOptions{})
	resp, err := rp.Generate(context.Background(), llm.Request{})
	require.NoError(t, err)
	assert.NotNil(t, resp)
}
