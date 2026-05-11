package locks_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/locks"
	"issuetracker/pkg/logger"
)

// stubLock 는 ProcessingLock 의 in-memory 테스트 더블입니다 — 호출자가 Acquire 결과 / Release 동작을
// 미리 설정할 수 있고, 호출 횟수를 추적합니다.
type stubLock struct {
	mu            sync.Mutex
	acquireResult bool
	acquireErr    error
	releaseErr    error
	acquireCalls  int
	releaseCalls  int
	heldKeys      map[string]struct{}
}

func newStubLock(acquireResult bool) *stubLock {
	return &stubLock{
		acquireResult: acquireResult,
		heldKeys:      map[string]struct{}{},
	}
}

func (s *stubLock) Acquire(_ context.Context, key string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.acquireCalls++
	if s.acquireErr != nil {
		return false, s.acquireErr
	}
	if s.acquireResult {
		s.heldKeys[key] = struct{}{}
	}
	return s.acquireResult, nil
}

func (s *stubLock) Release(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.releaseCalls++
	delete(s.heldKeys, key)
	return s.releaseErr
}

func (s *stubLock) callCounts() (acquire, release int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.acquireCalls, s.releaseCalls
}

// ─────────────────────────────────────────────────────────────────────────────
// Semaphore 단위 테스트
// ─────────────────────────────────────────────────────────────────────────────

func TestSemaphore_AcquireRelease(t *testing.T) {
	t.Parallel()
	sem := locks.NewSemaphore(2)
	assert.Equal(t, 2, sem.Capacity())
	assert.Equal(t, 0, sem.InFlight())

	require.NoError(t, sem.Acquire(context.Background()))
	assert.Equal(t, 1, sem.InFlight())

	require.NoError(t, sem.Acquire(context.Background()))
	assert.Equal(t, 2, sem.InFlight())

	sem.Release()
	assert.Equal(t, 1, sem.InFlight())

	sem.Release()
	assert.Equal(t, 0, sem.InFlight())
}

func TestSemaphore_CapacityFullBlocksUntilRelease(t *testing.T) {
	t.Parallel()
	sem := locks.NewSemaphore(1)
	require.NoError(t, sem.Acquire(context.Background()))

	// 두 번째 Acquire 는 첫 release 까지 block. 별도 goroutine 에서 시도.
	done := make(chan struct{})
	go func() {
		_ = sem.Acquire(context.Background())
		close(done)
	}()

	// short wait — block 상태인지 확인.
	select {
	case <-done:
		t.Fatal("second Acquire returned without first Release")
	case <-time.After(20 * time.Millisecond):
	}

	sem.Release()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("second Acquire did not unblock after Release")
	}
	sem.Release()
}

func TestSemaphore_ContextCancelInterruptsAcquire(t *testing.T) {
	t.Parallel()
	sem := locks.NewSemaphore(1)
	require.NoError(t, sem.Acquire(context.Background()))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	err := sem.Acquire(ctx)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	sem.Release()
}

func TestSemaphore_PanicsOnZeroCapacity(t *testing.T) {
	t.Parallel()
	assert.Panics(t, func() { locks.NewSemaphore(0) })
	assert.Panics(t, func() { locks.NewSemaphore(-1) })
}

// ─────────────────────────────────────────────────────────────────────────────
// StageGate 단위 테스트
// ─────────────────────────────────────────────────────────────────────────────

func newTestLog(t *testing.T) *logger.Logger {
	t.Helper()
	return logger.New(logger.Config{Level: "error"})
}

func TestStageGate_Acquire_Success(t *testing.T) {
	t.Parallel()
	sem := locks.NewSemaphore(2)
	lk := newStubLock(true)
	gate := locks.NewStageGate(locks.StageFetcher, sem, lk, newTestLog(t))

	release, acquired, err := gate.Acquire(context.Background(), "https://example.com/a")
	require.NoError(t, err)
	require.True(t, acquired)
	require.NotNil(t, release)
	assert.Equal(t, 1, sem.InFlight())

	release()
	assert.Equal(t, 0, sem.InFlight())
	acq, rel := lk.callCounts()
	assert.Equal(t, 1, acq)
	assert.Equal(t, 1, rel)
}

func TestStageGate_LockAlreadyHeld_ReleasesSemaphore(t *testing.T) {
	t.Parallel()
	sem := locks.NewSemaphore(2)
	lk := newStubLock(false) // 다른 worker 가 lock 잡고 있음
	gate := locks.NewStageGate(locks.StageParser, sem, lk, newTestLog(t))

	release, acquired, err := gate.Acquire(context.Background(), "https://example.com/a")
	require.NoError(t, err)
	assert.False(t, acquired)
	assert.Nil(t, release)
	// semaphore slot 은 반납되어야 함 — 다른 worker 가 진입 가능해야 함.
	assert.Equal(t, 0, sem.InFlight(), "lock 미획득 시 semaphore 즉시 반납")
}

func TestStageGate_LockError_ReleasesSemaphore(t *testing.T) {
	t.Parallel()
	sem := locks.NewSemaphore(2)
	lk := newStubLock(false)
	lk.acquireErr = errors.New("redis down")
	gate := locks.NewStageGate(locks.StageValidator, sem, lk, newTestLog(t))

	release, acquired, err := gate.Acquire(context.Background(), "https://example.com/a")
	require.Error(t, err)
	assert.False(t, acquired)
	assert.Nil(t, release)
	assert.Equal(t, 0, sem.InFlight(), "lock 에러 시도 semaphore 반납")
}

func TestStageGate_SemaphoreFull_ContextTimeout(t *testing.T) {
	t.Parallel()
	sem := locks.NewSemaphore(1)
	lk := newStubLock(true)
	gate := locks.NewStageGate(locks.StageFetcher, sem, lk, newTestLog(t))

	// 첫 acquire — semaphore 1 점유.
	release1, acquired1, err := gate.Acquire(context.Background(), "https://example.com/a")
	require.NoError(t, err)
	require.True(t, acquired1)
	defer release1()

	// 둘째 acquire 는 semaphore full + ctx timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	release2, acquired2, err := gate.Acquire(ctx, "https://example.com/b")
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.False(t, acquired2)
	assert.Nil(t, release2)
}

func TestStageGate_ReleaseIdempotent(t *testing.T) {
	t.Parallel()
	sem := locks.NewSemaphore(2)
	lk := newStubLock(true)
	gate := locks.NewStageGate(locks.StageFetcher, sem, lk, newTestLog(t))

	release, acquired, err := gate.Acquire(context.Background(), "https://example.com/a")
	require.NoError(t, err)
	require.True(t, acquired)

	release()
	release() // 두 번째 호출 — panic 없이 무시되어야 함.
	release() // 세 번째도 idempotent.

	_, rel := lk.callCounts()
	assert.Equal(t, 1, rel, "lock.Release 는 정확히 1회만 호출")
	assert.Equal(t, 0, sem.InFlight(), "semaphore 도 정확히 1회 반납")
}

func TestStageGate_ConcurrentAcquireRespectsCapacity(t *testing.T) {
	t.Parallel()
	cap := 3
	sem := locks.NewSemaphore(cap)
	lk := newStubLock(true)
	gate := locks.NewStageGate(locks.StageFetcher, sem, lk, newTestLog(t))

	var (
		maxInFlight atomic.Int64
		wg          sync.WaitGroup
	)
	for i := 0; i < 20; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, acquired, err := gate.Acquire(context.Background(), "https://example.com/"+string(rune('a'+i)))
			if err != nil || !acquired {
				return
			}
			defer release()
			// peak 측정.
			cur := int64(sem.InFlight())
			for {
				m := maxInFlight.Load()
				if cur <= m || maxInFlight.CompareAndSwap(m, cur) {
					break
				}
			}
			time.Sleep(5 * time.Millisecond)
		}()
	}
	wg.Wait()
	assert.LessOrEqual(t, maxInFlight.Load(), int64(cap), "동시 in-flight 가 capacity 초과 안 함")
	assert.Equal(t, 0, sem.InFlight(), "모든 release 후 0")
}

func TestNoopStageGate_AlwaysAcquired(t *testing.T) {
	t.Parallel()
	gate := locks.NewNoopStageGate()
	release, acquired, err := gate.Acquire(context.Background(), "https://example.com/a")
	require.NoError(t, err)
	assert.True(t, acquired)
	require.NotNil(t, release)
	release() // panic 없어야 함.
	release() // idempotent (noop 은 항상 안전).
}

func TestNewStageGate_PanicsOnInvalidArgs(t *testing.T) {
	t.Parallel()
	sem := locks.NewSemaphore(1)
	lk := locks.NoopProcessingLock{}
	log := newTestLog(t)

	assert.Panics(t, func() { locks.NewStageGate("", sem, lk, log) })
	assert.Panics(t, func() { locks.NewStageGate(locks.StageFetcher, nil, lk, log) })
	assert.Panics(t, func() { locks.NewStageGate(locks.StageFetcher, sem, nil, log) })
	assert.Panics(t, func() { locks.NewStageGate(locks.StageFetcher, sem, lk, nil) })
}
