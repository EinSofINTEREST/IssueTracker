package worker_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/processor/fetcher/worker"
)

// TestNewSemaphore_BadCapacity_ReturnsError:
// 이슈 #208 panic-on-nil → error 정책. capacity <= 0 거부.
func TestNewSemaphore_BadCapacity_ReturnsError(t *testing.T) {
	_, err := worker.NewSemaphore(0)
	assert.Error(t, err)
	_, err = worker.NewSemaphore(-1)
	assert.Error(t, err)
}

// TestSemaphore_Capacity_Reports:
// Capacity() 가 생성 시 받은 값과 일치.
func TestSemaphore_Capacity_Reports(t *testing.T) {
	s, err := worker.NewSemaphore(5)
	require.NoError(t, err)
	assert.Equal(t, 5, s.Capacity())
}

// TestSemaphore_Acquire_BlocksUntilRelease:
// capacity=2 이면 3번째 Acquire 는 첫 두 Release 중 하나가 일어나야 통과.
func TestSemaphore_Acquire_BlocksUntilRelease(t *testing.T) {
	s, err := worker.NewSemaphore(2)
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, s.Acquire(ctx))
	require.NoError(t, s.Acquire(ctx))

	// 3번째 Acquire 는 timeout context 로 차단 검증
	timeoutCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	err = s.Acquire(timeoutCtx)
	assert.Error(t, err, "3번째 Acquire 는 슬롯 부족으로 timeout")

	// Release 후 Acquire 통과
	require.NoError(t, s.Release())
	require.NoError(t, s.Acquire(ctx))
	require.NoError(t, s.Release())
	require.NoError(t, s.Release())
}

// TestSemaphore_Concurrent_RespectsCapacity:
// goroutine N (>capacity) 동시 Acquire 시 동시 점유 수가 capacity 초과 안 함.
func TestSemaphore_Concurrent_RespectsCapacity(t *testing.T) {
	const capacity = 3
	const goroutines = 20
	s, err := worker.NewSemaphore(capacity)
	require.NoError(t, err)

	var (
		current atomic.Int32
		maxSeen atomic.Int32
		wg      sync.WaitGroup
	)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			require.NoError(t, s.Acquire(context.Background()))
			defer func() {
				if err := s.Release(); err != nil {
					t.Errorf("release failed: %v", err)
				}
			}()

			n := current.Add(1)
			defer current.Add(-1)

			// max 추적
			for {
				m := maxSeen.Load()
				if n <= m || maxSeen.CompareAndSwap(m, n) {
					break
				}
			}

			time.Sleep(10 * time.Millisecond)
		}()
	}

	wg.Wait()
	assert.LessOrEqual(t, int(maxSeen.Load()), capacity, "동시 점유 수가 capacity 초과 안 됨")
	assert.GreaterOrEqual(t, int(maxSeen.Load()), 1)
}

// TestSemaphore_Acquire_ContextCanceled_ReturnsError:
// Acquire 대기 중 ctx cancel → ctx.Err 반환 + 슬롯 점유 안 함.
func TestSemaphore_Acquire_ContextCanceled_ReturnsError(t *testing.T) {
	s, err := worker.NewSemaphore(1)
	require.NoError(t, err)

	require.NoError(t, s.Acquire(context.Background()))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = s.Acquire(ctx)
	assert.ErrorIs(t, err, context.Canceled)

	// 점유 안 됐으니 Release 1번만 가능.
	require.NoError(t, s.Release())
}

// TestSemaphore_Release_WithoutAcquire_ReturnsError:
// 매칭 Acquire 없이 Release 호출 시 ErrReleaseWithoutAcquire — production panic 금지 (이슈 #208).
func TestSemaphore_Release_WithoutAcquire_ReturnsError(t *testing.T) {
	s, err := worker.NewSemaphore(2)
	require.NoError(t, err)

	err = s.Release()
	assert.ErrorIs(t, err, worker.ErrReleaseWithoutAcquire)
}
