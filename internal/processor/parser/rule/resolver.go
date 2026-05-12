// Package rule 은 DB 기반 파싱 규칙 의 resolver 와 단일 parser engine 을 제공합니다.
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
	"regexp"
	"strings"
	"sync"
	"time"

	"issuetracker/internal/storage"
)

// DefaultCacheTTL 은 Resolver 의 기본 양성 캐시 TTL 입니다.
// 너무 길면 운영자가 새 rule enabled 후에도 한참 미반영, 너무 짧으면 DB 부하.
const DefaultCacheTTL = 5 * time.Minute

// DefaultNegativeCacheTTL 은 미매칭 (ErrNotFound) 결과의 캐시 TTL 입니다.
// 양성보다 짧게 — 새 rule 등록 시 빠르게 반영되도록.
const DefaultNegativeCacheTTL = 30 * time.Second

// DefaultMaxCacheEntries 는 cache 의 최대 entry 수입니다.
//
// 무제한 map 은 호스트 수 폭증 시 OOM 위험. 단순 정책 — 가득 차면 가장 오래된 entry
// (만료 임박 순) 를 evict. LRU 가 아니라 expiry-order eviction 이지만 본 패키지의
// 부하 패턴 (소수의 동일 host 반복 lookup) 에는 충분.
const DefaultMaxCacheEntries = 10_000

// Resolver 는 URL 에서 host 를 추출해 storage.ParserRuleRecord 를 조회합니다.
//
// Resolver maps a URL to its active ParserRule via host_pattern + target_type.
// In-memory cache (TTL based) 로 DB roundtrip 을 줄입니다 — 운영자는 새 rule enabled 후
// 최대 DefaultCacheTTL 만큼 지연을 감수합니다.
//
// cache value 가 단일 rule → rule 슬라이스 (host 매칭 후보들) 로 변경.
// ResolveByURL 이 슬라이스를 받아 application 측에서 URL path 와 path_pattern regex 매칭.
// path_pattern=” 인 row 는 catch-all 로 마지막에 위치 (LENGTH DESC 정렬).
//
// goroutine-safe — sync.RWMutex 로 cache 보호. regex compile cache 는 sync.Map.
type Resolver struct {
	repo             storage.ParserRuleRepository
	cacheTTL         time.Duration
	negativeCacheTTL time.Duration
	maxEntries       int

	mu    sync.RWMutex
	cache map[cacheKey]cacheEntry
	now   func() time.Time // 테스트 주입 (실시각 → fake clock)

	// hasAnyCache 는 HasAnyRule 결과 (exists, hasEnabled) 를 보관합니다.
	// negativeCacheTTL 과 동일 짧은 TTL — disabled 룰 토글 / 신규 룰 학습이 빠르게 반영되도록.
	hasAnyCache map[cacheKey]hasAnyEntry

	// regexCache 는 path_pattern 별 compile 결과를 보관합니다.
	// 같은 패턴의 재컴파일을 회피 — 운영 중 동일 host 의 후보 슬라이스가 cache 만료 시마다
	// 다시 fetch 되어도 regex 객체는 재사용. sync.Map 으로 lock-free read.
	regexCache sync.Map // map[string]*compiledPattern
}

// hasAnyEntry 는 HasAnyRule 결과 캐시 entry 입니다.
type hasAnyEntry struct {
	exists     bool
	hasEnabled bool
	expiresAt  time.Time
}

// compiledPattern 은 path_pattern 의 컴파일 결과 (또는 컴파일 실패) 를 보관합니다.
// 실패한 패턴은 err 를 보관하여 매번 재컴파일을 시도하지 않도록 negative cache 역할.
type compiledPattern struct {
	re  *regexp.Regexp // 컴파일 실패 또는 빈 패턴이면 nil
	err error
}

// cacheKey 는 (host, target_type) 튜플입니다 — DB FindActiveCandidates 인자와 1:1 매칭.
type cacheKey struct {
	host       string
	targetType storage.TargetType
}

// cacheEntry 는 lookup 결과를 캐싱합니다.
//
// 후보 슬라이스를 통째로 보관 — application 측 path 매칭은 매 호출마다 실행
// (cache hit path 안에서 수행). 매칭 비용은 ms 미만 (regex 컴파일은 regexCache 로 분리).
type cacheEntry struct {
	candidates []*storage.ParserRuleRecord // 빈 슬라이스 = negative cache
	expiresAt  time.Time
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

// WithMaxCacheEntries 는 cache 의 최대 entry 수를 override 합니다 (default: 10_000).
// 0 이하 값은 무시되고 default 유지.
func WithMaxCacheEntries(n int) Option {
	return func(r *Resolver) {
		if n > 0 {
			r.maxEntries = n
		}
	}
}

// NewResolver 는 ParserRuleRepository 를 사용하는 Resolver 를 생성합니다.
// repo 가 nil 이면 error — 호출자 (cmd/main) 가 boot fatal 처리.
func NewResolver(repo storage.ParserRuleRepository, opts ...Option) (*Resolver, error) {
	if repo == nil {
		return nil, errors.New("rule: NewResolver requires non-nil repo")
	}
	r := &Resolver{
		repo:             repo,
		cacheTTL:         DefaultCacheTTL,
		negativeCacheTTL: DefaultNegativeCacheTTL,
		maxEntries:       DefaultMaxCacheEntries,
		cache:            make(map[cacheKey]cacheEntry),
		hasAnyCache:      make(map[cacheKey]hasAnyEntry),
		now:              time.Now,
	}
	for _, o := range opts {
		o(r)
	}
	return r, nil
}

// ResolveByURL 은 URL 에서 host + path 를 추출해 매칭 활성 규칙을 반환합니다.
//
// 흐름:
//  1. URL parse 실패 → ErrInvalidURL
//  2. cache hit → 후보 슬라이스에서 path 매칭, 첫 매칭 rule 반환
//  3. cache miss → repo.FindActiveCandidates → cache 후 path 매칭
//
// 후보 슬라이스 안에서 path 매칭:
//   - path_pattern=” (catch-all) 은 모든 path 매칭, LENGTH DESC 정렬상 가장 마지막
//   - 더 구체적인 (긴) path_pattern 이 먼저 평가되어 우선 채택
//   - 매칭 없음 → ErrNoRule
//
// 매칭 없음은 storage.ErrNotFound 가 아닌 rule.ErrNoRule 로 정규화 — 호출자가 errors.Is
// 로 분기 가능 (예: LLM 자동 생성 fallback 트리거).
func (r *Resolver) ResolveByURL(ctx context.Context, rawURL string, targetType storage.TargetType) (*storage.ParserRuleRecord, error) {
	host, path, err := extractHostPath(rawURL)
	if err != nil {
		return nil, &Error{Code: ErrInvalidURL, Message: err.Error(), URL: rawURL}
	}
	return r.Resolve(ctx, host, path, targetType)
}

// Resolve 는 host + path 를 직접 받아 매칭 활성 규칙을 반환합니다.
//
// host 는 정규화 (lowercase, 포트 제외) 된 상태로 전달 권장. path 는 URL.Path (rawpath 아님) —
// 정규화는 호출자 책임.
func (r *Resolver) Resolve(ctx context.Context, host, path string, targetType storage.TargetType) (*storage.ParserRuleRecord, error) {
	host = strings.ToLower(host)
	key := cacheKey{host: host, targetType: targetType}

	candidates, hit := r.lookupCache(key)
	if !hit {
		fetched, err := r.repo.FindActiveCandidates(ctx, host, targetType)
		if err != nil {
			return nil, fmt.Errorf("find active candidates (%s, %s): %w", host, targetType, err)
		}
		ttl := r.cacheTTL
		if len(fetched) == 0 {
			ttl = r.negativeCacheTTL
		}
		r.storeCache(key, fetched, ttl)
		candidates = fetched
	}

	if len(candidates) == 0 {
		return nil, &Error{Code: ErrNoRule, Message: "no active rule (cached)", Host: host, TargetType: string(targetType)}
	}

	// 후보 슬라이스는 LENGTH(path_pattern) DESC 로 정렬됨 — 더 구체적인 패턴부터 평가.
	for _, c := range candidates {
		if r.pathMatches(c.PathPattern, path) {
			return c, nil
		}
	}
	return nil, &Error{Code: ErrNoRule, Message: "no rule matched url path", Host: host, URL: path, TargetType: string(targetType)}
}

// pathMatches 는 path_pattern 이 path 와 매칭되는지 확인합니다.
//
//   - pattern=” → 모든 path 매칭 (catch-all)
//   - pattern 컴파일 결과를 regexCache 에 보관 — 같은 패턴 재컴파일 회피
//   - 컴파일 실패한 패턴은 매칭 안 됨으로 간주 (negative cache 로 보관)
func (r *Resolver) pathMatches(pattern, path string) bool {
	if pattern == "" {
		return true
	}
	cp := r.compileRegex(pattern)
	if cp.re == nil {
		return false
	}
	return cp.re.MatchString(path)
}

// compileRegex 는 path_pattern 을 컴파일하고 결과를 sync.Map 에 캐시합니다.
// 같은 패턴이 여러 host 또는 cache 만료 후 재fetch 되어도 컴파일은 1회만 발생.
func (r *Resolver) compileRegex(pattern string) *compiledPattern {
	if v, ok := r.regexCache.Load(pattern); ok {
		return v.(*compiledPattern)
	}
	// regexp.Compile 은 에러 시 re=nil 반환 — 별도 nil 처리 불필요.
	re, err := regexp.Compile(pattern)
	cp := &compiledPattern{re: re, err: err}
	// LoadOrStore 로 race 시 첫 winner 의 결과를 보존 — 같은 패턴 두 goroutine 동시 컴파일도 안전.
	actual, _ := r.regexCache.LoadOrStore(pattern, cp)
	return actual.(*compiledPattern)
}

// Invalidate 는 (host, type) 의 cache entry 를 즉시 제거합니다 — 운영자가 rule 변경 직후 호출.
//
// hasAnyCache 도 함께 invalidate — 룰 INSERT/UPDATE/DELETE 시 enabled 상태 변동 가능.
func (r *Resolver) Invalidate(host string, targetType storage.TargetType) {
	host = strings.ToLower(host)
	key := cacheKey{host: host, targetType: targetType}
	r.mu.Lock()
	delete(r.cache, key)
	delete(r.hasAnyCache, key)
	r.mu.Unlock()
}

// InvalidateAll 은 모든 cache 를 비웁니다.
func (r *Resolver) InvalidateAll() {
	r.mu.Lock()
	r.cache = make(map[cacheKey]cacheEntry)
	r.hasAnyCache = make(map[cacheKey]hasAnyEntry)
	r.mu.Unlock()
}

// HasAnyRule 은 (host, target_type) 룰의 존재 여부 + enabled 여부를 short-TTL 캐시 + DB lookup 으로 반환합니다.
//
// 반환:
//   - exists     : enabled / disabled 무관 row 존재 여부
//   - hasEnabled : enabled=TRUE row 존재 여부
//   - err        : DB 조회 실패 (캐시 hit 시 nil)
//
// 용도: parser_worker 의 ErrNoRule 분기에서 \"진짜 부재\" 와 \"운영자 disable 잔존\" 을 구분 —
// 후자는 LLM 재학습 트리거 회피 (수동 재활성 영역).
//
// 캐시 정책:
//   - TTL = negativeCacheTTL (default 30s) — Resolve 의 negative cache 와 동일 짧은 TTL
//     운영자 enabled 토글 / 신규 INSERT 가 빠르게 반영되도록.
//   - Invalidate(host, type) 호출 시 본 캐시도 함께 무효화 — Resolve 캐시와 일관.
func (r *Resolver) HasAnyRule(ctx context.Context, host string, targetType storage.TargetType) (exists, hasEnabled bool, err error) {
	host = strings.ToLower(host)
	key := cacheKey{host: host, targetType: targetType}

	r.mu.RLock()
	if entry, ok := r.hasAnyCache[key]; ok && r.now().Before(entry.expiresAt) {
		r.mu.RUnlock()
		return entry.exists, entry.hasEnabled, nil
	}
	r.mu.RUnlock()

	exists, hasEnabled, err = r.repo.HasAnyRule(ctx, host, targetType)
	if err != nil {
		return false, false, err
	}

	r.mu.Lock()
	r.evictHasAnyExpiringSoon()
	r.hasAnyCache[key] = hasAnyEntry{
		exists:     exists,
		hasEnabled: hasEnabled,
		expiresAt:  r.now().Add(r.negativeCacheTTL),
	}
	r.mu.Unlock()
	return exists, hasEnabled, nil
}

// lookupCache 는 캐시 조회 결과를 반환합니다.
// 반환값: (candidates, hit) — hit=true 이면 캐시 적용 (negative cache 는 빈 슬라이스).
func (r *Resolver) lookupCache(key cacheKey) ([]*storage.ParserRuleRecord, bool) {
	r.mu.RLock()
	entry, ok := r.cache[key]
	r.mu.RUnlock()
	if !ok || r.now().After(entry.expiresAt) {
		return nil, false
	}
	return entry.candidates, true
}

// evictExpiringSoon 은 cache 가 maxEntries 초과 시 가장 만료 임박한 entry 를 제거합니다.
// 호출자는 이미 r.mu 를 hold 하고 있어야 합니다 (storeCache 안에서 호출).
//
// 단순 정책 — LRU 가 아니라 expiry-order eviction. 본 패키지의 부하 패턴 (소수의 동일
// host 반복 lookup) 에는 충분. 호스트 폭증 시 OOM 방어가 핵심 목적.
func (r *Resolver) evictExpiringSoon() {
	if len(r.cache) < r.maxEntries {
		return
	}
	var oldestKey cacheKey
	var oldestExpiry time.Time
	first := true
	for k, e := range r.cache {
		if first || e.expiresAt.Before(oldestExpiry) {
			oldestKey = k
			oldestExpiry = e.expiresAt
			first = false
		}
	}
	if !first {
		delete(r.cache, oldestKey)
	}
}

// evictHasAnyExpiringSoon 은 hasAnyCache 가 maxEntries 초과 시 만료 임박 entry 를 제거합니다.
// evictExpiringSoon 과 동일 정책 — host 폭증 시 OOM 방어. 호출자는 r.mu hold.
func (r *Resolver) evictHasAnyExpiringSoon() {
	if len(r.hasAnyCache) < r.maxEntries {
		return
	}
	var oldestKey cacheKey
	var oldestExpiry time.Time
	first := true
	for k, e := range r.hasAnyCache {
		if first || e.expiresAt.Before(oldestExpiry) {
			oldestKey = k
			oldestExpiry = e.expiresAt
			first = false
		}
	}
	if !first {
		delete(r.hasAnyCache, oldestKey)
	}
}

// storeCache 는 후보 슬라이스를 저장합니다.
// maxEntries 초과 시 evictExpiringSoon 으로 가장 만료 임박 entry 제거 후 저장.
func (r *Resolver) storeCache(key cacheKey, candidates []*storage.ParserRuleRecord, ttl time.Duration) {
	r.mu.Lock()
	r.evictExpiringSoon()
	r.cache[key] = cacheEntry{candidates: candidates, expiresAt: r.now().Add(ttl)}
	r.mu.Unlock()
}

// extractHostPath 는 URL 문자열에서 host (소문자) + path 를 추출합니다.
//
// host: 포트 제거 + 소문자.
// path: URL.Path (raw path 아님 — percent-decoded 표현). 빈 path 는 "/" 로 정규화.
func extractHostPath(rawURL string) (string, string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", "", fmt.Errorf("parse url: %w", err)
	}
	// u.Hostname() — 포트 부분 ":8080" 을 제거 (Gemini code review 피드백).
	// DB 의 host_pattern 이 순수 호스트네임이라 포트 포함 매칭 실패 회피.
	host := u.Hostname()
	if host == "" {
		return "", "", fmt.Errorf("empty host in url %q", rawURL)
	}
	path := u.Path
	if path == "" {
		path = "/"
	}
	return strings.ToLower(host), path, nil
}
