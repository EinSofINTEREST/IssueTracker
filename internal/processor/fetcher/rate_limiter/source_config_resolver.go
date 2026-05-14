// source_config_resolver.go — fetcher_rules 의 RPH 등 source 단위 설정을 host 키로 동적 조회.
//
// rule.Resolver 와 동일 알고리즘 (sync.Map TTL cache + singleflight) — host 단위 설정을
// hot-path 에서 빠르게 반환하면서, 운영 중 fetcher_rules.requests_per_hour UPDATE 가 다음
// lookup (또는 Invalidate hook) 부터 자연 반영되도록 동적 layer 를 제공.
//
// 본 패키지는 IP 단위 token bucket 의 RPH 결정에 사용 — IPRateLimiterRegistry 가 새 limiter
// 생성 시 resolver.Resolve(host).RequestsPerHour 를 사용. 기존 limiter 는 IP 별 멤버 변수로
// 박혀있어 즉각 반영되지는 않으나, 1시간 evict TTL 후 재생성 시 새 RPH 적용.

package rate_limiter

import (
	"context"
	"errors"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"issuetracker/internal/storage"
	"issuetracker/internal/storage/repository"
	"issuetracker/pkg/logger"
)

// DefaultSourceConfigCacheTTL 는 cache entry 의 기본 유효기간입니다.
//
// 운영자 manual UPDATE 가 적용되기까지의 최대 지연. 너무 짧으면 DB 부하 ↑, 너무 길면 운영
// 변경 반영 지연 ↑. 5분이 일반적 trade-off (rule.Resolver 와 동일).
const DefaultSourceConfigCacheTTL = 5 * time.Minute

// SourceConfig 는 host 단위로 결정되는 source 의 동적 설정입니다.
//
// 현재는 RequestsPerHour 단일 필드. 향후 burst / timeout 등 host-aware 동적 설정을 추가할
// 수 있는 확장 지점.
type SourceConfig struct {
	// RequestsPerHour: token bucket 의 시간당 토큰 발급 수. 0 이면 noop limiter (제한 없음).
	RequestsPerHour int
}

// SourceConfigResolver 는 host → SourceConfig 매핑을 조회합니다.
//
// 모든 구현체는 goroutine-safe 해야 합니다 — 동시에 여러 fetch goroutine 이 Resolve 호출.
type SourceConfigResolver interface {
	// Resolve 는 host 의 SourceConfig 를 반환합니다.
	//
	// 매칭 row 없음 / DB 일시 장애 시 default (RPH=0, 즉 unlimited) 와 nil 에러 — fail-open
	// 정책. fetch path 가 limiter 부재로 인해 막히지 않도록 (rate limit 자체가 보조 안전망).
	Resolve(ctx context.Context, host string) (SourceConfig, error)

	// Invalidate 는 host 의 cache entry 를 즉시 만료시킵니다 (운영자 변경 반영 가속용).
	Invalidate(host string)
}

// sourceConfigCacheEntry 는 sync.Map 의 value — Resolve 결과 + cached 시각.
type sourceConfigCacheEntry struct {
	cfg      SourceConfig
	cachedAt time.Time
}

// dbSourceConfigResolver 는 fetcher_rules 테이블을 source of truth 로 사용하는 Resolver 입니다.
//
// rule.Resolver 와 동일하게 singleflight 로 thundering herd 방지 — cache miss / 만료 시점에
// 동일 host 에 대해 여러 goroutine 이 동시 진입해도 DB 조회는 단 1회만 발생.
type dbSourceConfigResolver struct {
	repo   repository.FetcherRuleRepository
	log    *logger.Logger
	ttl    time.Duration
	cache  sync.Map // map[string]sourceConfigCacheEntry
	flight singleflight.Group
}

// NewSourceConfigResolver 는 fetcher_rules 기반 dbSourceConfigResolver 를 생성합니다.
//
// repo 가 nil 이면 wiring 오류 — error 반환.
// ttl 이 0 이하이면 DefaultSourceConfigCacheTTL 사용.
func NewSourceConfigResolver(repo repository.FetcherRuleRepository, log *logger.Logger, ttl time.Duration) (SourceConfigResolver, error) {
	if repo == nil {
		return nil, errors.New("rate_limiter: NewSourceConfigResolver requires non-nil FetcherRuleRepository")
	}
	if ttl <= 0 {
		ttl = DefaultSourceConfigCacheTTL
	}
	return &dbSourceConfigResolver{repo: repo, log: log, ttl: ttl}, nil
}

// Resolve 는 host 의 fetcher_rules row 에서 RPH 를 조회합니다.
//
// 매칭 없음 (ErrNotFound) / DB 일시 장애 모두 RPH=0 으로 cache + 반환 — fail-open. 운영 중
// fetch 흐름이 rate limit 인프라 장애로 stall 되지 않도록 보수적 정책.
func (r *dbSourceConfigResolver) Resolve(ctx context.Context, host string) (SourceConfig, error) {
	if host == "" {
		return SourceConfig{}, nil
	}

	if cached, ok := r.cache.Load(host); ok {
		if e, ok := cached.(sourceConfigCacheEntry); ok && time.Since(e.cachedAt) < r.ttl {
			return e.cfg, nil
		}
	}

	// singleflight 의 모든 분기가 (cfg, nil) 반환 — fail-open 정책으로 외부 에러 전파 없음.
	// 따라서 err 슬롯은 dead — `_` 로 명시 무시.
	v, _, _ := r.flight.Do(host, func() (interface{}, error) {
		// double-check: singleflight leader 진입 사이 다른 leader 가 cache 채웠을 수 있음.
		if cached, ok := r.cache.Load(host); ok {
			if e, ok := cached.(sourceConfigCacheEntry); ok && time.Since(e.cachedAt) < r.ttl {
				return e.cfg, nil
			}
		}
		rec, err := r.repo.GetByHost(ctx, host)
		if err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				cfg := SourceConfig{}
				r.cache.Store(host, sourceConfigCacheEntry{cfg: cfg, cachedAt: time.Now()})
				return cfg, nil
			}
			// DB 일시 장애 — fail-open: cache 에 저장하지 않고 ephemeral default 반환.
			// 다음 호출이 다시 DB 시도 (실패한 결과를 ttl 동안 sticky 하게 cache 하지 않도록).
			if r.log != nil {
				r.log.WithFields(map[string]interface{}{
					"host": host,
				}).WithError(err).Warn("source config resolve failed — falling back to unlimited")
			}
			return SourceConfig{}, nil
		}
		cfg := SourceConfig{RequestsPerHour: rec.RequestsPerHour}
		r.cache.Store(host, sourceConfigCacheEntry{cfg: cfg, cachedAt: time.Now()})
		return cfg, nil
	})
	return v.(SourceConfig), nil
}

// Invalidate 는 host 의 cache entry 를 즉시 제거합니다.
//
// 운영자가 fetcher_rules.requests_per_hour UPDATE 직후 즉각 반영 원하면 호출. 미호출 시
// ttl 만료 (default 5분) 까지는 기존 RPH 가 유지됨.
func (r *dbSourceConfigResolver) Invalidate(host string) {
	r.cache.Delete(host)
}
