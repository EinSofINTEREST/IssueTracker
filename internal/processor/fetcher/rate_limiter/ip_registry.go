package rate_limiter

import (
	"context"
	"sync"
	"time"

	"issuetracker/internal/processor/fetcher/core"
	"issuetracker/pkg/logger"
)

// IPRateLimiterRegistry는 IP별 독립 RateLimiter를 관리합니다.
// URL → IP 해석 후 해당 IP의 rate limiter로 요청을 제어합니다.
// 동일 IP를 공유하는 여러 도메인은 하나의 rate limiter를 공유합니다.
// 미사용 limiter는 TTL 만료 후 자동 정리됩니다.
//
// IPRateLimiterRegistry manages per-IP rate limiters.
// Multiple domains resolving to the same IP share one limiter.
//
// RPH 결정 정책:
//   - configResolver != nil: host 단위 동적 lookup (fetcher_rules.requests_per_hour) —
//     운영 중 UPDATE 후 다음 신규 limiter 부터 새 RPH 반영. 기존 limiter 는 evict TTL 후 재생성.
//   - configResolver == nil: legacy 정적 모드 — requestsPerHour 멤버 변수 사용.
type IPRateLimiterRegistry struct {
	resolver        core.IPResolver
	configResolver  SourceConfigResolver // nil 허용 (legacy static mode)
	requestsPerHour int                  // configResolver == nil 일 때만 사용
	burst           int
	limiters        map[string]limiterEntry
	mu              sync.Mutex
	evictTTL        time.Duration
}

type limiterEntry struct {
	limiter  core.RateLimiter
	lastUsed time.Time
}

// NewIPRateLimiterRegistry는 정적 RPH 기반 rate limiter 레지스트리를 생성합니다 (legacy).
//
// 신규 wiring 은 NewIPRateLimiterRegistryWithResolver 사용 권장 — 운영 중 RPH 동적 반영.
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

// NewIPRateLimiterRegistryWithResolver 는 SourceConfigResolver 기반 동적 rate limiter
// 레지스트리를 생성합니다.
//
// 새 limiter 생성 시점에 configResolver.Resolve(host).RequestsPerHour 를 lookup —
// fetcher_rules.requests_per_hour UPDATE 가 다음 limiter 부터 자연 반영.
//
// configResolver 는 nil 이면 안 됨 — 본 생성자는 requestsPerHour 멤버를 설정하지 않으므로
// nil resolver 시 모든 IP 에 RPH=0 (noop limiter) 적용. 정적 RPH 가 필요하면 대신
// NewIPRateLimiterRegistry 사용.
//
// burst 는 fetcher_rules 에 컬럼 없으므로 호출자가 정적 값 주입.
func NewIPRateLimiterRegistryWithResolver(resolver core.IPResolver, configResolver SourceConfigResolver, burst int) *IPRateLimiterRegistry {
	return &IPRateLimiterRegistry{
		resolver:       resolver,
		configResolver: configResolver,
		burst:          burst,
		limiters:       make(map[string]limiterEntry),
		evictTTL:       1 * time.Hour,
	}
}

// Wait는 URL의 목적지 IP를 해석하고 해당 IP의 rate limiter에서 대기합니다.
// IP 해석 실패 시 ctx.Err()가 존재하면 해당 에러를 반환하고,
// 그 외의 경우 rate limiting 없이 진행합니다 (graceful degradation).
//
// configResolver 모드: host 추출 후 lock 획득 전에 RPH 를 미리 resolve — DB I/O 가 mutex
// 안에서 발생하지 않도록 (gemini Major 반영). lookup 결과를 getOrCreate 에 값으로 전달.
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

	// configResolver 모드에서만 host 추출 + 사전 lookup. 정적 모드는 host 무시 + 멤버 RPH 사용.
	// resolver.Resolve 는 mutex 밖에서 호출 — DB I/O 가 임계 구역에 stall 되지 않도록.
	rph := r.requestsPerHour
	if r.configResolver != nil {
		host, herr := extractHost(rawURL)
		if herr == nil {
			cfg, rerr := r.configResolver.Resolve(ctx, host)
			if rerr == nil {
				rph = cfg.RequestsPerHour
			}
			// resolver 실패 / extractHost 실패 모두 fail-open — 멤버 RPH (0=unlimited) 사용.
		}
	}

	limiter := r.getOrCreate(ip, rph)
	return limiter.Wait(ctx)
}

// getOrCreate는 IP에 대한 rate limiter를 반환하거나 새로 생성합니다.
// 접근 시마다 lastUsed를 갱신하고, 만료된 항목을 정리합니다.
//
// rph 인자는 호출자 (Wait) 가 lock 밖에서 미리 resolve 한 값. mutex 안에서는 lookup 없이
// 즉시 limiter 생성 — DB I/O 가 임계 구역을 차단하지 않도록.
func (r *IPRateLimiterRegistry) getOrCreate(ip string, rph int) core.RateLimiter {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()

	if entry, ok := r.limiters[ip]; ok {
		entry.lastUsed = now
		r.limiters[ip] = entry
		return entry.limiter
	}

	// label 은 throttle 로그 추적 식별자 — IP registry 는 IP 그대로 전달 (이슈 #503).
	// 같은 IP 의 모든 host 가 limiter 를 공유하므로 host 보다 IP 가 정확한 식별자.
	limiter := NewRateLimiter(rph, r.burst, ip)
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
