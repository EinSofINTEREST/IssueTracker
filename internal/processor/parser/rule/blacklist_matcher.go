package rule

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"sync"
	"time"

	"issuetracker/internal/storage"
)

// DefaultBlacklistCacheTTL 은 BlacklistMatcher 의 기본 양성 캐시 TTL 입니다 (이슈 #295).
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

// BlacklistMatcher 는 (host, path) 매칭으로 blacklist 차단 여부를 판단합니다 (이슈 #295).
//
// Resolver 와 동일 패턴 — host 단위 후보 슬라이스를 cache + DB lookup, application 측에서
// path regex 매칭. catch-all (path_pattern="") 은 LENGTH DESC 정렬상 가장 마지막에 평가.
//
// goroutine-safe.
type BlacklistMatcher struct {
	repo storage.BlacklistRepository

	cacheTTL         time.Duration
	negativeCacheTTL time.Duration
	maxEntries       int

	mu    sync.RWMutex
	cache map[string]blacklistCacheEntry // key: host

	regexCache sync.Map // pattern (string) → *blacklistCompiledPattern

	now func() time.Time
}

type blacklistCacheEntry struct {
	rows      []*storage.BlacklistRecord // 빈 슬라이스 = negative cache
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

// NewBlacklistMatcher 는 BlacklistRepository 를 사용하는 Matcher 를 생성합니다.
// repo 가 nil 이면 error — 호출자 (cmd/main) 가 boot fatal 처리.
func NewBlacklistMatcher(repo storage.BlacklistRepository, opts ...BlacklistMatcherOption) (*BlacklistMatcher, error) {
	if repo == nil {
		return nil, errors.New("rule: NewBlacklistMatcher requires non-nil repo")
	}
	m := &BlacklistMatcher{
		repo:             repo,
		cacheTTL:         DefaultBlacklistCacheTTL,
		negativeCacheTTL: DefaultBlacklistNegativeCacheTTL,
		maxEntries:       DefaultBlacklistMaxCacheEntries,
		cache:            make(map[string]blacklistCacheEntry),
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
func (m *BlacklistMatcher) lookup(ctx context.Context, host string) ([]*storage.BlacklistRecord, error) {
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

func (m *BlacklistMatcher) compileRegex(pattern string) *blacklistCompiledPattern {
	if v, ok := m.regexCache.Load(pattern); ok {
		return v.(*blacklistCompiledPattern)
	}
	re, err := regexp.Compile(pattern)
	cp := &blacklistCompiledPattern{re: re, err: err}
	actual, _ := m.regexCache.LoadOrStore(pattern, cp)
	return actual.(*blacklistCompiledPattern)
}

// invalidatingBlacklistRepo 는 BlacklistRepository 를 wrap 하여 mutation 후 자동으로 Matcher 의
// host cache 를 invalidate 하는 decorator 입니다 (이슈 #295, parsing_rules 의 invalidatingRepo
// 와 동일 패턴).
//
// 적용 정책:
//   - Insert 성공          → Invalidate (host)
//   - Insert ErrDuplicate  → Invalidate (다른 인스턴스가 INSERT 했을 가능성 — cache stale)
//   - Update 성공          → 사전 GetByID 로 host lookup 후 Invalidate
//   - Delete 성공          → 사전 GetByID 로 host lookup 후 Invalidate
type invalidatingBlacklistRepo struct {
	inner storage.BlacklistRepository
	inv   BlacklistInvalidator
}

// BlacklistInvalidator 는 host cache invalidate 의 최소 인터페이스입니다.
// BlacklistMatcher 가 본 인터페이스를 만족.
type BlacklistInvalidator interface {
	Invalidate(host string)
}

// WrapBlacklistWithInvalidator 는 BlacklistRepository 를 invalidatingBlacklistRepo 로 wrap 합니다.
//
// inner nil 이면 panic — wiring 버그. inv nil 이면 wrapper 만 (invalidate skip).
func WrapBlacklistWithInvalidator(inner storage.BlacklistRepository, inv BlacklistInvalidator) storage.BlacklistRepository {
	if inner == nil {
		panic("rule: WrapBlacklistWithInvalidator requires non-nil inner repository")
	}
	return &invalidatingBlacklistRepo{inner: inner, inv: inv}
}

func (r *invalidatingBlacklistRepo) invalidate(host string) {
	if r.inv != nil {
		r.inv.Invalidate(host)
	}
}

func (r *invalidatingBlacklistRepo) Insert(ctx context.Context, rec *storage.BlacklistRecord) error {
	err := r.inner.Insert(ctx, rec)
	if err == nil || errors.Is(err, storage.ErrDuplicate) {
		r.invalidate(rec.HostPattern)
	}
	return err
}

func (r *invalidatingBlacklistRepo) Update(ctx context.Context, rec *storage.BlacklistRecord) error {
	err := r.inner.Update(ctx, rec)
	if err == nil {
		r.invalidate(rec.HostPattern)
	}
	return err
}

func (r *invalidatingBlacklistRepo) Delete(ctx context.Context, id int64) error {
	rec, lookupErr := r.inner.GetByID(ctx, id)
	if err := r.inner.Delete(ctx, id); err != nil {
		return err
	}
	if lookupErr == nil && rec != nil {
		r.invalidate(rec.HostPattern)
	}
	return nil
}

// 이하 read-only 위임.

func (r *invalidatingBlacklistRepo) GetByID(ctx context.Context, id int64) (*storage.BlacklistRecord, error) {
	return r.inner.GetByID(ctx, id)
}
func (r *invalidatingBlacklistRepo) FindEnabledByHost(ctx context.Context, host string) ([]*storage.BlacklistRecord, error) {
	return r.inner.FindEnabledByHost(ctx, host)
}
func (r *invalidatingBlacklistRepo) List(ctx context.Context, f storage.BlacklistFilter) ([]*storage.BlacklistRecord, error) {
	return r.inner.List(ctx, f)
}

// _ 컴파일 타임 인터페이스 만족 검증.
var _ storage.BlacklistRepository = (*invalidatingBlacklistRepo)(nil)
