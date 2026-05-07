package locks_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/locks"
	"issuetracker/internal/processor/fetcher/core"
)

// recordingLocker — TTL 기록 가능한 fakeRedisLocker variant. category vs article TTL 검증용.
type recordingLocker struct {
	mu      sync.Mutex
	keys    map[string]struct{}
	lastTTL time.Duration
}

func (r *recordingLocker) AcquireLock(_ context.Context, key string, ttl time.Duration) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lastTTL = ttl
	if _, ok := r.keys[key]; ok {
		return false, nil
	}
	r.keys[key] = struct{}{}
	return true, nil
}

func (r *recordingLocker) ReleaseLock(_ context.Context, key string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.keys, key)
	return nil
}

func (r *recordingLocker) ttl() time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastTTL
}

// TestPipelineGuard_CategoryUsesShortTTL 는 Category target 호출 시 short TTL 이 적용되는지 검증합니다 (이슈 #285).
func TestPipelineGuard_CategoryUsesShortTTL(t *testing.T) {
	rl := &recordingLocker{keys: make(map[string]struct{})}
	lock := locks.NewRedisIngestionLock(rl, 24*time.Hour)
	guard := locks.NewPipelineGuard(lock, 60*time.Second)

	acquired, err := guard.CheckAndAcquire(context.Background(), "https://example.com/category", core.TargetTypeCategory)
	require.NoError(t, err)
	assert.True(t, acquired)
	assert.Equal(t, 60*time.Second, rl.ttl(), "Category 는 단명 TTL 적용")
}

// TestPipelineGuard_ArticleUsesDefaultTTL 는 Article target 호출 시 IngestionLock default TTL (24h) 적용을 검증합니다.
func TestPipelineGuard_ArticleUsesDefaultTTL(t *testing.T) {
	rl := &recordingLocker{keys: make(map[string]struct{})}
	lock := locks.NewRedisIngestionLock(rl, 24*time.Hour)
	guard := locks.NewPipelineGuard(lock, 60*time.Second)

	acquired, err := guard.CheckAndAcquire(context.Background(), "https://example.com/article", core.TargetTypeArticle)
	require.NoError(t, err)
	assert.True(t, acquired)
	assert.Equal(t, 24*time.Hour, rl.ttl(), "Article 은 IngestionLock default TTL (24h) 적용")
}

// TestPipelineGuard_DuplicateAcquireReturnsFalse 는 같은 URL 두 번 acquire 시 두 번째는 false 반환을 검증합니다.
func TestPipelineGuard_DuplicateAcquireReturnsFalse(t *testing.T) {
	rl := &recordingLocker{keys: make(map[string]struct{})}
	lock := locks.NewRedisIngestionLock(rl, 24*time.Hour)
	guard := locks.NewPipelineGuard(lock, 60*time.Second)
	url := "https://example.com/x"

	acquired1, err := guard.CheckAndAcquire(context.Background(), url, core.TargetTypeCategory)
	require.NoError(t, err)
	assert.True(t, acquired1)

	acquired2, err := guard.CheckAndAcquire(context.Background(), url, core.TargetTypeCategory)
	require.NoError(t, err)
	assert.False(t, acquired2, "이미 marker 점유 — 두 번째는 false")
}

// TestPipelineGuard_ReleaseAllowsReacquire 는 Release 후 다시 acquire 가능한지 검증합니다.
func TestPipelineGuard_ReleaseAllowsReacquire(t *testing.T) {
	rl := &recordingLocker{keys: make(map[string]struct{})}
	lock := locks.NewRedisIngestionLock(rl, 24*time.Hour)
	guard := locks.NewPipelineGuard(lock, 60*time.Second)
	url := "https://example.com/x"

	_, err := guard.CheckAndAcquire(context.Background(), url, core.TargetTypeCategory)
	require.NoError(t, err)

	require.NoError(t, guard.Release(context.Background(), url))

	acquired, err := guard.CheckAndAcquire(context.Background(), url, core.TargetTypeCategory)
	require.NoError(t, err)
	assert.True(t, acquired, "Release 후 즉시 재진입 가능")
}

// TestPipelineGuard_NilLockFallsBackToNoop 는 lock=nil 주입 시 NoopIngestionLock 으로 fallback 되는지 검증합니다.
func TestPipelineGuard_NilLockFallsBackToNoop(t *testing.T) {
	guard := locks.NewPipelineGuard(nil, 60*time.Second)
	acquired, err := guard.CheckAndAcquire(context.Background(), "x", core.TargetTypeCategory)
	require.NoError(t, err)
	assert.True(t, acquired, "Noop fallback — 항상 acquired=true")
	require.NoError(t, guard.Release(context.Background(), "x"), "Noop fallback — Release 도 noop")
}

// TestPipelineGuard_ZeroTTLUsesDefault 는 categoryTTL=0 주입 시 DefaultCategoryTTL fallback 검증.
func TestPipelineGuard_ZeroTTLUsesDefault(t *testing.T) {
	rl := &recordingLocker{keys: make(map[string]struct{})}
	lock := locks.NewRedisIngestionLock(rl, 24*time.Hour)
	guard := locks.NewPipelineGuard(lock, 0)

	_, err := guard.CheckAndAcquire(context.Background(), "x", core.TargetTypeCategory)
	require.NoError(t, err)
	assert.Equal(t, locks.DefaultCategoryTTL, rl.ttl())
}
