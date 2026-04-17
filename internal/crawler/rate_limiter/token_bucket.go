// Package rate_limiter는 IP 기반 rate limiting 구현체를 제공합니다.
// core.RateLimiter 인터페이스의 구현체와 IP별 레지스트리를 포함합니다.
//
// Package rate_limiter provides IP-based rate limiting implementations.
package rate_limiter

import (
	"context"
	"fmt"
	"sync"
	"time"

	"issuetracker/internal/crawler/core"
	"issuetracker/pkg/logger"
)

// TokenBucketRateLimiter는 token bucket 알고리즘을 사용한 rate limiter입니다.
// 시간당 요청 수를 제한하며, burst를 허용합니다.
type TokenBucketRateLimiter struct {
	rate       float64 // tokens per second
	burst      int     // maximum tokens
	tokens     float64 // current tokens
	lastRefill time.Time
	mu         sync.Mutex
}

// NewRateLimiter는 새로운 rate limiter를 생성합니다.
// requestsPerHour: 시간당 허용 요청 수 (0 이하면 제한 없음)
// burst: 한번에 허용되는 최대 요청 수 (최소 1)
func NewRateLimiter(requestsPerHour, burst int) core.RateLimiter {
	// 0 이하 값 방어: divide by zero 및 무한 대기 방지
	if requestsPerHour <= 0 {
		return &noopRateLimiter{}
	}
	if burst < 1 {
		burst = 1
	}

	rate := float64(requestsPerHour) / 3600.0 // convert to per second

	return &TokenBucketRateLimiter{
		rate:       rate,
		burst:      burst,
		tokens:     float64(burst),
		lastRefill: time.Now(),
	}
}

// noopRateLimiter는 제한 없이 모든 요청을 허용하는 rate limiter입니다.
// RequestsPerHour가 0 이하일 때 사용됩니다.
type noopRateLimiter struct{}

func (noopRateLimiter) Wait(_ context.Context) error { return nil }
func (noopRateLimiter) Allow() bool                  { return true }

// Wait는 rate limit에 따라 대기합니다.
// token이 없으면 token이 생성될 때까지 대기합니다.
func (r *TokenBucketRateLimiter) Wait(ctx context.Context) error {
	log := logger.FromContext(ctx)
	waitCount := 0

	for {
		if r.Allow() {
			if waitCount > 0 {
				log.WithField("wait_count", waitCount).Debug("rate limit wait completed")
			}
			return nil
		}

		sleepDuration := r.timeToNextToken()
		waitCount++

		if waitCount == 1 {
			log.WithFields(map[string]interface{}{
				"wait_ms": sleepDuration.Milliseconds(),
				"rate":    r.rate,
				"burst":   r.burst,
			}).Debug("rate limit reached, waiting for token")
		}

		select {
		case <-ctx.Done():
			ctxErr := ctx.Err()
			log.WithFields(map[string]interface{}{
				"wait_count": waitCount,
				"rate":       r.rate,
				"burst":      r.burst,
				"ctx_err":    ctxErr,
			}).Debug("rate limit wait context done")
			return ctxErr
		case <-time.After(sleepDuration):
			// continue to check again
		}
	}
}

// Allow는 현재 요청이 허용되는지 확인합니다.
// token이 있으면 true를 반환하고 token을 소비합니다.
func (r *TokenBucketRateLimiter) Allow() bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.refill()

	if r.tokens >= 1.0 {
		r.tokens--
		return true
	}

	return false
}

// refill은 경과 시간에 따라 token을 채웁니다.
// 호출 전에 lock을 획득해야 합니다.
func (r *TokenBucketRateLimiter) refill() {
	now := time.Now()
	elapsed := now.Sub(r.lastRefill).Seconds()

	// 경과 시간에 비례하여 token 추가
	newTokens := elapsed * r.rate
	r.tokens = minFloat(r.tokens+newTokens, float64(r.burst))
	r.lastRefill = now
}

// timeToNextToken은 다음 token이 생성될 때까지 시간을 반환합니다.
func (r *TokenBucketRateLimiter) timeToNextToken() time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.tokens >= 1.0 {
		return 0
	}

	// 1개의 token이 생성될 때까지 시간 계산
	tokensNeeded := 1.0 - r.tokens
	secondsNeeded := tokensNeeded / r.rate

	return time.Duration(secondsNeeded * float64(time.Second))
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

// String은 rate limiter의 상태를 문자열로 반환합니다.
func (r *TokenBucketRateLimiter) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()

	return fmt.Sprintf("RateLimiter(rate=%.2f/s, burst=%d, tokens=%.2f)",
		r.rate, r.burst, r.tokens)
}
