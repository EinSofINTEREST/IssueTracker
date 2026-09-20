package locks_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/locks"
)

// fakeRedis 는 토큰 시맨틱을 가진 최소 Redis 대역입니다.
//
// SET NX (값=토큰) 과 compare-and-delete 만 구현 — 이슈 #63 의 경쟁 상태를 TTL 실시간 대기 없이
// 재현하기 위해 Expire() 로 만료를 수동 트리거합니다.
type fakeRedis struct {
	mu   sync.Mutex
	vals map[string]string
}

func newFakeRedis() *fakeRedis { return &fakeRedis{vals: map[string]string{}} }

func (f *fakeRedis) AcquireLockWithToken(_ context.Context, key string, _ time.Duration) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, exists := f.vals[key]; exists {
		return "", false, nil
	}
	token := "tok-" + key + "-" + time.Now().Format("150405.000000000")
	f.vals[key] = token
	return token, true, nil
}

func (f *fakeRedis) ReleaseLockOwned(_ context.Context, key, token string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if cur, ok := f.vals[key]; ok && cur == token {
		delete(f.vals, key)
		return true, nil
	}
	return false, nil
}

// Expire 는 TTL 만료를 흉내 냅니다.
func (f *fakeRedis) Expire(key string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.vals, key)
}

func (f *fakeRedis) held(key string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.vals[key]
	return v, ok
}

// 이슈 #63 의 핵심 재현 — TTL 만료 후 타 인스턴스가 재획득한 락을 원 소유자가 지우면 안 된다.
func TestRedisProcessingLock_ExpiredThenReacquired_OriginalOwnerDoesNotDelete(t *testing.T) {
	redis := newFakeRedis()
	key := locks.ProcessingKey(locks.StageEnricher, "https://example.com/a")
	ctx := context.Background()

	// 두 인스턴스는 같은 Redis 를 보지만 토큰은 각자 보관한다.
	instanceA := locks.NewRedisProcessingLock(redis, time.Minute)
	instanceB := locks.NewRedisProcessingLock(redis, time.Minute)

	tokenA, acquired, err := instanceA.Acquire(ctx, key)
	require.NoError(t, err)
	require.True(t, acquired, "A 가 먼저 획득해야 함")

	// A 의 처리가 TTL 을 초과 → 락 자동 만료
	redis.Expire(key)

	_, acquired, err = instanceB.Acquire(ctx, key)
	require.NoError(t, err)
	require.True(t, acquired, "만료 후 B 가 획득해야 함")
	tokenB, ok := redis.held(key)
	require.True(t, ok)

	// 뒤늦게 끝난 A 가 release — 예전 구현은 여기서 B 의 락을 지웠다.
	err = instanceA.Release(ctx, key, tokenA)

	assert.ErrorIs(t, err, locks.ErrLockNotOwned, "소유권 상실이 호출자에게 보고되어야 함")
	got, stillHeld := redis.held(key)
	assert.True(t, stillHeld, "B 의 락이 살아 있어야 함")
	assert.Equal(t, tokenB, got, "B 의 토큰이 그대로여야 함")
}

func TestRedisProcessingLock_NormalCycle_ReleasesOwnLock(t *testing.T) {
	redis := newFakeRedis()
	key := locks.ProcessingKey(locks.StageParser, "https://example.com/b")
	lock := locks.NewRedisProcessingLock(redis, time.Minute)
	ctx := context.Background()

	token, acquired, err := lock.Acquire(ctx, key)
	require.NoError(t, err)
	require.True(t, acquired)

	require.NoError(t, lock.Release(ctx, key, token))

	_, stillHeld := redis.held(key)
	assert.False(t, stillHeld, "정상 경로에서는 락이 해제되어야 함")
}

// 잡은 적 없는 키의 Release 는 조용히 no-op — 남의 락을 건드릴 근거가 없다.
func TestRedisProcessingLock_ReleaseWithoutAcquire_IsNoop(t *testing.T) {
	redis := newFakeRedis()
	key := locks.ProcessingKey(locks.StageValidator, "https://example.com/c")
	ctx := context.Background()

	owner := locks.NewRedisProcessingLock(redis, time.Minute)
	_, acquired, err := owner.Acquire(ctx, key)
	require.NoError(t, err)
	require.True(t, acquired)

	// 토큰 없이 Release — 잡은 적 없는 쪽의 호출을 흉내.
	stranger := locks.NewRedisProcessingLock(redis, time.Minute)
	assert.NoError(t, stranger.Release(ctx, key, ""), "빈 토큰 Release 는 no-op 이어야 함")

	_, stillHeld := redis.held(key)
	assert.True(t, stillHeld, "타 인스턴스의 락이 보존되어야 함")
}

// 동일 키를 두 인스턴스가 동시에 잡으려 하면 하나만 성공해야 한다.
func TestRedisProcessingLock_ConcurrentAcquire_OnlyOneWins(t *testing.T) {
	redis := newFakeRedis()
	key := locks.ProcessingKey(locks.StageFetcher, "https://example.com/d")
	ctx := context.Background()

	const n = 16
	var wins int64
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l := locks.NewRedisProcessingLock(redis, time.Minute)
			if _, ok, err := l.Acquire(ctx, key); err == nil && ok {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	assert.EqualValues(t, 1, wins, "동시 획득은 1회만 성공해야 함")
}

// Copilot 리뷰 지적 — 맵 기반 토큰 보관의 aliasing 회귀.
//
// 하나의 ProcessingLock 인스턴스를 4개 stage 가 공유하므로, 같은 key 를 TTL 초과 후 같은
// 인스턴스가 다시 획득하는 상황이 실제로 가능하다. 토큰을 인스턴스 내부 맵에 key 단위로
// 보관하면 이때 이전 토큰이 덮어써지고, 먼저 시작한 쪽의 Release 가 나중 획득의 락을 지운다
// — 본래 막으려던 오삭제가 한 프로세스 안에서 재현된다.
//
// 토큰을 호출자가 들고 있으면 이 경로가 구조적으로 불가능하다.
func TestRedisProcessingLock_SameInstanceReacquire_OldTokenDoesNotDelete(t *testing.T) {
	redis := newFakeRedis()
	key := locks.ProcessingKey(locks.StageEnricher, "https://example.com/reacquire")
	ctx := context.Background()

	// **같은 인스턴스** 를 공유 — 운영 wiring 과 동일 (procLock 하나를 모든 stage 가 공유).
	shared := locks.NewRedisProcessingLock(redis, time.Minute)

	tokenFirst, acquired, err := shared.Acquire(ctx, key)
	require.NoError(t, err)
	require.True(t, acquired)

	redis.Expire(key) // 첫 처리가 TTL 초과

	tokenSecond, acquired, err := shared.Acquire(ctx, key)
	require.NoError(t, err)
	require.True(t, acquired)
	require.NotEqual(t, tokenFirst, tokenSecond, "재획득은 새 토큰이어야 함")

	// 첫 처리가 뒤늦게 끝나 release
	err = shared.Release(ctx, key, tokenFirst)

	assert.ErrorIs(t, err, locks.ErrLockNotOwned)
	held, stillHeld := redis.held(key)
	assert.True(t, stillHeld, "두 번째 획득의 락이 살아 있어야 함")
	assert.Equal(t, tokenSecond, held)
}
