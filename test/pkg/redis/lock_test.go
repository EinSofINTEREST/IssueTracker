package redis_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/pkg/config"
	pkgredis "issuetracker/pkg/redis"
)

// newTestClient는 로컬 Redis에 연결하는 테스트용 클라이언트를 반환합니다.
// REDIS_HOST/PORT 환경변수로 주소를 변경할 수 있습니다.
func newTestClient(t *testing.T) *pkgredis.Client {
	t.Helper()
	cfg, err := config.LoadRedis()
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
