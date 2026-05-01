package rule_test

import (
	"context"
	"errors"
	"sort"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/parser/rule"
	"issuetracker/internal/storage"
)

// fakeRepo 는 in-memory ParsingRuleRepository 구현입니다.
// FindActiveCandidates 호출 횟수를 atomic 으로 추적하여 cache 동작을 검증합니다.
type fakeRepo struct {
	rules           []*storage.ParsingRuleRecord // FindActiveCandidates 매칭 후보
	notFound        bool                         // true 면 항상 빈 슬라이스 (negative cache 시뮬레이션)
	findActiveCalls int64
}

func (r *fakeRepo) Insert(_ context.Context, _ *storage.ParsingRuleRecord) error { return nil }
func (r *fakeRepo) Update(_ context.Context, _ *storage.ParsingRuleRecord) error { return nil }
func (r *fakeRepo) GetByID(_ context.Context, _ int64) (*storage.ParsingRuleRecord, error) {
	return nil, storage.ErrNotFound
}
func (r *fakeRepo) List(_ context.Context, _ storage.ParsingRuleFilter) ([]*storage.ParsingRuleRecord, error) {
	return nil, nil
}
func (r *fakeRepo) Delete(_ context.Context, _ int64) error { return nil }
func (r *fakeRepo) UpdatePathPattern(_ context.Context, _ int64, _, _ string) error {
	return nil
}

// FindActive 는 FindActiveCandidates 의 첫 항목을 반환 (이슈 #173 후방 호환).
func (r *fakeRepo) FindActive(ctx context.Context, host string, t storage.TargetType) (*storage.ParsingRuleRecord, error) {
	candidates, err := r.FindActiveCandidates(ctx, host, t)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, storage.ErrNotFound
	}
	return candidates[0], nil
}

// FindActiveCandidates 는 host + target_type 매칭 슬라이스 반환. LENGTH(path_pattern) DESC 정렬 시뮬레이션.
func (r *fakeRepo) FindActiveCandidates(_ context.Context, host string, t storage.TargetType) ([]*storage.ParsingRuleRecord, error) {
	atomic.AddInt64(&r.findActiveCalls, 1)
	if r.notFound {
		return nil, nil
	}
	out := make([]*storage.ParsingRuleRecord, 0)
	for _, rec := range r.rules {
		if rec.HostPattern == host && rec.TargetType == t && rec.Enabled {
			out = append(out, rec)
		}
	}
	// LENGTH(path_pattern) DESC stable sort (postgres 의 정렬과 동일)
	sortByPathPatternLengthDesc(out)
	return out, nil
}

func (r *fakeRepo) calls() int { return int(atomic.LoadInt64(&r.findActiveCalls)) }

// sortByPathPatternLengthDesc 는 LENGTH(path_pattern) DESC, version DESC 정렬을 수행합니다.
// postgres 의 ORDER BY LENGTH(path_pattern) DESC, version DESC 와 동일 동작 (PR #181 gemini 피드백).
func sortByPathPatternLengthDesc(s []*storage.ParsingRuleRecord) {
	sort.SliceStable(s, func(i, j int) bool {
		if len(s[i].PathPattern) != len(s[j].PathPattern) {
			return len(s[i].PathPattern) > len(s[j].PathPattern)
		}
		return s[i].Version > s[j].Version
	})
}

func samplePageRule(host string) *storage.ParsingRuleRecord {
	return &storage.ParsingRuleRecord{
		ID:          1,
		SourceName:  "test",
		HostPattern: host,
		TargetType:  storage.TargetTypePage,
		Version:     1,
		Enabled:     true,
		Selectors: storage.SelectorMap{
			Title:       &storage.FieldSelector{CSS: "h1"},
			MainContent: &storage.FieldSelector{CSS: "article p", Multi: true},
		},
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Resolve / ResolveByURL
// ─────────────────────────────────────────────────────────────────────────────

func TestResolver_ResolveByURL_Success(t *testing.T) {
	repo := &fakeRepo{rules: []*storage.ParsingRuleRecord{samplePageRule("news.example.com")}}
	r := rule.NewResolver(repo)

	got, err := r.ResolveByURL(context.Background(), "https://news.example.com/article/1", storage.TargetTypePage)
	require.NoError(t, err)
	assert.Equal(t, "news.example.com", got.HostPattern)
	assert.Equal(t, 1, repo.calls())
}

func TestResolver_ResolveByURL_HostUppercaseNormalized(t *testing.T) {
	repo := &fakeRepo{rules: []*storage.ParsingRuleRecord{samplePageRule("news.example.com")}}
	r := rule.NewResolver(repo)

	// URL host 가 대문자여도 lookup 은 lowercase 로 정규화되어 매칭
	_, err := r.ResolveByURL(context.Background(), "https://NEWS.EXAMPLE.COM/x", storage.TargetTypePage)
	require.NoError(t, err)
}

func TestResolver_ResolveByURL_InvalidURL_ReturnsError(t *testing.T) {
	repo := &fakeRepo{}
	r := rule.NewResolver(repo)

	_, err := r.ResolveByURL(context.Background(), "://no-scheme", storage.TargetTypePage)
	require.Error(t, err)
	var rerr *rule.Error
	require.ErrorAs(t, err, &rerr)
	assert.Equal(t, rule.ErrInvalidURL, rerr.Code)
	assert.Equal(t, 0, repo.calls(), "URL 검증 실패 시 repo 호출 안 함")
}

func TestResolver_NoMatch_ReturnsErrNoRule(t *testing.T) {
	repo := &fakeRepo{notFound: true}
	r := rule.NewResolver(repo)

	_, err := r.Resolve(context.Background(), "no-rule.example.com", "/", storage.TargetTypePage)
	require.Error(t, err)
	assert.True(t, errors.Is(err, &rule.Error{Code: rule.ErrNoRule}))
}

// ─────────────────────────────────────────────────────────────────────────────
// Cache 동작
// ─────────────────────────────────────────────────────────────────────────────

func TestResolver_Cache_HitsAvoidRepoCall(t *testing.T) {
	repo := &fakeRepo{rules: []*storage.ParsingRuleRecord{samplePageRule("news.example.com")}}
	r := rule.NewResolver(repo)

	// 첫 호출 → repo 1
	_, err := r.Resolve(context.Background(), "news.example.com", "/", storage.TargetTypePage)
	require.NoError(t, err)
	assert.Equal(t, 1, repo.calls())

	// 후속 호출 → cache hit, repo 호출 X
	for i := 0; i < 5; i++ {
		_, err := r.Resolve(context.Background(), "news.example.com", "/", storage.TargetTypePage)
		require.NoError(t, err)
	}
	assert.Equal(t, 1, repo.calls(), "양성 cache hit — 추가 repo 호출 없어야 함")
}

func TestResolver_NegativeCache_AvoidRepoCallForMissingHost(t *testing.T) {
	repo := &fakeRepo{notFound: true}
	r := rule.NewResolver(repo)

	for i := 0; i < 5; i++ {
		_, err := r.Resolve(context.Background(), "missing.example.com", "/", storage.TargetTypePage)
		require.Error(t, err)
	}
	assert.Equal(t, 1, repo.calls(), "negative cache 도 repo 폭주 회피")
}

func TestResolver_Invalidate_ForcesRepoCall(t *testing.T) {
	repo := &fakeRepo{rules: []*storage.ParsingRuleRecord{samplePageRule("news.example.com")}}
	r := rule.NewResolver(repo)

	_, err := r.Resolve(context.Background(), "news.example.com", "/", storage.TargetTypePage)
	require.NoError(t, err)
	_, err = r.Resolve(context.Background(), "news.example.com", "/", storage.TargetTypePage)
	require.NoError(t, err)
	assert.Equal(t, 1, repo.calls())

	r.Invalidate("news.example.com", storage.TargetTypePage)

	_, err = r.Resolve(context.Background(), "news.example.com", "/", storage.TargetTypePage)
	require.NoError(t, err)
	assert.Equal(t, 2, repo.calls(), "Invalidate 후 다음 호출은 repo 다시 도달")
}

func TestResolver_InvalidateAll_ClearsAllEntries(t *testing.T) {
	repo := &fakeRepo{rules: []*storage.ParsingRuleRecord{
		samplePageRule("a.example.com"),
		samplePageRule("b.example.com"),
	}}
	r := rule.NewResolver(repo)

	_, err := r.Resolve(context.Background(), "a.example.com", "/", storage.TargetTypePage)
	require.NoError(t, err)
	_, err = r.Resolve(context.Background(), "b.example.com", "/", storage.TargetTypePage)
	require.NoError(t, err)
	assert.Equal(t, 2, repo.calls())

	r.InvalidateAll()

	_, err = r.Resolve(context.Background(), "a.example.com", "/", storage.TargetTypePage)
	require.NoError(t, err)
	_, err = r.Resolve(context.Background(), "b.example.com", "/", storage.TargetTypePage)
	require.NoError(t, err)
	assert.Equal(t, 4, repo.calls())
}

func TestResolver_CacheTTL_ExpiresAfterDuration(t *testing.T) {
	repo := &fakeRepo{rules: []*storage.ParsingRuleRecord{samplePageRule("news.example.com")}}
	// TTL 50ms — 테스트 친화적 짧은 시간
	r := rule.NewResolver(repo, rule.WithCacheTTL(50*time.Millisecond))

	_, err := r.Resolve(context.Background(), "news.example.com", "/", storage.TargetTypePage)
	require.NoError(t, err)
	assert.Equal(t, 1, repo.calls())

	time.Sleep(80 * time.Millisecond)

	_, err = r.Resolve(context.Background(), "news.example.com", "/", storage.TargetTypePage)
	require.NoError(t, err)
	assert.Equal(t, 2, repo.calls(), "TTL 만료 후 repo 다시 호출")
}

func TestNewResolver_NilRepo_Panics(t *testing.T) {
	assert.PanicsWithValue(t, "rule: NewResolver requires non-nil repo", func() {
		rule.NewResolver(nil)
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Path pattern 매칭 (이슈 #173 단계 1)
// ─────────────────────────────────────────────────────────────────────────────

// path_pattern=” (catch-all) rule 만 있을 때 어떤 path 든 매칭됨 — 기존 동작 보존.
func TestResolver_PathPattern_EmptyMatchesAnyPath(t *testing.T) {
	repo := &fakeRepo{rules: []*storage.ParsingRuleRecord{samplePageRule("news.example.com")}}
	r := rule.NewResolver(repo)

	for _, path := range []string{"/", "/article/1", "/sports/breaking-news", "/about"} {
		got, err := r.Resolve(context.Background(), "news.example.com", path, storage.TargetTypePage)
		require.NoError(t, err, "path=%s", path)
		assert.Equal(t, "news.example.com", got.HostPattern, "path=%s", path)
	}
}

// 같은 host 에 path_pattern 이 여러 개일 때 더 구체적인 (긴) 패턴이 우선 매칭됨.
func TestResolver_PathPattern_LongerPatternWinsOverCatchAll(t *testing.T) {
	specific := samplePageRule("news.example.com")
	specific.ID = 2
	specific.Version = 2
	specific.PathPattern = "^/sports/.*$"
	specific.Selectors.Title = &storage.FieldSelector{CSS: "h1.sports-headline"}

	catchAll := samplePageRule("news.example.com") // PathPattern="" 기본
	catchAll.Selectors.Title = &storage.FieldSelector{CSS: "h1.default"}

	repo := &fakeRepo{rules: []*storage.ParsingRuleRecord{specific, catchAll}}
	r := rule.NewResolver(repo)

	// /sports/* path 는 specific rule 매칭 (LENGTH DESC 정렬상 우선)
	got, err := r.Resolve(context.Background(), "news.example.com", "/sports/breaking", storage.TargetTypePage)
	require.NoError(t, err)
	assert.Equal(t, "h1.sports-headline", got.Selectors.Title.CSS, "더 구체적인 path_pattern 우선")

	// /article/* path 는 catch-all 매칭
	got, err = r.Resolve(context.Background(), "news.example.com", "/article/123", storage.TargetTypePage)
	require.NoError(t, err)
	assert.Equal(t, "h1.default", got.Selectors.Title.CSS, "specific 미매칭 시 catch-all fallback")
}

// path_pattern 이 모두 매칭 안 되고 catch-all 도 없으면 ErrNoRule.
func TestResolver_PathPattern_NoMatchReturnsErrNoRule(t *testing.T) {
	specific := samplePageRule("news.example.com")
	specific.PathPattern = "^/sports/.*$"

	repo := &fakeRepo{rules: []*storage.ParsingRuleRecord{specific}}
	r := rule.NewResolver(repo)

	_, err := r.Resolve(context.Background(), "news.example.com", "/about", storage.TargetTypePage)
	require.Error(t, err)
	assert.True(t, errors.Is(err, &rule.Error{Code: rule.ErrNoRule}))
}

// 잘못된 regex 패턴은 매칭 실패로 간주 (compileRegex negative cache).
// Resolver 가 panic 하지 않고 다음 후보로 넘어감.
func TestResolver_PathPattern_InvalidRegexFallsThrough(t *testing.T) {
	bad := samplePageRule("news.example.com")
	bad.ID = 2
	bad.Version = 2
	bad.PathPattern = "[unclosed-bracket" // 컴파일 실패

	catchAll := samplePageRule("news.example.com") // PathPattern=""
	catchAll.Selectors.Title = &storage.FieldSelector{CSS: "h1.default"}

	repo := &fakeRepo{rules: []*storage.ParsingRuleRecord{bad, catchAll}}
	r := rule.NewResolver(repo)

	got, err := r.Resolve(context.Background(), "news.example.com", "/article/1", storage.TargetTypePage)
	require.NoError(t, err)
	assert.Equal(t, "h1.default", got.Selectors.Title.CSS, "잘못된 regex 는 skip 되고 catch-all fallback")
}

// 같은 패턴 여러 host 에 사용해도 컴파일은 1회 (regexCache 동작).
// 직접 카운터 노출 안 됨 — 동일 패턴으로 다중 lookup 시 정상 동작 + 회귀 없음 검증.
func TestResolver_PathPattern_RegexCacheReuseAcrossHosts(t *testing.T) {
	pattern := "^/news/[0-9]+$"

	a := samplePageRule("a.example.com")
	a.PathPattern = pattern
	b := samplePageRule("b.example.com")
	b.PathPattern = pattern

	repo := &fakeRepo{rules: []*storage.ParsingRuleRecord{a, b}}
	r := rule.NewResolver(repo)

	got, err := r.Resolve(context.Background(), "a.example.com", "/news/123", storage.TargetTypePage)
	require.NoError(t, err)
	assert.Equal(t, "a.example.com", got.HostPattern)

	got, err = r.Resolve(context.Background(), "b.example.com", "/news/456", storage.TargetTypePage)
	require.NoError(t, err)
	assert.Equal(t, "b.example.com", got.HostPattern)
}

// ResolveByURL 이 host + path 모두 추출 + 매칭.
func TestResolver_ResolveByURL_PathPatternMatching(t *testing.T) {
	specific := samplePageRule("news.example.com")
	specific.PathPattern = "^/article/[0-9]+$"
	specific.Selectors.Title = &storage.FieldSelector{CSS: "h1.article"}

	repo := &fakeRepo{rules: []*storage.ParsingRuleRecord{specific}}
	r := rule.NewResolver(repo)

	got, err := r.ResolveByURL(context.Background(), "https://news.example.com/article/12345?utm=x", storage.TargetTypePage)
	require.NoError(t, err)
	assert.Equal(t, "h1.article", got.Selectors.Title.CSS, "URL.Path 가 path_pattern 매칭")
}
