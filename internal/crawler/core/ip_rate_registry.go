package core

import (
	"context"
	"sync"
)

// IPRateLimiterRegistry는 IP별 독립 RateLimiter를 관리합니다.
// URL → IP 해석 후 해당 IP의 rate limiter로 요청을 제어합니다.
// 동일 IP를 공유하는 여러 도메인은 하나의 rate limiter를 공유합니다.
//
// IPRateLimiterRegistry manages per-IP rate limiters.
// Multiple domains resolving to the same IP share one limiter.
type IPRateLimiterRegistry struct {
	resolver        IPResolver
	requestsPerHour int
	burst           int
	limiters        map[string]RateLimiter
	mu              sync.Mutex
}

// NewIPRateLimiterRegistry는 IP별 rate limiter 레지스트리를 생성합니다.
func NewIPRateLimiterRegistry(resolver IPResolver, requestsPerHour, burst int) *IPRateLimiterRegistry {
	return &IPRateLimiterRegistry{
		resolver:        resolver,
		requestsPerHour: requestsPerHour,
		burst:           burst,
		limiters:        make(map[string]RateLimiter),
	}
}

// Wait는 URL의 목적지 IP를 해석하고 해당 IP의 rate limiter에서 대기합니다.
// IP 해석 실패 시 rate limiting 없이 진행합니다 (graceful degradation).
func (r *IPRateLimiterRegistry) Wait(ctx context.Context, rawURL string) error {
	ip, err := r.resolver.Resolve(rawURL)
	if err != nil {
		// DNS 해석 실패 시 rate limiting 없이 진행
		return nil
	}

	limiter := r.getOrCreate(ip)
	return limiter.Wait(ctx)
}

// getOrCreate는 IP에 대한 rate limiter를 반환하거나 새로 생성합니다.
func (r *IPRateLimiterRegistry) getOrCreate(ip string) RateLimiter {
	r.mu.Lock()
	defer r.mu.Unlock()

	if limiter, ok := r.limiters[ip]; ok {
		return limiter
	}

	limiter := NewRateLimiter(r.requestsPerHour, r.burst)
	r.limiters[ip] = limiter
	return limiter
}
