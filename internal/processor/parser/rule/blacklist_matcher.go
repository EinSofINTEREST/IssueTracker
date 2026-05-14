package rule

import (
	"context"
	"errors"
	"issuetracker/internal/storage/model"
	"issuetracker/internal/storage/repository"
	"regexp"
	"strings"
	"sync"
	"time"
)

// DefaultBlacklistCacheTTL 은 BlacklistMatcher 의 기본 양성 캐시 TTL 입니다.
//
// Resolver (5m) 와 동일 — 운영자가 manual 등록 / disable 토글 후 약간의 지연을 허용하되,
// 핫패스 lookup 의 DB 부담은 거의 0 으로.
const DefaultBlacklistCacheTTL = 5 * time.Minute

// DefaultBlacklistNegativeCacheTTL 은 host 매칭 row 부재 (빈 슬라이스) 의 캐시 TTL 입니다.
//
// 양성보다 짧게 — 운영자가 신규 host 를 등록한 직후 빠르게 반영되도록.
const DefaultBlacklistNegativeCacheTTL = 30 * time.Second

// DefaultBlacklistMaxCacheEntries 는 host 단위 cache 의 최대 entry 수입니다.
const DefaultBlacklistMaxCacheEntries = 10_000

// DefaultBlacklistMaxRegexEntries 는 path_pattern regex 컴파일 결과 cache 의 최대 entry 수입니다.
//
// sync.Map 은 키 공간이 작고 bounded 일 때만 권장 — 향후 auto source
// 등장 시 unique pattern 수가 증가할 수 있어 메모리 unbounded growth 가능. cap + simple evict
// 로 보호.
const DefaultBlacklistMaxRegexEntries = 10_000

// BlacklistMatcher 는 (host, path) 매칭으로 blacklist 차단 여부를 판단합니다.
//
// Resolver 와 동일 패턴 — host 단위 후보 슬라이스를 cache + DB lookup, application 측에서
// path regex 매칭. catch-all (path_pattern="") 은 LENGTH DESC 정렬상 가장 마지막에 평가.
//
// goroutine-safe.
type BlacklistMatcher struct {
	repo repository.BlacklistRepository

	cacheTTL         time.Duration
	negativeCacheTTL time.Duration
	maxEntries       int
	maxRegexEntries  int

	mu    sync.RWMutex
	cache map[string]blacklistCacheEntry // key: host

	regexMu    sync.RWMutex
	regexCache map[string]*blacklistCompiledPattern // pattern → compiled (cap + simple evict)

	now func() time.Time
}

type blacklistCacheEntry struct {
	rows      []*model.BlacklistRecord // 빈 슬라이스 = negative cache
	expiresAt time.Time
}

type blacklistCompiledPattern struct {
	re  *regexp.Regexp // 컴파일 실패 또는 빈 패턴이면 nil
	err error
}

// BlacklistMatcherOption 은 BlacklistMatcher 생성 옵션입니다.
type BlacklistMatcherOption func(*BlacklistMatcher)

// WithBlacklistCacheTTL 는 양성 lookup 결과 cache TTL 을 override 합니다.
func WithBlacklistCacheTTL(d time.Duration) BlacklistMatcherOption {
	return func(m *BlacklistMatcher) {
		if d > 0 {
			m.cacheTTL = d
		}
	}
}

// WithBlacklistNegativeCacheTTL 는 미매칭 (빈 슬라이스) cache TTL 을 override 합니다.
func WithBlacklistNegativeCacheTTL(d time.Duration) BlacklistMatcherOption {
	return func(m *BlacklistMatcher) {
		if d > 0 {
			m.negativeCacheTTL = d
		}
	}
}

// WithBlacklistMaxCacheEntries 는 host 단위 cache 의 최대 entry 수를 override 합니다.
func WithBlacklistMaxCacheEntries(n int) BlacklistMatcherOption {
	return func(m *BlacklistMatcher) {
		if n > 0 {
			m.maxEntries = n
		}
	}
}

// WithBlacklistMaxRegexEntries 는 path_pattern regex 컴파일 cache 의 최대 entry 수를 override 합니다.
func WithBlacklistMaxRegexEntries(n int) BlacklistMatcherOption {
	return func(m *BlacklistMatcher) {
		if n > 0 {
			m.maxRegexEntries = n
		}
	}
}

// NewBlacklistMatcher 는 BlacklistRepository 를 사용하는 Matcher 를 생성합니다.
// repo 가 nil 이면 error — 호출자 (cmd/main) 가 boot fatal 처리.
func NewBlacklistMatcher(repo repository.BlacklistRepository, opts ...BlacklistMatcherOption) (*BlacklistMatcher, error) {
	if repo == nil {
		return nil, errors.New("rule: NewBlacklistMatcher requires non-nil repo")
	}
	m := &BlacklistMatcher{
		repo:             repo,
		cacheTTL:         DefaultBlacklistCacheTTL,
		negativeCacheTTL: DefaultBlacklistNegativeCacheTTL,
		maxEntries:       DefaultBlacklistMaxCacheEntries,
		maxRegexEntries:  DefaultBlacklistMaxRegexEntries,
		cache:            make(map[string]blacklistCacheEntry),
		regexCache:       make(map[string]*blacklistCompiledPattern),
		now:              time.Now,
	}
	for _, o := range opts {
		o(m)
	}
	return m, nil
}

// IsBlocked 는 rawURL 이 enabled blacklist row 에 매칭되는지 반환합니다.
//
// 흐름:
//  1. URL parse 실패 → false (차단하지 않음 — 안전 fallback)
//  2. host cache hit → 후보 슬라이스에서 path 매칭
//  3. cache miss → repo.FindEnabledByHost → cache + path 매칭
//
// 어느 한 row 라도 path 매칭하면 true. DB 에러는 best-effort — false + error 반환 (호출자가
// 에러 무시하면 차단 안 함, 운영 안전성 우선).
func (m *BlacklistMatcher) IsBlocked(ctx context.Context, rawURL string) (bool, error) {
	host, path, err := extractHostPath(rawURL)
	if err != nil {
		return false, nil
	}
	host = strings.ToLower(host)

	rows, lookupErr := m.lookup(ctx, host)
	if lookupErr != nil {
		return false, lookupErr
	}
	for _, r := range rows {
		if m.pathMatches(r.PathPattern, path) {
			return true, nil
		}
	}
	return false, nil
}

// Filter 는 입력 URL 슬라이스에서 blacklist 매칭 항목을 제거하여 반환합니다.
//
// 동일 호스트가 반복되는 카테고리 페이지 링크 시나리오에서 host cache 가 재사용되어 비용
// 거의 없음. 개별 IsBlocked 호출 에러는 best-effort — 해당 URL 만 통과 (차단 안 함).
//
// Deprecated: mode 컬럼 도입 후 Classify 사용 권장 — Filter 는 Allowed 만 반환하여
// 'extract_links_only' 모드의 URL 도 함께 drop 됨. 호환성을 위해 메소드는 유지.
func (m *BlacklistMatcher) Filter(ctx context.Context, urls []string) []string {
	if len(urls) == 0 {
		return urls
	}
	out := make([]string, 0, len(urls))
	for _, u := range urls {
		blocked, err := m.IsBlocked(ctx, u)
		if err != nil {
			// best-effort: lookup 에러 시 차단 안 함 (안전 fallback) — 호출자에게 별도 통지 X.
			out = append(out, u)
			continue
		}
		if !blocked {
			out = append(out, u)
		}
	}
	return out
}

// BlacklistDecision 은 Classify 의 결과 — mode 별로 URL 슬라이스를 분리합니다.
//
// 호출자 (parser_worker) 는 슬라이스별로 다른 publish 분기를 적용:
//   - Allowed          : blacklist 매칭 X 또는 lookup 에러 (best-effort 통과) — 정상 article 발행
//   - ExtractLinksOnly : mode='extract_links_only' 매칭 — list 로 강제 발행 (ParseLinks 만)
//   - 'drop' 매칭 URL 은 어느 슬라이스에도 들어가지 않음 (완전 drop)
type BlacklistDecision struct {
	Allowed          []string
	ExtractLinksOnly []string
}

// Classify 는 입력 URL 슬라이스를 mode 별로 분류합니다.
//
// 매칭 정책:
//  1. blacklist row 매칭 X → Allowed
//  2. lookup 에러 → Allowed (best-effort, 안전 fallback)
//  3. mode='extract_links_only' 매칭 → ExtractLinksOnly
//  4. mode='drop' 매칭 → 어느 슬라이스에도 미포함 (완전 drop)
//
// 같은 host 의 여러 row 가 매칭하면 LENGTH(path_pattern) DESC 정렬상 첫 매칭 row 의 mode 채택.
func (m *BlacklistMatcher) Classify(ctx context.Context, urls []string) BlacklistDecision {
	out := BlacklistDecision{
		Allowed:          make([]string, 0, len(urls)),
		ExtractLinksOnly: make([]string, 0),
	}
	for _, u := range urls {
		mode, err := m.matchedMode(ctx, u)
		if err != nil {
			// best-effort: lookup 에러 시 Allowed 통과 — Filter 와 동일 정책.
			out.Allowed = append(out.Allowed, u)
			continue
		}
		switch mode {
		case "":
			out.Allowed = append(out.Allowed, u)
		case model.BlacklistModeExtractLinksOnly:
			out.ExtractLinksOnly = append(out.ExtractLinksOnly, u)
		case model.BlacklistModeDrop:
			// 완전 drop — 어느 슬라이스에도 미포함.
		default:
			// 알 수 없는 mode (DB CHECK 가 막지만 방어 fallback) — drop 으로 간주하여 미포함.
		}
	}
	return out
}

// matchedMode 는 rawURL 에 매칭되는 첫 blacklist row 의 mode 를 반환합니다.
//
// 매칭 없음: ("", nil). lookup 에러: ("", err). 매칭됨: (mode, nil).
// LENGTH(path_pattern) DESC 정렬 후보에서 첫 매칭 row 의 mode 사용 — 더 구체적 path 우선.
//
// MatchedMode 는 본 함수의 exported 별칭입니다 (precheck.blacklistSource 등 단일 URL hot-path 호출용).
func (m *BlacklistMatcher) MatchedMode(ctx context.Context, rawURL string) (model.BlacklistMode, error) {
	return m.matchedMode(ctx, rawURL)
}

func (m *BlacklistMatcher) matchedMode(ctx context.Context, rawURL string) (model.BlacklistMode, error) {
	host, path, err := extractHostPath(rawURL)
	if err != nil {
		return "", nil
	}
	host = strings.ToLower(host)

	rows, lookupErr := m.lookup(ctx, host)
	if lookupErr != nil {
		return "", lookupErr
	}
	for _, r := range rows {
		if m.pathMatches(r.PathPattern, path) {
			return r.Mode, nil
		}
	}
	return "", nil
}

// Invalidate 는 host 단위 cache entry 를 즉시 제거합니다.
//
// 운영자가 row insert/update/delete 직후 호출 — 다음 lookup 부터 fresh DB 결과 적용.
// invalidatingBlacklistRepo decorator (별도) 가 mutation 후 자동 호출.
func (m *BlacklistMatcher) Invalidate(host string) {
	host = strings.ToLower(host)
	m.mu.Lock()
	delete(m.cache, host)
	m.mu.Unlock()
}

// InvalidateAll 은 모든 cache 를 비웁니다.
func (m *BlacklistMatcher) InvalidateAll() {
	m.mu.Lock()
	m.cache = make(map[string]blacklistCacheEntry)
	m.mu.Unlock()
}

// lookup 은 cache hit 시 후보 슬라이스를 반환, miss 시 DB 조회 후 cache 에 저장합니다.
func (m *BlacklistMatcher) lookup(ctx context.Context, host string) ([]*model.BlacklistRecord, error) {
	now := m.now()
	m.mu.RLock()
	entry, ok := m.cache[host]
	m.mu.RUnlock()
	if ok && now.Before(entry.expiresAt) {
		return entry.rows, nil
	}

	fetched, err := m.repo.FindEnabledByHost(ctx, host)
	if err != nil {
		return nil, err
	}
	ttl := m.cacheTTL
	if len(fetched) == 0 {
		ttl = m.negativeCacheTTL
	}

	m.mu.Lock()
	if len(m.cache) >= m.maxEntries {
		// 가장 단순한 evict: 임의 1개 제거 (Resolver 와 동일 정책 — LRU 도입 비용 대비 효과 낮음).
		for k := range m.cache {
			delete(m.cache, k)
			break
		}
	}
	m.cache[host] = blacklistCacheEntry{rows: fetched, expiresAt: now.Add(ttl)}
	m.mu.Unlock()
	return fetched, nil
}

// pathMatches 는 path_pattern 이 path 와 매칭되는지 확인합니다 (Resolver.pathMatches 와 동일 정책).
//
//   - pattern="" → 모든 path 매칭 (catch-all)
//   - 컴파일 실패한 패턴은 매칭 안 됨 (negative cache 로 보관)
func (m *BlacklistMatcher) pathMatches(pattern, path string) bool {
	if pattern == "" {
		return true
	}
	cp := m.compileRegex(pattern)
	if cp.re == nil {
		return false
	}
	return cp.re.MatchString(path)
}

// compileRegex 는 pattern 의 컴파일 결과를 cache + reuse 합니다.
//
// sync.Map 대신 mutex-protected map 으로 변경 — maxRegexEntries cap 적용 + simple evict (1 random).
// auto source 등 unique pattern 수 증가 시 unbounded growth 회피.
func (m *BlacklistMatcher) compileRegex(pattern string) *blacklistCompiledPattern {
	m.regexMu.RLock()
	if cp, ok := m.regexCache[pattern]; ok {
		m.regexMu.RUnlock()
		return cp
	}
	m.regexMu.RUnlock()

	re, err := regexp.Compile(pattern)
	cp := &blacklistCompiledPattern{re: re, err: err}

	m.regexMu.Lock()
	defer m.regexMu.Unlock()
	// race winner 가 이미 채워뒀으면 그쪽 결과 반환 (LoadOrStore 동등 동작).
	if existing, ok := m.regexCache[pattern]; ok {
		return existing
	}
	if len(m.regexCache) >= m.maxRegexEntries {
		// cache map 와 동일 정책 — 임의 1개 evict (LRU 도입 비용 대비 효과 낮음).
		for k := range m.regexCache {
			delete(m.regexCache, k)
			break
		}
	}
	m.regexCache[pattern] = cp
	return cp
}
