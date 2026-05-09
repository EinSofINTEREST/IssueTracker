package llm

import (
	"context"
	"errors"
	"math/rand"
	"sync"
	"time"
)

// RetryPolicy 는 retryable 에러에 적용되는 재시도 정책입니다.
//
// 코드별 정책 (.claude/rules/04-error-handling.md):
//   - RateLimit  (429)        : max 5회, 10s initial, exp backoff (jitter 없음)
//   - Network/Timeout/Server  : max 3회, 1s initial, exp backoff + jitter
//   - 그 외 retryable          : Network 정책 적용
//
// MaxAttempts=1 이면 retry 비활성 (테스트 / 강제 fail-fast).
type RetryPolicy struct {
	MaxAttempts  int
	InitialDelay time.Duration
	MaxDelay     time.Duration
	Multiplier   float64
	Jitter       bool
}

// DefaultRateLimitRetryPolicy 는 ErrCodeRateLimit (429) 에 적용되는 정책 default 입니다.
//
// initial 10s 는 #215 본문 (initial 5s) 보다 보수적 — Gemini free tier 의 분당 quota
// 회복 주기 (~60s) 를 고려해 첫 backoff 도 그 절반 가까이 둠. 운영자가 RetryProvider
// 구성 시 override 가능.
func DefaultRateLimitRetryPolicy() RetryPolicy {
	return RetryPolicy{
		MaxAttempts:  5,
		InitialDelay: 10 * time.Second,
		MaxDelay:     5 * time.Minute,
		Multiplier:   2.0,
		Jitter:       false,
	}
}

// DefaultNetworkRetryPolicy 는 Network/Timeout/Server 에 적용되는 정책 default 입니다.
func DefaultNetworkRetryPolicy() RetryPolicy {
	return RetryPolicy{
		MaxAttempts:  3,
		InitialDelay: 1 * time.Second,
		MaxDelay:     30 * time.Second,
		Multiplier:   2.0,
		Jitter:       true,
	}
}

// RetryProvider 는 inner Provider 를 wrap 하여 retryable 에러 발생 시 정책에 따라
// 재시도하는 middleware 입니다.
//
// 정책 선택 기준 (Generate 시점에 동적 결정):
//   - *llm.Error.Code == ErrCodeRateLimit → RateLimitPolicy
//   - 그 외 Retryable=true              → NetworkPolicy
//   - non-retryable                      → 즉시 반환 (재시도 무용)
//
// 동시성 안전 — inner provider 자체가 안전하다고 가정 (인터페이스 contract).
// rand 만 단일 mu 로 보호.
type RetryProvider struct {
	inner      Provider
	rateLimitP RetryPolicy
	networkP   RetryPolicy
	rngMu      sync.Mutex
	rng        *rand.Rand
}

// RetryProviderOptions 는 RetryProvider 생성 옵션입니다.
//
// zero value 의 RetryPolicy (MaxAttempts=0) 는 Default*RetryPolicy() 로 fallback.
type RetryProviderOptions struct {
	RateLimitPolicy RetryPolicy
	NetworkPolicy   RetryPolicy
}

// NewRetryProvider 는 inner provider 를 wrap 한 RetryProvider 를 반환합니다.
//
// inner 가 nil 이면 panic — 명시적 wiring 실수 즉시 발견.
func NewRetryProvider(inner Provider, opts RetryProviderOptions) *RetryProvider {
	if inner == nil {
		panic("llm: NewRetryProvider requires non-nil inner provider")
	}
	if opts.RateLimitPolicy.MaxAttempts == 0 {
		opts.RateLimitPolicy = DefaultRateLimitRetryPolicy()
	}
	if opts.NetworkPolicy.MaxAttempts == 0 {
		opts.NetworkPolicy = DefaultNetworkRetryPolicy()
	}
	return &RetryProvider{
		inner:      inner,
		rateLimitP: opts.RateLimitPolicy,
		networkP:   opts.NetworkPolicy,
		rng:        rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// Name 은 inner provider 의 이름을 그대로 노출합니다 (decorator 투명성).
func (r *RetryProvider) Name() string { return r.inner.Name() }

// Generate 는 inner provider 를 호출하고, retryable 에러 발생 시 정책에 따라 재시도합니다.
func (r *RetryProvider) Generate(ctx context.Context, req Request) (*Response, error) {
	// maxOverall: 두 정책의 합 — 동일 호출에서 rate_limit 과 network 가 alternating 으로 발생해도
	// 각 정책의 attempt 한도까지 모두 소진할 수 있도록 union 이 아닌 sum 으로 cap.
	var (
		lastErr         error
		rateAttempts    int
		networkAttempts int
		maxOverall      = r.rateLimitP.MaxAttempts + r.networkP.MaxAttempts
	)

	for loop := 0; loop < maxOverall; loop++ {
		resp, err := r.inner.Generate(ctx, req)
		if err == nil {
			return resp, nil
		}

		var llmErr *Error
		if !errors.As(err, &llmErr) || !llmErr.Retryable {
			return nil, err
		}

		var policy RetryPolicy
		var counter *int
		if llmErr.Code == ErrCodeRateLimit {
			policy = r.rateLimitP
			counter = &rateAttempts
		} else {
			policy = r.networkP
			counter = &networkAttempts
		}
		*counter++
		lastErr = err

		// 해당 정책의 attempt 한도 도달 시 종료.
		if *counter >= policy.MaxAttempts {
			return nil, err
		}

		delay := r.computeBackoff(policy, *counter)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
	}

	if lastErr == nil {
		// 안전망 — 위 루프 어느 분기에서도 lastErr 없이 빠져나오는 경우는 없음.
		return nil, errors.New("llm: retry exhausted without error capture")
	}
	return nil, lastErr
}

// computeBackoff 는 attempt-th 재시도 전 대기 시간을 계산합니다 (1-based attempt).
//
// delay = initial * multiplier^(attempt-1), MaxDelay 로 cap. Jitter=true 면 [0.5×, 1.5×) 변동.
func (r *RetryProvider) computeBackoff(p RetryPolicy, attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := float64(p.InitialDelay)
	for i := 1; i < attempt; i++ {
		delay *= p.Multiplier
	}
	if maxD := float64(p.MaxDelay); maxD > 0 && delay > maxD {
		delay = maxD
	}
	if p.Jitter {
		r.rngMu.Lock()
		factor := 0.5 + r.rng.Float64()
		r.rngMu.Unlock()
		delay *= factor
	}
	return time.Duration(delay)
}

// 컴파일 타임 contract 보증.
var _ Provider = (*RetryProvider)(nil)
