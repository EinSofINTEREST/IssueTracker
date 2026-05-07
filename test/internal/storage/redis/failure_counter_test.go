package redisstore_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/storage"
	redisstore "issuetracker/internal/storage/redis"
	"issuetracker/pkg/config"
	pkgredis "issuetracker/pkg/redis"
)

// newTestRedisClient 는 로컬 Redis 에 연결하는 테스트 클라이언트를 만듭니다.
// 미연결 시 통합 테스트 자체 skip (lock_test.go 패턴 동일).
func newTestRedisClient(t *testing.T) *pkgredis.Client {
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

// uniqueKeyPrefix 는 동시 실행 / 반복 실행 시 테스트 간 격리를 위한 prefix 입니다.
func uniqueKeyPrefix(t *testing.T) string {
	t.Helper()
	return "test:fetcher:fail:" + t.Name() + ":" + time.Now().Format(time.RFC3339Nano)
}

// TestNewRedisFailureCounter_NilClient_ReturnsError:
// 이슈 #208 panic-on-nil → error 정책. nil client 는 wiring 오류라 즉시 error.
func TestNewRedisFailureCounter_NilClient_ReturnsError(t *testing.T) {
	_, err := redisstore.NewFailureCounter(nil, 5, time.Hour, "", newTestLogger())
	assert.Error(t, err)
}

// TestNewRedisFailureCounter_BadThreshold_ReturnsError:
// threshold 0 또는 음수 → 카운터 자체 무의미. 즉시 error.
func TestNewRedisFailureCounter_BadThreshold_ReturnsError(t *testing.T) {
	client := newTestRedisClient(t)
	_, err := redisstore.NewFailureCounter(client.Raw(), 0, time.Hour, "", newTestLogger())
	assert.Error(t, err)

	_, err = redisstore.NewFailureCounter(client.Raw(), -1, time.Hour, "", newTestLogger())
	assert.Error(t, err)
}

// TestNewRedisFailureCounter_BadWindow_ReturnsError:
// window 0 또는 음수 → sliding window 의미 없음. 즉시 error.
func TestNewRedisFailureCounter_BadWindow_ReturnsError(t *testing.T) {
	client := newTestRedisClient(t)
	_, err := redisstore.NewFailureCounter(client.Raw(), 5, 0, "", newTestLogger())
	assert.Error(t, err)
}

// TestRedisFailureCounter_Record_FirstHitBelowThreshold:
// 첫 실패 1건 등록 → count=1, threshold(=5) 미달 → false.
func TestRedisFailureCounter_Record_FirstHitBelowThreshold(t *testing.T) {
	client := newTestRedisClient(t)
	prefix := uniqueKeyPrefix(t)
	c, err := redisstore.NewFailureCounter(client.Raw(), 5, time.Hour, prefix, newTestLogger())
	require.NoError(t, err)

	count, reached, err := c.Record(context.Background(), "edition.cnn.com", storage.FailureReasonRuleParseFailure)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
	assert.False(t, reached)

	// cleanup
	t.Cleanup(func() {
		client.Raw().Del(context.Background(), prefix+":edition.cnn.com")
	})
}

// TestRedisFailureCounter_Record_AccumulatesUntilThreshold:
// 임계값 (3) 만큼 누적 → count=3, threshold 도달 → true.
func TestRedisFailureCounter_Record_AccumulatesUntilThreshold(t *testing.T) {
	client := newTestRedisClient(t)
	prefix := uniqueKeyPrefix(t)
	c, err := redisstore.NewFailureCounter(client.Raw(), 3, time.Hour, prefix, newTestLogger())
	require.NoError(t, err)

	host := "naver.com"
	ctx := context.Background()

	count, reached, err := c.Record(ctx, host, storage.FailureReasonRuleParseFailure)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
	assert.False(t, reached)

	count, reached, err = c.Record(ctx, host, storage.FailureReasonEmptyBody)
	require.NoError(t, err)
	assert.Equal(t, 2, count)
	assert.False(t, reached)

	count, reached, err = c.Record(ctx, host, storage.FailureReasonRuleParseFailure)
	require.NoError(t, err)
	assert.Equal(t, 3, count)
	assert.True(t, reached)

	// 임계값 도달 이후 추가 record 도 threshold=true 유지 (windowing 안에서는 cumulative).
	count, reached, err = c.Record(ctx, host, storage.FailureReasonEmptyBody)
	require.NoError(t, err)
	assert.Equal(t, 4, count)
	assert.True(t, reached)

	t.Cleanup(func() {
		client.Raw().Del(context.Background(), prefix+":"+host)
	})
}

// TestRedisFailureCounter_Record_PrunesOldEntries:
// window 보다 오래된 entry 는 ZREMRANGEBYSCORE 로 제거되어 카운트에서 빠진다.
//
// 직접 시간 흐름을 만들 수 없으므로 매우 짧은 window (50ms) + sleep 으로 검증.
func TestRedisFailureCounter_Record_PrunesOldEntries(t *testing.T) {
	client := newTestRedisClient(t)
	prefix := uniqueKeyPrefix(t)
	c, err := redisstore.NewFailureCounter(client.Raw(), 5, 50*time.Millisecond, prefix, newTestLogger())
	require.NoError(t, err)

	host := "yonhap.co.kr"
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		_, _, err := c.Record(ctx, host, storage.FailureReasonRuleParseFailure)
		require.NoError(t, err)
	}

	// window 만료 대기 + 새 record 로 ZREMRANGEBYSCORE 발동 확인.
	time.Sleep(80 * time.Millisecond)

	count, reached, err := c.Record(ctx, host, storage.FailureReasonRuleParseFailure)
	require.NoError(t, err)
	assert.Equal(t, 1, count, "이전 3건은 window 밖이라 제거됨, 새 1건만 남아야 함")
	assert.False(t, reached)

	t.Cleanup(func() {
		client.Raw().Del(context.Background(), prefix+":"+host)
	})
}

// TestRedisFailureCounter_EmptyHost_ReturnsZero:
// 빈 host 는 즉시 (0, false, nil) — Redis 호출 없음.
func TestRedisFailureCounter_EmptyHost_ReturnsZero(t *testing.T) {
	client := newTestRedisClient(t)
	c, err := redisstore.NewFailureCounter(client.Raw(), 5, time.Hour, "", newTestLogger())
	require.NoError(t, err)

	count, reached, err := c.Record(context.Background(), "", storage.FailureReasonRuleParseFailure)
	require.NoError(t, err)
	assert.Equal(t, 0, count)
	assert.False(t, reached)
}

// TestNoopFailureCounter_AlwaysReturnsZero:
// FETCHER_AUTO_UPGRADE_ENABLED=false 또는 Redis 미연결 시 사용. 항상 (0, false, nil).
func TestNoopFailureCounter_AlwaysReturnsZero(t *testing.T) {
	c := storage.NewNoopFailureCounter()

	count, reached, err := c.Record(context.Background(), "any.host.com", storage.FailureReasonRuleParseFailure)
	require.NoError(t, err)
	assert.Equal(t, 0, count)
	assert.False(t, reached)
}
