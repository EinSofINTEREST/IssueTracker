package worker_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"issuetracker/internal/crawler/worker"
)

// fakeRedisLocker — 테스트용 in-memory SETNX 시뮬레이션.
//
// AcquireLock(key, ttl): key 가 처음이면 acquired=true, 같은 key 재호출은 acquired=false.
// ReleaseLock(key): key 제거 (다음 Acquire 가 다시 acquired=true).
//
// failNext 가 set 되어 있으면 1회 에러 반환 (graceful degrade 동작 검증).
type fakeRedisLocker struct {
	mu      sync.Mutex
	keys    map[string]struct{}
	calls   int
	failErr error
}

func newFakeRedisLocker() *fakeRedisLocker {
	return &fakeRedisLocker{keys: make(map[string]struct{})}
}

func (f *fakeRedisLocker) AcquireLock(_ context.Context, key string, _ time.Duration) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.failErr != nil {
		err := f.failErr
		f.failErr = nil
		return false, err
	}
	if _, exists := f.keys[key]; exists {
		return false, nil
	}
	f.keys[key] = struct{}{}
	return true, nil
}

func (f *fakeRedisLocker) ReleaseLock(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.keys, key)
	return nil
}

func (f *fakeRedisLocker) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// ─────────────────────────────────────────────────────────────────────────────
// Tests
// ─────────────────────────────────────────────────────────────────────────────

func TestRedisIngestionLock_AcquireReturnsTrueOnFirstCall(t *testing.T) {
	r := newFakeRedisLocker()
	lock := worker.NewRedisIngestionLock(r, time.Hour)

	acquired, err := lock.Acquire(context.Background(), "https://example.com/article/1")
	assert.NoError(t, err)
	assert.True(t, acquired, "최초 진입은 acquired=true")
}

func TestRedisIngestionLock_AcquireReturnsFalseOnDuplicate(t *testing.T) {
	r := newFakeRedisLocker()
	lock := worker.NewRedisIngestionLock(r, time.Hour)

	first, _ := lock.Acquire(context.Background(), "https://example.com/x")
	second, _ := lock.Acquire(context.Background(), "https://example.com/x")
	assert.True(t, first)
	assert.False(t, second, "동일 URL 두 번째 acquire 는 false")
}

func TestRedisIngestionLock_DifferentURLsIndependent(t *testing.T) {
	r := newFakeRedisLocker()
	lock := worker.NewRedisIngestionLock(r, time.Hour)

	a, _ := lock.Acquire(context.Background(), "https://example.com/a")
	b, _ := lock.Acquire(context.Background(), "https://example.com/b")
	assert.True(t, a)
	assert.True(t, b, "서로 다른 URL 은 독립")
}

func TestRedisIngestionLock_InvalidateAllowsReacquire(t *testing.T) {
	r := newFakeRedisLocker()
	lock := worker.NewRedisIngestionLock(r, time.Hour)

	_, _ = lock.Acquire(context.Background(), "https://example.com/x")
	err := lock.Invalidate(context.Background(), "https://example.com/x")
	assert.NoError(t, err)

	acquired, _ := lock.Acquire(context.Background(), "https://example.com/x")
	assert.True(t, acquired, "Invalidate 후 다시 acquired=true")
}

func TestRedisIngestionLock_PropagatesError(t *testing.T) {
	r := newFakeRedisLocker()
	r.failErr = errors.New("redis unavailable")
	lock := worker.NewRedisIngestionLock(r, time.Hour)

	acquired, err := lock.Acquire(context.Background(), "https://example.com/x")
	assert.Error(t, err)
	assert.False(t, acquired, "에러 시 acquired=false (호출자가 fail-open 결정)")
}

func TestRedisIngestionLock_DefaultTTLOnZero(t *testing.T) {
	r := newFakeRedisLocker()
	// ttl 0 → DefaultIngestionLockTTL (24h) 사용 — panic 없이 동작 확인
	assert.NotPanics(t, func() {
		_ = worker.NewRedisIngestionLock(r, 0)
	})
}

// nil receiver / nil locker 보호 — zero-value 또는 NewRedisIngestionLock(nil, ...) 호출 시
// panic 없이 에러 반환 (PR #179 CodeRabbit 피드백).
func TestRedisIngestionLock_NilLockerReturnsError(t *testing.T) {
	lock := worker.NewRedisIngestionLock(nil, time.Hour)
	acquired, err := lock.Acquire(context.Background(), "https://example.com/x")
	assert.Error(t, err)
	assert.False(t, acquired)

	err = lock.Invalidate(context.Background(), "https://example.com/x")
	assert.Error(t, err)
}

func TestRedisIngestionLock_NilReceiverReturnsError(t *testing.T) {
	var lock *worker.RedisIngestionLock // zero value
	acquired, err := lock.Acquire(context.Background(), "https://example.com/x")
	assert.Error(t, err)
	assert.False(t, acquired)

	err = lock.Invalidate(context.Background(), "https://example.com/x")
	assert.Error(t, err)
}

func TestNoopIngestionLock_AlwaysAcquires(t *testing.T) {
	lock := worker.NoopIngestionLock{}
	acquired, err := lock.Acquire(context.Background(), "https://example.com/x")
	assert.NoError(t, err)
	assert.True(t, acquired, "Noop 은 항상 acquired=true (단일 인스턴스 fallback)")
}

func TestNoopIngestionLock_InvalidateNoOp(t *testing.T) {
	lock := worker.NoopIngestionLock{}
	err := lock.Invalidate(context.Background(), "https://example.com/x")
	assert.NoError(t, err)
}

// 동일 URL 의 동시 acquire 가 정확히 1회만 acquired=true 를 반환하는지 race 검증.
// fakeRedisLocker 가 mutex 보호하므로 SETNX semantics 시뮬레이션 정확.
func TestRedisIngestionLock_ConcurrentAcquireSingleWinner(t *testing.T) {
	r := newFakeRedisLocker()
	lock := worker.NewRedisIngestionLock(r, time.Hour)

	const concurrency = 50
	var winners int
	var mu sync.Mutex
	var wg sync.WaitGroup

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			acquired, _ := lock.Acquire(context.Background(), "https://example.com/x")
			if acquired {
				mu.Lock()
				winners++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	assert.Equal(t, 1, winners, "동시 acquire 50회 중 정확히 1회만 winner — SETNX 의 atomic 보장")
	assert.Equal(t, concurrency, r.callCount())
}

// sha256 키가 동일 URL 에 대해 deterministic 한지 (ingestionKey 가 export 안 되어 있어도
// 같은 URL 은 같은 SETNX 결과 — 두 번째 acquire false 를 통해 간접 검증).
func TestRedisIngestionLock_SameURLProducesSameKey(t *testing.T) {
	r := newFakeRedisLocker()
	lock := worker.NewRedisIngestionLock(r, time.Hour)

	url := strings.Repeat("https://example.com/very/long/path/", 20) // 720+ chars
	first, _ := lock.Acquire(context.Background(), url)
	second, _ := lock.Acquire(context.Background(), url)
	assert.True(t, first)
	assert.False(t, second, "긴 URL 도 sha256 기반 키 일관 — 두 번째 acquire 는 false")
}
