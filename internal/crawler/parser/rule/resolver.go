// Package rule 은 DB 기반 파싱 규칙 (이슈 #100) 의 resolver 와 단일 parser engine 을 제공합니다.
//
// Package rule provides the URL → Rule resolver and a single rule-driven parser engine
// that implements both NewsArticleParser and NewsListParser. 사이트별 hardcode 파서를
// 대체하여 새 사이트 지원을 코드 변경 없이 DB rule 추가만으로 가능하게 합니다.
package rule

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"issuetracker/internal/storage"
	"issuetracker/pkg/logger"
)

// DefaultCacheTTL 은 Resolver 의 기본 양성 캐시 TTL 입니다.
// 너무 길면 운영자가 새 rule enabled 후에도 한참 미반영, 너무 짧으면 DB 부하.
const DefaultCacheTTL = 5 * time.Minute

// DefaultNegativeCacheTTL 은 미매칭 (ErrNotFound) 결과의 캐시 TTL 입니다.
// 양성보다 짧게 — 새 rule 등록 시 빠르게 반영되도록.
const DefaultNegativeCacheTTL = 30 * time.Second

// Resolver 는 URL 에서 host 를 추출해 storage.ParsingRuleRecord 를 조회합니다 (이슈 #100).
//
// Resolver maps a URL to its active ParsingRule via host_pattern + target_type.
// In-memory cache (TTL based) 로 DB roundtrip 을 줄입니다 — 운영자는 새 rule enabled 후
// 최대 DefaultCacheTTL 만큼 지연을 감수합니다.
//
// goroutine-safe — sync.RWMutex 로 cache 보호.
type Resolver struct {
	repo             storage.ParsingRuleRepository
	cacheTTL         time.Duration
	negativeCacheTTL time.Duration
	log              *logger.Logger

	mu    sync.RWMutex
	cache map[cacheKey]cacheEntry
	now   func() time.Time // 테스트 주입 (실시각 → fake clock)
}

// cacheKey 는 (host, target_type) 튜플입니다 — DB FindActive 인자와 1:1 매칭.
type cacheKey struct {
	host       string
	targetType storage.TargetType
}

// cacheEntry 는 lookup 결과를 캐싱합니다. rule==nil 이면 negative cache.
type cacheEntry struct {
	rule      *storage.ParsingRuleRecord
	expiresAt time.Time
}

// Option 은 Resolver 생성 옵션입니다.
type Option func(*Resolver)

// WithCacheTTL 은 양성 lookup 결과의 cache TTL 을 override 합니다.
func WithCacheTTL(d time.Duration) Option {
	return func(r *Resolver) { r.cacheTTL = d }
}

// WithNegativeCacheTTL 은 미매칭 결과의 cache TTL 을 override 합니다.
func WithNegativeCacheTTL(d time.Duration) Option {
	return func(r *Resolver) { r.negativeCacheTTL = d }
}

// WithLogger 는 cache miss / 만료 등의 trace 를 출력할 logger 를 주입합니다 (nil 허용).
func WithLogger(log *logger.Logger) Option {
	return func(r *Resolver) { r.log = log }
}

// NewResolver 는 ParsingRuleRepository 를 사용하는 Resolver 를 생성합니다.
// repo 가 nil 이면 panic — application 시작 시점 wire 누락 즉시 가시화.
func NewResolver(repo storage.ParsingRuleRepository, opts ...Option) *Resolver {
	if repo == nil {
		panic("rule: NewResolver requires non-nil repo")
	}
	r := &Resolver{
		repo:             repo,
		cacheTTL:         DefaultCacheTTL,
		negativeCacheTTL: DefaultNegativeCacheTTL,
		cache:            make(map[cacheKey]cacheEntry),
		now:              time.Now,
	}
	for _, o := range opts {
		o(r)
	}
	return r
}

// ResolveByURL 은 URL 에서 host 를 추출해 매칭 활성 규칙을 반환합니다.
//
// 흐름:
//  1. URL parse 실패 → ErrInvalidURL
//  2. cache hit (양성) → 즉시 반환
//  3. cache hit (negative, 미만료) → ErrNoRule (DB roundtrip 회피)
//  4. cache miss → repo.FindActive → 결과를 cache 후 반환
//
// 매칭 없음은 storage.ErrNotFound 가 아닌 rule.ErrNoRule 로 정규화 — 호출자가 errors.Is
// 로 분기 가능 (예: LLM 자동 생성 fallback 트리거).
func (r *Resolver) ResolveByURL(ctx context.Context, rawURL string, targetType storage.TargetType) (*storage.ParsingRuleRecord, error) {
	host, err := extractHost(rawURL)
	if err != nil {
		return nil, &Error{Code: ErrInvalidURL, Message: err.Error(), URL: rawURL}
	}
	return r.Resolve(ctx, host, targetType)
}

// Resolve 는 host 를 직접 받아 매칭 활성 규칙을 반환합니다 (host 가 이미 추출된 경우).
func (r *Resolver) Resolve(ctx context.Context, host string, targetType storage.TargetType) (*storage.ParsingRuleRecord, error) {
	host = strings.ToLower(host)
	key := cacheKey{host: host, targetType: targetType}

	if rule, hit, negative := r.lookupCache(key); hit {
		if negative {
			return nil, &Error{Code: ErrNoRule, Message: "no active rule (cached)", Host: host, TargetType: string(targetType)}
		}
		return rule, nil
	}

	rule, err := r.repo.FindActive(ctx, host, targetType)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			r.storeCache(key, nil, r.negativeCacheTTL)
			return nil, &Error{Code: ErrNoRule, Message: "no active rule", Host: host, TargetType: string(targetType)}
		}
		return nil, fmt.Errorf("find active rule (%s, %s): %w", host, targetType, err)
	}
	r.storeCache(key, rule, r.cacheTTL)
	return rule, nil
}

// Invalidate 는 (host, type) 의 cache entry 를 즉시 제거합니다 — 운영자가 rule 변경 직후 호출.
func (r *Resolver) Invalidate(host string, targetType storage.TargetType) {
	host = strings.ToLower(host)
	r.mu.Lock()
	delete(r.cache, cacheKey{host: host, targetType: targetType})
	r.mu.Unlock()
}

// InvalidateAll 은 모든 cache 를 비웁니다.
func (r *Resolver) InvalidateAll() {
	r.mu.Lock()
	r.cache = make(map[cacheKey]cacheEntry)
	r.mu.Unlock()
}

// lookupCache 는 캐시 조회 결과를 반환합니다.
// 반환값: (rule, hit, negative) — hit=true 이면 캐시 적용, negative=true 이면 미매칭 캐시.
func (r *Resolver) lookupCache(key cacheKey) (*storage.ParsingRuleRecord, bool, bool) {
	r.mu.RLock()
	entry, ok := r.cache[key]
	r.mu.RUnlock()
	if !ok || r.now().After(entry.expiresAt) {
		return nil, false, false
	}
	return entry.rule, true, entry.rule == nil
}

// storeCache 는 entry 를 저장합니다 (rule==nil 이면 negative cache).
func (r *Resolver) storeCache(key cacheKey, rule *storage.ParsingRuleRecord, ttl time.Duration) {
	r.mu.Lock()
	r.cache[key] = cacheEntry{rule: rule, expiresAt: r.now().Add(ttl)}
	r.mu.Unlock()
}

// extractHost 는 URL 문자열에서 host (소문자) 를 추출합니다.
func extractHost(rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("parse url: %w", err)
	}
	// u.Hostname() — 포트 부분 ":8080" 을 제거 (Gemini code review 피드백).
	// DB 의 host_pattern 이 순수 호스트네임이라 포트 포함 매칭 실패 회피.
	host := u.Hostname()
	if host == "" {
		return "", fmt.Errorf("empty host in url %q", rawURL)
	}
	return strings.ToLower(host), nil
}
