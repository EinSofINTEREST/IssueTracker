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

	"issuetracker/internal/processor/parser/rule"
	"issuetracker/internal/storage"
)

// fakeBlacklistRepo 는 in-memory BlacklistRepository — Matcher / decorator 단위 테스트용.
type fakeBlacklistRepo struct {
	rows         []*storage.BlacklistRecord
	findCalls    int64
	getByIDCalls int64
	findErr      error
	insertErr    error
	getByIDErr   error
	updateErr    error
	deleteErr    error
}

func (r *fakeBlacklistRepo) Insert(_ context.Context, rec *storage.BlacklistRecord) error {
	if r.insertErr != nil {
		return r.insertErr
	}
	rec.ID = int64(len(r.rows) + 1)
	r.rows = append(r.rows, rec)
	return nil
}

func (r *fakeBlacklistRepo) Update(_ context.Context, rec *storage.BlacklistRecord) error {
	if r.updateErr != nil {
		return r.updateErr
	}
	for _, existing := range r.rows {
		if existing.ID == rec.ID {
			existing.Reason = rec.Reason
			existing.Source = rec.Source
			existing.Enabled = rec.Enabled
			return nil
		}
	}
	return storage.ErrNotFound
}

func (r *fakeBlacklistRepo) Delete(_ context.Context, id int64) error {
	if r.deleteErr != nil {
		return r.deleteErr
	}
	out := r.rows[:0]
	for _, existing := range r.rows {
		if existing.ID != id {
			out = append(out, existing)
		}
	}
	r.rows = out
	return nil
}

func (r *fakeBlacklistRepo) GetByID(_ context.Context, id int64) (*storage.BlacklistRecord, error) {
	atomic.AddInt64(&r.getByIDCalls, 1)
	if r.getByIDErr != nil {
		return nil, r.getByIDErr
	}
	for _, existing := range r.rows {
		if existing.ID == id {
			return existing, nil
		}
	}
	return nil, storage.ErrNotFound
}

func (r *fakeBlacklistRepo) FindEnabledByHost(_ context.Context, host string) ([]*storage.BlacklistRecord, error) {
	atomic.AddInt64(&r.findCalls, 1)
	if r.findErr != nil {
		return nil, r.findErr
	}
	out := make([]*storage.BlacklistRecord, 0)
	for _, existing := range r.rows {
		if existing.HostPattern == host && existing.Enabled {
			out = append(out, existing)
		}
	}
	// postgres ORDER BY LENGTH(path_pattern) DESC, id DESC 시뮬레이션 — 더 구체적 path 우선.
	sort.SliceStable(out, func(i, j int) bool {
		if len(out[i].PathPattern) != len(out[j].PathPattern) {
			return len(out[i].PathPattern) > len(out[j].PathPattern)
		}
		return out[i].ID > out[j].ID
	})
	return out, nil
}

func (r *fakeBlacklistRepo) List(_ context.Context, _ storage.BlacklistFilter) ([]*storage.BlacklistRecord, error) {
	return r.rows, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// IsBlocked / Filter 매칭 동작
// ─────────────────────────────────────────────────────────────────────────────

func TestBlacklistMatcher_HostCatchAllBlocksAllPaths(t *testing.T) {
	repo := &fakeBlacklistRepo{rows: []*storage.BlacklistRecord{
		{ID: 1, HostPattern: "ads.example.com", PathPattern: "", Source: storage.BlacklistSourceManual, Enabled: true},
	}}
	m, err := rule.NewBlacklistMatcher(repo)
	require.NoError(t, err)

	for _, u := range []string{
		"https://ads.example.com/",
		"https://ads.example.com/anything",
		"https://ads.example.com/some/deep/path",
	} {
		blocked, err := m.IsBlocked(context.Background(), u)
		require.NoError(t, err)
		assert.True(t, blocked, "host catch-all 은 모든 path 차단: %s", u)
	}
}

func TestBlacklistMatcher_PathPatternBlocksMatchingOnly(t *testing.T) {
	repo := &fakeBlacklistRepo{rows: []*storage.BlacklistRecord{
		{ID: 1, HostPattern: "news.example.com", PathPattern: `^/promotion/.*$`, Source: storage.BlacklistSourceManual, Enabled: true},
	}}
	m, err := rule.NewBlacklistMatcher(repo)
	require.NoError(t, err)

	blocked, _ := m.IsBlocked(context.Background(), "https://news.example.com/promotion/123")
	assert.True(t, blocked, "정밀 path 는 매칭 path 만 차단")

	blocked, _ = m.IsBlocked(context.Background(), "https://news.example.com/article/123")
	assert.False(t, blocked, "정밀 path 는 미매칭 path 통과")
}

func TestBlacklistMatcher_DisabledRowsIgnored(t *testing.T) {
	repo := &fakeBlacklistRepo{rows: []*storage.BlacklistRecord{
		{ID: 1, HostPattern: "ads.example.com", PathPattern: "", Source: storage.BlacklistSourceManual, Enabled: false},
	}}
	m, _ := rule.NewBlacklistMatcher(repo)

	blocked, _ := m.IsBlocked(context.Background(), "https://ads.example.com/x")
	assert.False(t, blocked, "disabled row 는 무시")
}

func TestBlacklistMatcher_HostMismatchPasses(t *testing.T) {
	repo := &fakeBlacklistRepo{rows: []*storage.BlacklistRecord{
		{ID: 1, HostPattern: "ads.example.com", PathPattern: "", Source: storage.BlacklistSourceManual, Enabled: true},
	}}
	m, _ := rule.NewBlacklistMatcher(repo)

	blocked, _ := m.IsBlocked(context.Background(), "https://news.example.com/x")
	assert.False(t, blocked, "다른 host 는 통과")
}

func TestBlacklistMatcher_InvalidURLDoesNotBlock(t *testing.T) {
	repo := &fakeBlacklistRepo{}
	m, _ := rule.NewBlacklistMatcher(repo)

	blocked, err := m.IsBlocked(context.Background(), "::not a url")
	require.NoError(t, err)
	assert.False(t, blocked, "URL parse 실패는 안전하게 false (차단 안 함)")
}

func TestBlacklistMatcher_HostNormalizedLowercase(t *testing.T) {
	repo := &fakeBlacklistRepo{rows: []*storage.BlacklistRecord{
		{ID: 1, HostPattern: "ads.example.com", PathPattern: "", Source: storage.BlacklistSourceManual, Enabled: true},
	}}
	m, _ := rule.NewBlacklistMatcher(repo)

	blocked, _ := m.IsBlocked(context.Background(), "https://ADS.EXAMPLE.COM/x")
	assert.True(t, blocked, "host 는 lowercase 정규화 후 매칭")
}

func TestBlacklistMatcher_InvalidRegexFallsThrough(t *testing.T) {
	// 잘못된 regex 는 컴파일 실패 — 매칭 안 됨으로 처리, 다른 row 가 catch-all 이면 그쪽 적용.
	repo := &fakeBlacklistRepo{rows: []*storage.BlacklistRecord{
		{ID: 1, HostPattern: "news.example.com", PathPattern: "[unclosed", Source: storage.BlacklistSourceManual, Enabled: true},
		{ID: 2, HostPattern: "news.example.com", PathPattern: "", Source: storage.BlacklistSourceManual, Enabled: true},
	}}
	m, _ := rule.NewBlacklistMatcher(repo)

	blocked, _ := m.IsBlocked(context.Background(), "https://news.example.com/article/1")
	assert.True(t, blocked, "잘못된 regex 는 skip 되고 catch-all 로 차단")
}

func TestBlacklistMatcher_DBErrorPropagates(t *testing.T) {
	repo := &fakeBlacklistRepo{findErr: errors.New("db down")}
	m, _ := rule.NewBlacklistMatcher(repo)

	blocked, err := m.IsBlocked(context.Background(), "https://news.example.com/x")
	require.Error(t, err, "DB 에러는 호출자에게 전파")
	assert.False(t, blocked, "에러 시 차단 안 함 (안전 fallback)")
}

func TestBlacklistMatcher_Filter_RemovesBlockedURLs(t *testing.T) {
	repo := &fakeBlacklistRepo{rows: []*storage.BlacklistRecord{
		{ID: 1, HostPattern: "ads.example.com", PathPattern: "", Source: storage.BlacklistSourceManual, Enabled: true},
		{ID: 2, HostPattern: "news.example.com", PathPattern: `^/promotion/.*$`, Source: storage.BlacklistSourceManual, Enabled: true},
	}}
	m, _ := rule.NewBlacklistMatcher(repo)

	in := []string{
		"https://news.example.com/article/1",
		"https://ads.example.com/banner",
		"https://news.example.com/promotion/123",
		"https://news.example.com/article/2",
	}
	out := m.Filter(context.Background(), in)
	assert.Equal(t, []string{
		"https://news.example.com/article/1",
		"https://news.example.com/article/2",
	}, out)
}

func TestBlacklistMatcher_Filter_EmptyInputReturnsAsIs(t *testing.T) {
	repo := &fakeBlacklistRepo{}
	m, _ := rule.NewBlacklistMatcher(repo)

	out := m.Filter(context.Background(), nil)
	assert.Empty(t, out)
}

func TestBlacklistMatcher_Filter_DBErrorPassesThroughURL(t *testing.T) {
	repo := &fakeBlacklistRepo{findErr: errors.New("db down")}
	m, _ := rule.NewBlacklistMatcher(repo)

	in := []string{"https://news.example.com/article/1"}
	out := m.Filter(context.Background(), in)
	assert.Equal(t, in, out, "Filter 의 lookup 에러는 best-effort — 해당 URL 통과 (차단 안 함)")
}

// ─────────────────────────────────────────────────────────────────────────────
// Cache 동작
// ─────────────────────────────────────────────────────────────────────────────

func TestBlacklistMatcher_Cache_HitsAvoidRepoCall(t *testing.T) {
	repo := &fakeBlacklistRepo{rows: []*storage.BlacklistRecord{
		{ID: 1, HostPattern: "news.example.com", PathPattern: "", Source: storage.BlacklistSourceManual, Enabled: true},
	}}
	m, _ := rule.NewBlacklistMatcher(repo)

	for i := 0; i < 5; i++ {
		_, err := m.IsBlocked(context.Background(), "https://news.example.com/x")
		require.NoError(t, err)
	}
	assert.Equal(t, int64(1), atomic.LoadInt64(&repo.findCalls), "cache hit 후엔 repo 호출 0")
}

func TestBlacklistMatcher_NegativeCache_AvoidsRepoCallForMissingHost(t *testing.T) {
	repo := &fakeBlacklistRepo{}
	m, _ := rule.NewBlacklistMatcher(repo)

	for i := 0; i < 5; i++ {
		_, _ = m.IsBlocked(context.Background(), "https://nothere.example.com/x")
	}
	assert.Equal(t, int64(1), atomic.LoadInt64(&repo.findCalls), "negative cache 도 hit")
}

func TestBlacklistMatcher_Invalidate_ForcesRepoCall(t *testing.T) {
	repo := &fakeBlacklistRepo{rows: []*storage.BlacklistRecord{
		{ID: 1, HostPattern: "news.example.com", PathPattern: "", Source: storage.BlacklistSourceManual, Enabled: true},
	}}
	m, _ := rule.NewBlacklistMatcher(repo)

	_, _ = m.IsBlocked(context.Background(), "https://news.example.com/x")
	_, _ = m.IsBlocked(context.Background(), "https://news.example.com/x")
	require.Equal(t, int64(1), atomic.LoadInt64(&repo.findCalls))

	m.Invalidate("news.example.com")
	_, _ = m.IsBlocked(context.Background(), "https://news.example.com/x")
	assert.Equal(t, int64(2), atomic.LoadInt64(&repo.findCalls), "Invalidate 후 다음 lookup 은 repo 재호출")
}

func TestBlacklistMatcher_InvalidateAll_ClearsCache(t *testing.T) {
	repo := &fakeBlacklistRepo{rows: []*storage.BlacklistRecord{
		{ID: 1, HostPattern: "a.example.com", PathPattern: "", Source: storage.BlacklistSourceManual, Enabled: true},
		{ID: 2, HostPattern: "b.example.com", PathPattern: "", Source: storage.BlacklistSourceManual, Enabled: true},
	}}
	m, _ := rule.NewBlacklistMatcher(repo)

	_, _ = m.IsBlocked(context.Background(), "https://a.example.com/x")
	_, _ = m.IsBlocked(context.Background(), "https://b.example.com/x")
	require.Equal(t, int64(2), atomic.LoadInt64(&repo.findCalls))

	m.InvalidateAll()
	_, _ = m.IsBlocked(context.Background(), "https://a.example.com/x")
	_, _ = m.IsBlocked(context.Background(), "https://b.example.com/x")
	assert.Equal(t, int64(4), atomic.LoadInt64(&repo.findCalls))
}

func TestBlacklistMatcher_NewWithNilRepo_Errors(t *testing.T) {
	_, err := rule.NewBlacklistMatcher(nil)
	require.Error(t, err)
}

// ─────────────────────────────────────────────────────────────────────────────
// Classify (이슈 #297) — mode 별 분류
// ─────────────────────────────────────────────────────────────────────────────

func TestBlacklistMatcher_Classify_DropMatch_ExcludedFromAllSlices(t *testing.T) {
	repo := &fakeBlacklistRepo{rows: []*storage.BlacklistRecord{
		{ID: 1, HostPattern: "ads.example.com", PathPattern: "", Mode: storage.BlacklistModeDrop, Enabled: true},
	}}
	m, _ := rule.NewBlacklistMatcher(repo)

	in := []string{
		"https://news.example.com/article/1",
		"https://ads.example.com/banner",
	}
	d := m.Classify(context.Background(), in)
	assert.Equal(t, []string{"https://news.example.com/article/1"}, d.Allowed)
	assert.Empty(t, d.ExtractLinksOnly, "drop 매칭은 ExtractLinksOnly 에 들어가지 않음")
}

func TestBlacklistMatcher_Classify_ExtractLinksOnly_RoutedToOwnSlice(t *testing.T) {
	repo := &fakeBlacklistRepo{rows: []*storage.BlacklistRecord{
		{ID: 1, HostPattern: "sitemap.example.com", PathPattern: "", Mode: storage.BlacklistModeExtractLinksOnly, Enabled: true},
	}}
	m, _ := rule.NewBlacklistMatcher(repo)

	in := []string{
		"https://news.example.com/article/1",
		"https://sitemap.example.com/index",
	}
	d := m.Classify(context.Background(), in)
	assert.Equal(t, []string{"https://news.example.com/article/1"}, d.Allowed)
	assert.Equal(t, []string{"https://sitemap.example.com/index"}, d.ExtractLinksOnly,
		"extract_links_only 매칭은 별도 슬라이스로 라우팅")
}

func TestBlacklistMatcher_Classify_MixedModes(t *testing.T) {
	repo := &fakeBlacklistRepo{rows: []*storage.BlacklistRecord{
		{ID: 1, HostPattern: "ads.example.com", PathPattern: "", Mode: storage.BlacklistModeDrop, Enabled: true},
		{ID: 2, HostPattern: "sitemap.example.com", PathPattern: "", Mode: storage.BlacklistModeExtractLinksOnly, Enabled: true},
	}}
	m, _ := rule.NewBlacklistMatcher(repo)

	in := []string{
		"https://news.example.com/a",
		"https://ads.example.com/banner",
		"https://sitemap.example.com/i",
		"https://news.example.com/b",
	}
	d := m.Classify(context.Background(), in)
	assert.Equal(t, []string{"https://news.example.com/a", "https://news.example.com/b"}, d.Allowed)
	assert.Equal(t, []string{"https://sitemap.example.com/i"}, d.ExtractLinksOnly)
}

func TestBlacklistMatcher_Classify_LookupError_FallbackToAllowed(t *testing.T) {
	repo := &fakeBlacklistRepo{findErr: errors.New("db down")}
	m, _ := rule.NewBlacklistMatcher(repo)

	in := []string{"https://news.example.com/x"}
	d := m.Classify(context.Background(), in)
	assert.Equal(t, in, d.Allowed, "lookup 에러는 best-effort — Allowed 통과")
	assert.Empty(t, d.ExtractLinksOnly)
}

// 같은 host 에 path 다른 두 row — LENGTH(path_pattern) DESC 정렬상 더 구체적 path 의 mode 채택.
func TestBlacklistMatcher_Classify_LongerPathPatternModeWins(t *testing.T) {
	repo := &fakeBlacklistRepo{rows: []*storage.BlacklistRecord{
		// catch-all (path="") — 'extract_links_only'
		{ID: 1, HostPattern: "site.example.com", PathPattern: "", Mode: storage.BlacklistModeExtractLinksOnly, Enabled: true},
		// 정밀 path — 'drop'
		{ID: 2, HostPattern: "site.example.com", PathPattern: `^/promotion/.*$`, Mode: storage.BlacklistModeDrop, Enabled: true},
	}}
	m, _ := rule.NewBlacklistMatcher(repo)

	in := []string{
		"https://site.example.com/promotion/123", // 정밀 매칭 → drop
		"https://site.example.com/about/team",    // 미매칭 → catch-all → extract_links_only
	}
	d := m.Classify(context.Background(), in)
	assert.Empty(t, d.Allowed, "둘 다 blacklist 어딘가 매칭")
	assert.Equal(t, []string{"https://site.example.com/about/team"}, d.ExtractLinksOnly,
		"정밀 path 미매칭 시 catch-all 의 mode (extract_links_only) 채택")
}

// 이슈 #295: Cache TTL 짧게 설정 후 만료 후 재fetch 검증.
func TestBlacklistMatcher_CacheTTL_ExpiresAfterDuration(t *testing.T) {
	repo := &fakeBlacklistRepo{rows: []*storage.BlacklistRecord{
		{ID: 1, HostPattern: "news.example.com", PathPattern: "", Source: storage.BlacklistSourceManual, Enabled: true},
	}}
	m, _ := rule.NewBlacklistMatcher(repo,
		rule.WithBlacklistCacheTTL(50*time.Millisecond),
	)

	_, _ = m.IsBlocked(context.Background(), "https://news.example.com/x")
	_, _ = m.IsBlocked(context.Background(), "https://news.example.com/y")
	require.Equal(t, int64(1), atomic.LoadInt64(&repo.findCalls))

	time.Sleep(80 * time.Millisecond)
	_, _ = m.IsBlocked(context.Background(), "https://news.example.com/z")
	assert.Equal(t, int64(2), atomic.LoadInt64(&repo.findCalls), "TTL 만료 후 재fetch")
}
