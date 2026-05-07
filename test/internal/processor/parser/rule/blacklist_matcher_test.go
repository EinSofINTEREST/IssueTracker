package rule_test

import (
	"context"
	"errors"
	"sync"
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

// ─────────────────────────────────────────────────────────────────────────────
// invalidatingBlacklistRepo decorator
// ─────────────────────────────────────────────────────────────────────────────

type recordingBlacklistInvalidator struct {
	mu    sync.Mutex
	hosts []string
}

func (r *recordingBlacklistInvalidator) Invalidate(host string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hosts = append(r.hosts, host)
}

func (r *recordingBlacklistInvalidator) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.hosts))
	copy(out, r.hosts)
	return out
}

func TestInvalidatingBlacklistRepo_Insert_Success_Invalidates(t *testing.T) {
	inner := &fakeBlacklistRepo{}
	inv := &recordingBlacklistInvalidator{}
	repo := rule.WrapBlacklistWithInvalidator(inner, inv)

	err := repo.Insert(context.Background(), &storage.BlacklistRecord{
		HostPattern: "ads.example.com", Source: storage.BlacklistSourceManual, Enabled: true,
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"ads.example.com"}, inv.snapshot())
}

func TestInvalidatingBlacklistRepo_Insert_DuplicateAlsoInvalidates(t *testing.T) {
	inner := &fakeBlacklistRepo{insertErr: storage.ErrDuplicate}
	inv := &recordingBlacklistInvalidator{}
	repo := rule.WrapBlacklistWithInvalidator(inner, inv)

	err := repo.Insert(context.Background(), &storage.BlacklistRecord{
		HostPattern: "ads.example.com", Source: storage.BlacklistSourceManual, Enabled: true,
	})
	require.ErrorIs(t, err, storage.ErrDuplicate)
	assert.Equal(t, []string{"ads.example.com"}, inv.snapshot(), "ErrDuplicate 도 invalidate")
}

func TestInvalidatingBlacklistRepo_Update_Success_Invalidates(t *testing.T) {
	inner := &fakeBlacklistRepo{rows: []*storage.BlacklistRecord{
		{ID: 1, HostPattern: "ads.example.com", Enabled: true},
	}}
	inv := &recordingBlacklistInvalidator{}
	repo := rule.WrapBlacklistWithInvalidator(inner, inv)

	err := repo.Update(context.Background(), &storage.BlacklistRecord{
		ID: 1, HostPattern: "ads.example.com", Reason: "updated", Enabled: false,
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"ads.example.com"}, inv.snapshot())
}

func TestInvalidatingBlacklistRepo_Delete_PrefetchesAndInvalidates(t *testing.T) {
	inner := &fakeBlacklistRepo{rows: []*storage.BlacklistRecord{
		{ID: 1, HostPattern: "ads.example.com", Enabled: true},
	}}
	inv := &recordingBlacklistInvalidator{}
	repo := rule.WrapBlacklistWithInvalidator(inner, inv)

	err := repo.Delete(context.Background(), 1)
	require.NoError(t, err)
	assert.Equal(t, []string{"ads.example.com"}, inv.snapshot())
}

func TestInvalidatingBlacklistRepo_Delete_PrefetchFails_NoInvalidate(t *testing.T) {
	inner := &fakeBlacklistRepo{getByIDErr: errors.New("db down")}
	inv := &recordingBlacklistInvalidator{}
	repo := rule.WrapBlacklistWithInvalidator(inner, inv)

	err := repo.Delete(context.Background(), 1)
	require.NoError(t, err, "Delete 자체는 성공할 수 있음")
	assert.Empty(t, inv.snapshot(), "pre-fetch 실패 시 invalidate skip — TTL fallback")
}

func TestInvalidatingBlacklistRepo_NilInvalidator_NoOp(t *testing.T) {
	inner := &fakeBlacklistRepo{}
	repo := rule.WrapBlacklistWithInvalidator(inner, nil)

	err := repo.Insert(context.Background(), &storage.BlacklistRecord{
		HostPattern: "ads.example.com", Source: storage.BlacklistSourceManual, Enabled: true,
	})
	require.NoError(t, err)
	// nil invalidator 일 때 panic 없이 통과만 검증.
}

func TestBlacklistMatcher_NewWithNilRepo_Errors(t *testing.T) {
	_, err := rule.NewBlacklistMatcher(nil)
	require.Error(t, err)
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
