package rule_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/crawler/parser/rule"
	"issuetracker/internal/storage"
)

// fakeRepo 는 in-memory ParsingRuleRepository 구현입니다.
// FindActive 호출 횟수를 atomic 으로 추적하여 cache 동작을 검증합니다.
type fakeRepo struct {
	rules           []*storage.ParsingRuleRecord // FindActive 매칭 후보
	notFound        bool                         // true 면 항상 ErrNotFound
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

func (r *fakeRepo) FindActive(_ context.Context, host string, t storage.TargetType) (*storage.ParsingRuleRecord, error) {
	atomic.AddInt64(&r.findActiveCalls, 1)
	if r.notFound {
		return nil, storage.ErrNotFound
	}
	for _, rec := range r.rules {
		if rec.HostPattern == host && rec.TargetType == t {
			return rec, nil
		}
	}
	return nil, storage.ErrNotFound
}

func (r *fakeRepo) calls() int { return int(atomic.LoadInt64(&r.findActiveCalls)) }

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

	_, err := r.Resolve(context.Background(), "no-rule.example.com", storage.TargetTypePage)
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
	_, err := r.Resolve(context.Background(), "news.example.com", storage.TargetTypePage)
	require.NoError(t, err)
	assert.Equal(t, 1, repo.calls())

	// 후속 호출 → cache hit, repo 호출 X
	for i := 0; i < 5; i++ {
		_, err := r.Resolve(context.Background(), "news.example.com", storage.TargetTypePage)
		require.NoError(t, err)
	}
	assert.Equal(t, 1, repo.calls(), "양성 cache hit — 추가 repo 호출 없어야 함")
}

func TestResolver_NegativeCache_AvoidRepoCallForMissingHost(t *testing.T) {
	repo := &fakeRepo{notFound: true}
	r := rule.NewResolver(repo)

	for i := 0; i < 5; i++ {
		_, err := r.Resolve(context.Background(), "missing.example.com", storage.TargetTypePage)
		require.Error(t, err)
	}
	assert.Equal(t, 1, repo.calls(), "negative cache 도 repo 폭주 회피")
}

func TestResolver_Invalidate_ForcesRepoCall(t *testing.T) {
	repo := &fakeRepo{rules: []*storage.ParsingRuleRecord{samplePageRule("news.example.com")}}
	r := rule.NewResolver(repo)

	_, _ = r.Resolve(context.Background(), "news.example.com", storage.TargetTypePage)
	_, _ = r.Resolve(context.Background(), "news.example.com", storage.TargetTypePage)
	assert.Equal(t, 1, repo.calls())

	r.Invalidate("news.example.com", storage.TargetTypePage)

	_, _ = r.Resolve(context.Background(), "news.example.com", storage.TargetTypePage)
	assert.Equal(t, 2, repo.calls(), "Invalidate 후 다음 호출은 repo 다시 도달")
}

func TestResolver_InvalidateAll_ClearsAllEntries(t *testing.T) {
	repo := &fakeRepo{rules: []*storage.ParsingRuleRecord{
		samplePageRule("a.example.com"),
		samplePageRule("b.example.com"),
	}}
	r := rule.NewResolver(repo)

	_, _ = r.Resolve(context.Background(), "a.example.com", storage.TargetTypePage)
	_, _ = r.Resolve(context.Background(), "b.example.com", storage.TargetTypePage)
	assert.Equal(t, 2, repo.calls())

	r.InvalidateAll()

	_, _ = r.Resolve(context.Background(), "a.example.com", storage.TargetTypePage)
	_, _ = r.Resolve(context.Background(), "b.example.com", storage.TargetTypePage)
	assert.Equal(t, 4, repo.calls())
}

func TestResolver_CacheTTL_ExpiresAfterDuration(t *testing.T) {
	repo := &fakeRepo{rules: []*storage.ParsingRuleRecord{samplePageRule("news.example.com")}}
	// TTL 50ms — 테스트 친화적 짧은 시간
	r := rule.NewResolver(repo, rule.WithCacheTTL(50*time.Millisecond))

	_, _ = r.Resolve(context.Background(), "news.example.com", storage.TargetTypePage)
	assert.Equal(t, 1, repo.calls())

	time.Sleep(80 * time.Millisecond)

	_, _ = r.Resolve(context.Background(), "news.example.com", storage.TargetTypePage)
	assert.Equal(t, 2, repo.calls(), "TTL 만료 후 repo 다시 호출")
}

func TestNewResolver_NilRepo_Panics(t *testing.T) {
	assert.PanicsWithValue(t, "rule: NewResolver requires non-nil repo", func() {
		rule.NewResolver(nil)
	})
}
