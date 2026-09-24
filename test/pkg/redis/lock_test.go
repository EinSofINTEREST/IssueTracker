package redis_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	storagecfg "issuetracker/pkg/config/storage"

	pkgredis "issuetracker/pkg/redis"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestClient는 로컬 Redis에 연결하는 테스트용 클라이언트를 반환합니다.
// REDIS_HOST/PORT 환경변수로 주소를 변경할 수 있습니다.
func newTestClient(t *testing.T) *pkgredis.Client {
	t.Helper()
	cfg, err := storagecfg.LoadRedis()
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	client, err := pkgredis.New(ctx, cfg)
	if err != nil {
		t.Skipf("Redis not available (%v) — skipping integration test", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func TestLockKey_Format(t *testing.T) {
	key := pkgredis.LockKey("abc123")
	assert.Equal(t, "lock:crawl:abc123", key)
}

func TestAcquireLock_Success(t *testing.T) {
	client := newTestClient(t)
	ctx := context.Background()
	key := pkgredis.LockKey("test-acquire-success")

	// 사전 정리
	_ = client.ReleaseLock(ctx, key)

	ok, err := client.AcquireLock(ctx, key, 5*time.Second)
	require.NoError(t, err)
	assert.True(t, ok, "첫 번째 획득은 성공해야 함")

	t.Cleanup(func() { _ = client.ReleaseLock(ctx, key) })
}

func TestAcquireLock_AlreadyHeld(t *testing.T) {
	client := newTestClient(t)
	ctx := context.Background()
	key := pkgredis.LockKey("test-acquire-held")

	_ = client.ReleaseLock(ctx, key)

	ok1, err := client.AcquireLock(ctx, key, 5*time.Second)
	require.NoError(t, err)
	assert.True(t, ok1)

	ok2, err := client.AcquireLock(ctx, key, 5*time.Second)
	require.NoError(t, err)
	assert.False(t, ok2, "이미 잠긴 키는 false를 반환해야 함")

	t.Cleanup(func() { _ = client.ReleaseLock(ctx, key) })
}

func TestReleaseLock_Success(t *testing.T) {
	client := newTestClient(t)
	ctx := context.Background()
	key := pkgredis.LockKey("test-release")

	_ = client.ReleaseLock(ctx, key)

	_, err := client.AcquireLock(ctx, key, 5*time.Second)
	require.NoError(t, err)

	err = client.ReleaseLock(ctx, key)
	require.NoError(t, err)

	// 해제 후 재획득 가능해야 함
	ok, err := client.AcquireLock(ctx, key, 5*time.Second)
	require.NoError(t, err)
	assert.True(t, ok, "해제 후 재획득 가능해야 함")

	t.Cleanup(func() { _ = client.ReleaseLock(ctx, key) })
}

func TestAcquireLock_TTLExpiry(t *testing.T) {
	client := newTestClient(t)
	ctx := context.Background()
	key := pkgredis.LockKey("test-ttl-expiry")

	_ = client.ReleaseLock(ctx, key)

	ok, err := client.AcquireLock(ctx, key, 100*time.Millisecond)
	require.NoError(t, err)
	assert.True(t, ok)

	// TTL 만료 시점은 CI/부하 상황에서 지터가 발생할 수 있으므로
	// 일정 시간 동안 재획득 가능해질 때까지 폴링한다.
	var acquired bool
	require.Eventually(t, func() bool {
		ok2, err := client.AcquireLock(ctx, key, 5*time.Second)
		require.NoError(t, err)
		if ok2 {
			acquired = true
			return true
		}
		return false
	}, 2*time.Second, 50*time.Millisecond, "TTL 만료 후 재획득 가능해야 함")
	assert.True(t, acquired, "TTL 만료 후 재획득 가능해야 함")
	t.Cleanup(func() { _ = client.ReleaseLock(ctx, key) })
}

// ─────────────────────────────────────────────────────────────────────────────
// 토큰 기반 락 — 실제 Redis 통합 (이슈 #562)
//
// internal/locks 의 회귀 테스트는 fake Redis 를 쓰므로 다음이 검증되지 않는다:
//   - SET key <token> NX PX 가 실제로 토큰을 **값** 으로 저장하는지
//   - luaReleaseOwned 스크립트 자체의 정확성
//   - Eval 결과(int64) 처리와 goredis.Nil 분기
// 즉 Lua 가 잘못돼도 fake 기반 테스트는 통과한다. 아래는 그 공백을 메운다.
// ─────────────────────────────────────────────────────────────────────────────

// uniqueLockKey 는 테스트 간 충돌을 막는 키를 만들고 cleanup 을 등록합니다.
func uniqueLockKey(t *testing.T, client *pkgredis.Client, name string) string {
	t.Helper()
	key := fmt.Sprintf("test:lock:%s:%d", name, time.Now().UnixNano())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		client.Raw().Del(ctx, key)
	})
	return key
}

func TestAcquireLockWithToken_StoresTokenAsValue(t *testing.T) {
	client := newTestClient(t)
	ctx := context.Background()
	key := uniqueLockKey(t, client, "token-value")

	token, acquired, err := client.AcquireLockWithToken(ctx, key, time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	require.NotEmpty(t, token)

	// 값이 토큰 그대로여야 Lua 의 GET 비교가 성립한다.
	stored, err := client.Raw().Get(ctx, key).Result()
	require.NoError(t, err)
	assert.Equal(t, token, stored, "SET 이 토큰을 값으로 저장해야 한다")

	ttl, err := client.Raw().TTL(ctx, key).Result()
	require.NoError(t, err)
	assert.Greater(t, ttl, time.Duration(0), "PX 가 적용되어 TTL 이 설정돼야 한다")
}

func TestAcquireLockWithToken_SecondCallerFails(t *testing.T) {
	client := newTestClient(t)
	ctx := context.Background()
	key := uniqueLockKey(t, client, "nx")

	_, acquired, err := client.AcquireLockWithToken(ctx, key, time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)

	token2, acquired2, err := client.AcquireLockWithToken(ctx, key, time.Minute)
	require.NoError(t, err, "NX 실패는 에러가 아니다")
	assert.False(t, acquired2)
	assert.Empty(t, token2, "미획득 시 토큰은 빈 문자열")
}

func TestReleaseLockOwned_MatchingToken_DeletesKey(t *testing.T) {
	client := newTestClient(t)
	ctx := context.Background()
	key := uniqueLockKey(t, client, "release-ok")

	token, acquired, err := client.AcquireLockWithToken(ctx, key, time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)

	released, err := client.ReleaseLockOwned(ctx, key, token)
	require.NoError(t, err)
	assert.True(t, released)

	n, err := client.Raw().Exists(ctx, key).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(0), n, "소유자 해제는 키를 지워야 한다")
}

func TestReleaseLockOwned_MismatchedToken_PreservesKey(t *testing.T) {
	client := newTestClient(t)
	ctx := context.Background()
	key := uniqueLockKey(t, client, "release-mismatch")

	_, acquired, err := client.AcquireLockWithToken(ctx, key, time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)

	released, err := client.ReleaseLockOwned(ctx, key, "not-the-owner")
	require.NoError(t, err, "토큰 불일치는 에러가 아니라 released=false")
	assert.False(t, released)

	n, err := client.Raw().Exists(ctx, key).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(1), n, "타 인스턴스의 락을 지워서는 안 된다")
}

// TestReleaseLockOwned_AfterTTLExpiry_DoesNotDeleteNewOwner 는 이슈 #63 이 고친
// 결함을 실제 Redis 로 재현합니다.
//
// A 획득 → TTL 만료 → B 재획득 → 뒤늦은 A 의 해제가 B 의 락을 지우면 안 된다.
func TestReleaseLockOwned_AfterTTLExpiry_DoesNotDeleteNewOwner(t *testing.T) {
	client := newTestClient(t)
	ctx := context.Background()
	key := uniqueLockKey(t, client, "ttl-handover")

	tokenA, acquired, err := client.AcquireLockWithToken(ctx, key, 300*time.Millisecond)
	require.NoError(t, err)
	require.True(t, acquired)

	// TTL 만료 대기 — 만료 후 B 가 같은 키를 잡을 수 있어야 한다.
	require.Eventually(t, func() bool {
		n, e := client.Raw().Exists(ctx, key).Result()
		return e == nil && n == 0
	}, 3*time.Second, 50*time.Millisecond, "TTL 이 만료되어야 한다")

	tokenB, acquired, err := client.AcquireLockWithToken(ctx, key, time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	require.NotEqual(t, tokenA, tokenB)

	// 뒤늦게 끝난 A 의 해제 — B 의 락은 살아 있어야 한다.
	released, err := client.ReleaseLockOwned(ctx, key, tokenA)
	require.NoError(t, err)
	assert.False(t, released, "만료된 소유자의 해제는 released=false")

	stored, err := client.Raw().Get(ctx, key).Result()
	require.NoError(t, err)
	assert.Equal(t, tokenB, stored, "B 의 락이 그대로 유지돼야 한다")
}

func TestReleaseLockOwned_EmptyToken_ReturnsError(t *testing.T) {
	client := newTestClient(t)
	ctx := context.Background()
	key := uniqueLockKey(t, client, "empty-token")

	released, err := client.ReleaseLockOwned(ctx, key, "")
	require.Error(t, err, "빈 토큰은 소유권 검증을 무력화하므로 에러여야 한다")
	assert.False(t, released)
}

// TestReleaseLockOwned_MissingKey_ReturnsFalse 는 이미 만료된 락의 해제가 에러가 아님을
// 검증합니다.
//
// 참고: 이 경로는 goredis.Nil 분기를 타지 않습니다 (Copilot 피드백). luaReleaseOwned 는
// GET 결과가 무엇이든 정수를 반환하므로 (일치 시 DEL 결과, 아니면 0) Eval 은
// (int64(0), nil) 을 돌려줍니다. ReleaseLockOwned 의 goredis.Nil 처리는 스크립트가 nil 을
// 반환하는 경우에 대한 방어이며, 현재 스크립트로는 도달하지 않습니다.
func TestReleaseLockOwned_MissingKey_ReturnsFalse(t *testing.T) {
	client := newTestClient(t)
	ctx := context.Background()
	key := uniqueLockKey(t, client, "missing")

	released, err := client.ReleaseLockOwned(ctx, key, "any-token")
	require.NoError(t, err, "키 부재는 정상 경로 (이미 만료)")
	assert.False(t, released)
}

// TestAcquireLockWithToken_Concurrent_OnlyOneWins 는 동시 획득에서 정확히 1회만
// 성공하는지 검증합니다 — NX 가 실제로 atomic 한지 확인.
func TestAcquireLockWithToken_Concurrent_OnlyOneWins(t *testing.T) {
	client := newTestClient(t)
	ctx := context.Background()
	key := uniqueLockKey(t, client, "concurrent")

	const goroutines = 20
	var wg sync.WaitGroup
	var winners int32
	tokens := make(chan string, goroutines)
	// 에러를 삼키면 19개가 Redis 오류로 죽어도 winners==1 이라 테스트가 통과한다 —
	// NX 경쟁이 아니라 장애를 검증하게 된다 (Copilot 피드백).
	errs := make(chan error, goroutines)
	start := make(chan struct{})

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			token, acquired, err := client.AcquireLockWithToken(ctx, key, time.Minute)
			if err != nil {
				errs <- err
				return
			}
			if acquired {
				atomic.AddInt32(&winners, 1)
				tokens <- token
			}
		}()
	}
	close(start)
	wg.Wait()
	close(tokens)
	close(errs)

	for err := range errs {
		require.NoError(t, err, "경쟁 참가자 중 누구도 Redis 오류를 만나면 안 된다")
	}
	assert.Equal(t, int32(1), atomic.LoadInt32(&winners), "동시 획득에서 정확히 1회만 성공")

	winner := <-tokens
	stored, err := client.Raw().Get(ctx, key).Result()
	require.NoError(t, err)
	assert.Equal(t, winner, stored, "저장된 값이 승자의 토큰이어야 한다")
}
