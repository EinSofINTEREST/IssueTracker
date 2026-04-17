package rate_limiter

import (
	"context"
	"sync"
	"time"

	"issuetracker/internal/crawler/core"
	"issuetracker/pkg/logger"
)

// IPRateLimiterRegistry는 IP별 독립 RateLimiter를 관리합니다.
// URL → IP 해석 후 해당 IP의 rate limiter로 요청을 제어합니다.
// 동일 IP를 공유하는 여러 도메인은 하나의 rate limiter를 공유합니다.
// 미사용 limiter는 TTL 만료 후 자동 정리됩니다.
//
// IPRateLimiterRegistry manages per-IP rate limiters.
// Multiple domains resolving to the same IP share one limiter.
type IPRateLimiterRegistry struct {
	resolver        core.IPResolver
	requestsPerHour int
	burst           int
	limiters        map[string]limiterEntry
	mu              sync.Mutex
	evictTTL        time.Duration
}

type limiterEntry struct {
	limiter  core.RateLimiter
	lastUsed time.Time
}

// NewIPRateLimiterRegistry는 IP별 rate limiter 레지스트리를 생성합니다.
// 미사용 limiter는 1시간 후 eviction 대상이 됩니다.
func NewIPRateLimiterRegistry(resolver core.IPResolver, requestsPerHour, burst int) *IPRateLimiterRegistry {
	return &IPRateLimiterRegistry{
		resolver:        resolver,
		requestsPerHour: requestsPerHour,
		burst:           burst,
		limiters:        make(map[string]limiterEntry),
		evictTTL:        1 * time.Hour,
	}
}

// Wait는 URL의 목적지 IP를 해석하고 해당 IP의 rate limiter에서 대기합니다.
// IP 해석 실패 시 ctx.Err()가 존재하면 해당 에러를 반환하고,
// 그 외의 경우 rate limiting 없이 진행합니다 (graceful degradation).
func (r *IPRateLimiterRegistry) Wait(ctx context.Context, rawURL string) error {
	ip, err := r.resolver.Resolve(ctx, rawURL)
	if err != nil {
		// ctx 취소/만료 신호를 호출자에게 전파
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		// DNS 해석 실패 시 rate limiting 없이 진행하되, 운영 중 감지할 수 있도록 경고 로그 출력
		log := logger.FromContext(ctx)
		log.WithFields(map[string]interface{}{
			"url": rawURL,
		}).WithError(err).Warn("dns resolve failed, proceeding without rate limiting")
		return nil
	}

	limiter := r.getOrCreate(ip)
	return limiter.Wait(ctx)
}

// getOrCreate는 IP에 대한 rate limiter를 반환하거나 새로 생성합니다.
// 접근 시마다 lastUsed를 갱신하고, 만료된 항목을 정리합니다.
func (r *IPRateLimiterRegistry) getOrCreate(ip string) core.RateLimiter {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()

	if entry, ok := r.limiters[ip]; ok {
		entry.lastUsed = now
		r.limiters[ip] = entry
		return entry.limiter
	}

	limiter := NewRateLimiter(r.requestsPerHour, r.burst)
	r.limiters[ip] = limiterEntry{limiter: limiter, lastUsed: now}

	// 주기적 eviction: 새 limiter 생성 시점에 만료 항목 정리
	r.evictExpired(now)

	return limiter
}

// evictExpired는 evictTTL 이상 미사용된 limiter를 삭제합니다.
// 호출 전에 mu lock을 획득해야 합니다.
func (r *IPRateLimiterRegistry) evictExpired(now time.Time) {
	cutoff := now.Add(-r.evictTTL)
	for ip, entry := range r.limiters {
		if entry.lastUsed.Before(cutoff) {
			delete(r.limiters, ip)
		}
	}
}
