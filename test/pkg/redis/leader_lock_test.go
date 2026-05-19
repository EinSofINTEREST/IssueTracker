package redis_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pkgredis "issuetracker/pkg/redis"
)

// TestLeaderLockKey_Format 는 key prefix + role 매핑 검증.
func TestLeaderLockKey_Format(t *testing.T) {
	assert.Equal(t, "lock:leader:buffer_drainer", pkgredis.LeaderLockKey("buffer_drainer"))
}

// TestNewLeaderLocker_NilClient_Error 는 nil client 시 error 반환 검증.
func TestNewLeaderLocker_NilClient_Error(t *testing.T) {
	_, err := pkgredis.NewLeaderLocker(nil, "role", time.Second)
	assert.Error(t, err)
}

// TestNewLeaderLocker_EmptyRole_Error 는 빈 role 시 error 반환 검증.
func TestNewLeaderLocker_EmptyRole_Error(t *testing.T) {
	client := newTestClient(t) // Redis 미연결 시 skip
	_, err := pkgredis.NewLeaderLocker(client.Raw(), "", time.Second)
	assert.Error(t, err)
}

// TestNewLeaderLocker_ZeroTTL_Error 는 ttl 0 이하 시 error 반환 검증.
func TestNewLeaderLocker_ZeroTTL_Error(t *testing.T) {
	client := newTestClient(t)
	_, err := pkgredis.NewLeaderLocker(client.Raw(), "role", 0)
	assert.Error(t, err)
	_, err = pkgredis.NewLeaderLocker(client.Raw(), "role", -1*time.Second)
	assert.Error(t, err)
}

// TestLeaderLocker_TryAcquire_NewLeader_Succeeds 는 신규 leader 획득 시 true 반환 + HasLock 활성.
func TestLeaderLocker_TryAcquire_NewLeader_Succeeds(t *testing.T) {
	client := newTestClient(t)
	role := "test-leader-new-" + time.Now().Format("150405.000000")
	locker, err := pkgredis.NewLeaderLocker(client.Raw(), role, 2*time.Second)
	require.NoError(t, err)

	ctx := context.Background()
	t.Cleanup(func() { _ = locker.Release(ctx) })

	acquired, err := locker.TryAcquire(ctx)
	require.NoError(t, err)
	assert.True(t, acquired)
	assert.True(t, locker.HasLock())
}

// TestLeaderLocker_TryAcquire_AlreadyHeldByOther_ReturnsFalse 는 다른 instance lock 보유 시 false.
func TestLeaderLocker_TryAcquire_AlreadyHeldByOther_ReturnsFalse(t *testing.T) {
	client := newTestClient(t)
	role := "test-leader-contested-" + time.Now().Format("150405.000000")

	leader1, err := pkgredis.NewLeaderLocker(client.Raw(), role, 5*time.Second)
	require.NoError(t, err)
	leader2, err := pkgredis.NewLeaderLocker(client.Raw(), role, 5*time.Second)
	require.NoError(t, err)

	ctx := context.Background()
	t.Cleanup(func() { _ = leader1.Release(ctx) })

	// instance1 이 먼저 획득
	acquired1, err := leader1.TryAcquire(ctx)
	require.NoError(t, err)
	require.True(t, acquired1)

	// instance2 는 false
	acquired2, err := leader2.TryAcquire(ctx)
	require.NoError(t, err)
	assert.False(t, acquired2)
	assert.False(t, leader2.HasLock())
}

// TestLeaderLocker_Release_OwnershipRequired 는 release 시 token ownership 확인 검증.
// instance1 lock → instance1.Release → 그 후 instance2 가 lock 획득 가능.
func TestLeaderLocker_Release_OwnershipRequired(t *testing.T) {
	client := newTestClient(t)
	role := "test-leader-release-" + time.Now().Format("150405.000000")

	leader1, err := pkgredis.NewLeaderLocker(client.Raw(), role, 5*time.Second)
	require.NoError(t, err)
	leader2, err := pkgredis.NewLeaderLocker(client.Raw(), role, 5*time.Second)
	require.NoError(t, err)

	ctx := context.Background()
	t.Cleanup(func() {
		_ = leader1.Release(ctx)
		_ = leader2.Release(ctx)
	})

	acquired1, err := leader1.TryAcquire(ctx)
	require.NoError(t, err)
	require.True(t, acquired1)

	// instance1 release 후 in-process 상태 false.
	require.NoError(t, leader1.Release(ctx))
	assert.False(t, leader1.HasLock())

	// instance2 가 이제 획득 가능.
	acquired2, err := leader2.TryAcquire(ctx)
	require.NoError(t, err)
	assert.True(t, acquired2)
}

// TestLeaderLocker_Release_Noop_WhenNotHeld 는 lock 미보유 시 Release noop 검증.
func TestLeaderLocker_Release_Noop_WhenNotHeld(t *testing.T) {
	client := newTestClient(t)
	locker, err := pkgredis.NewLeaderLocker(client.Raw(), "test-leader-noop-release", 2*time.Second)
	require.NoError(t, err)

	// 미보유 상태에서 Release — error 없이 통과.
	assert.NoError(t, locker.Release(context.Background()))
}

// TestLeaderLocker_TryAcquire_DoubleByOwner_ReturnsFalse 는 같은 인스턴스의 이중 획득 방지 검증.
func TestLeaderLocker_TryAcquire_DoubleByOwner_ReturnsFalse(t *testing.T) {
	client := newTestClient(t)
	role := "test-leader-double-" + time.Now().Format("150405.000000")
	locker, err := pkgredis.NewLeaderLocker(client.Raw(), role, 5*time.Second)
	require.NoError(t, err)

	ctx := context.Background()
	t.Cleanup(func() { _ = locker.Release(ctx) })

	acquired1, err := locker.TryAcquire(ctx)
	require.NoError(t, err)
	require.True(t, acquired1)

	// 같은 인스턴스가 다시 TryAcquire — false (이중 획득 방지).
	acquired2, err := locker.TryAcquire(ctx)
	require.NoError(t, err)
	assert.False(t, acquired2)
}

// TestLeaderLocker_TTLExpiry_OtherInstanceCanAcquire 는 TTL 만료 후 다른 인스턴스가 leader 승격 검증.
func TestLeaderLocker_TTLExpiry_OtherInstanceCanAcquire(t *testing.T) {
	client := newTestClient(t)
	role := "test-leader-ttl-" + time.Now().Format("150405.000000")

	short := 500 * time.Millisecond
	leader1, err := pkgredis.NewLeaderLocker(client.Raw(), role, short)
	require.NoError(t, err)
	leader2, err := pkgredis.NewLeaderLocker(client.Raw(), role, short)
	require.NoError(t, err)

	ctx := context.Background()
	t.Cleanup(func() {
		_ = leader1.Release(ctx)
		_ = leader2.Release(ctx)
	})

	acquired1, _ := leader1.TryAcquire(ctx)
	require.True(t, acquired1)

	// TTL 만료 대기 (~600ms).
	time.Sleep(700 * time.Millisecond)

	// 만료된 lock 자리에 instance2 가 leader 승격.
	acquired2, err := leader2.TryAcquire(ctx)
	require.NoError(t, err)
	assert.True(t, acquired2, "TTL 만료 후 다른 instance 가 leader 승격 가능")
}
