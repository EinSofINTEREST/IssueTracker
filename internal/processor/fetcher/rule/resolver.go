// Package rule 는 fetcher 단계의 host 단위 정책 (fetcher_rules) 을 매핑하는 Resolver 를 제공합니다.
//
// fetcher_rules 테이블에 등록된 host 는 본 Resolver 가 해당 fetcher (goquery / chromedp) 를
// 반환합니다. 미등록 host 는 ResolveResult{Found: false} 가 되어 호출자 (ChainHandler) 가
// default chain (현재 동작) 을 사용합니다.
//
// 핫패스 호출 빈도가 높아 sync.Map 기반 in-memory cache + TTL 로 DB 부하를 흡수합니다.
// 캐시 entry 는 negative (not found) 도 보관 — 동일 host 의 반복 조회가 매번 DB 까지 가지
// 않도록.
package rule

import (
	"context"
	"errors"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"issuetracker/internal/storage"
	"issuetracker/internal/storage/model"
	"issuetracker/internal/storage/repository"
	"issuetracker/pkg/logger"
)

// DefaultCacheTTL 은 cache entry 의 기본 유효기간입니다.
//
// 운영자 manual UPSERT 또는 단계 3 의 자동 UPSERT 가 적용되기까지의 최대 지연.
// 너무 짧으면 DB 부하 ↑, 너무 길면 운영 변경 반영 지연 ↑. 5분이 일반적 trade-off.
const DefaultCacheTTL = 5 * time.Minute

// ResolveResult 는 host 단위 fetcher 룰 조회 결과입니다.
//
// Found=false 면 매칭 룰 없음 — 호출자는 default chain 동작.
// Found=true 면 Fetcher 가 'goquery' 또는 'chromedp'.
type ResolveResult struct {
	Found   bool
	Fetcher model.FetcherKind
}

// Resolver 는 host → fetcher 매핑을 조회합니다.
//
// 모든 구현체는 goroutine-safe 해야 합니다 — fetcher worker 가 동시에 Resolve 를 호출.
type Resolver interface {
	Resolve(ctx context.Context, host string) (ResolveResult, error)

	// Invalidate 는 host 의 cache entry 를 즉시 만료시킵니다 (단계 3 의 자동 UPSERT 직후 호출).
	Invalidate(host string)
}

// cacheEntry 는 sync.Map 의 value — Resolve 결과 + cached 시각.
type cacheEntry struct {
	result   ResolveResult
	cachedAt time.Time
}

// dbResolver 는 fetcher_rules 테이블을 source of truth 로 사용하는 Resolver 입니다.
//
// singleflight 로 thundering herd 방지 (gemini 피드백): cache miss / 만료 시점에 동일 host 에
// 대해 여러 goroutine 이 동시에 진입해도 DB 조회는 단 1회만 발생하고 모든 caller 가 같은 결과를
// 공유함.
type dbResolver struct {
	repo   repository.FetcherRuleRepository
	log    *logger.Logger
	ttl    time.Duration
	cache  sync.Map // map[string]cacheEntry
	flight singleflight.Group
}

// NewResolver 는 새 dbResolver 를 생성합니다.
//
// repo 가 nil 이면 wiring 오류 — error 반환.
// ttl 이 0 이하이면 DefaultCacheTTL 사용.
func NewResolver(repo repository.FetcherRuleRepository, log *logger.Logger, ttl time.Duration) (Resolver, error) {
	if repo == nil {
		return nil, errors.New("rule: NewResolver requires non-nil FetcherRuleRepository")
	}
	if ttl <= 0 {
		ttl = DefaultCacheTTL
	}
	return &dbResolver{repo: repo, log: log, ttl: ttl}, nil
}

// Resolve 는 host 의 fetcher_rules row 를 조회합니다.
//
// cache hit 시 즉시 반환. miss / 만료 시 singleflight 로 DB 조회를 단일화 (동일 host 동시 진입
// 시 1회만 hit), 결과 (positive / negative) 모두 cache. 빈 host 는 ResolveResult{Found: false},
// nil 에러 — 핫패스에서 빈 host 도 default 분기.
func (r *dbResolver) Resolve(ctx context.Context, host string) (ResolveResult, error) {
	if host == "" {
		return ResolveResult{}, nil
	}

	if cached, ok := r.cache.Load(host); ok {
		if e, ok := cached.(cacheEntry); ok && time.Since(e.cachedAt) < r.ttl {
			return e.result, nil
		}
	}

	v, err, _ := r.flight.Do(host, func() (interface{}, error) {
		// double-check: singleflight leader 가 진입한 사이 다른 leader 가 cache 채웠을 수 있음.
		if cached, ok := r.cache.Load(host); ok {
			if e, ok := cached.(cacheEntry); ok && time.Since(e.cachedAt) < r.ttl {
				return e.result, nil
			}
		}
		rec, err := r.repo.GetByHost(ctx, host)
		if err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				result := ResolveResult{Found: false}
				r.cache.Store(host, cacheEntry{result: result, cachedAt: time.Now()})
				return result, nil
			}
			return ResolveResult{}, err
		}
		result := ResolveResult{Found: true, Fetcher: rec.Fetcher}
		r.cache.Store(host, cacheEntry{result: result, cachedAt: time.Now()})
		return result, nil
	})
	if err != nil {
		return ResolveResult{}, err
	}
	return v.(ResolveResult), nil
}

// Invalidate 는 host 의 cache entry 를 즉시 제거합니다.
func (r *dbResolver) Invalidate(host string) {
	r.cache.Delete(host)
}
