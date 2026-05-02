package rule_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	fetcherRule "issuetracker/internal/processor/fetcher/rule"
)

// TestNewRedisRawIDTracker_NilClient_ReturnsError:
// 이슈 #208 panic-on-nil → error 정책.
func TestNewRedisRawIDTracker_NilClient_ReturnsError(t *testing.T) {
	_, err := fetcherRule.NewRedisRawIDTracker(nil, time.Hour, "", nil)
	assert.Error(t, err)
}

// TestNewRedisRawIDTracker_BadTTL_ReturnsError:
// ttl 0 또는 음수 → 의미 없음.
func TestNewRedisRawIDTracker_BadTTL_ReturnsError(t *testing.T) {
	client := newTestRedisClient(t)
	_, err := fetcherRule.NewRedisRawIDTracker(client.Raw(), 0, "", nil)
	assert.Error(t, err)
}

// TestRedisRawIDTracker_TrackAndPop_Basic:
// 같은 host 에 N건 Track → PopByHost 가 N건 반환 + Set 비어짐.
func TestRedisRawIDTracker_TrackAndPop_Basic(t *testing.T) {
	client := newTestRedisClient(t)
	prefix := uniqueKeyPrefix(t)
	tr, err := fetcherRule.NewRedisRawIDTracker(client.Raw(), time.Hour, prefix, nil)
	require.NoError(t, err)

	host := "edition.cnn.com"
	ctx := context.Background()
	rawIDs := []string{"raw-1", "raw-2", "raw-3"}

	for _, id := range rawIDs {
		require.NoError(t, tr.Track(ctx, host, id))
	}

	popped, err := tr.PopByHost(ctx, host, 10)
	require.NoError(t, err)
	assert.ElementsMatch(t, rawIDs, popped)

	// 두 번째 Pop 은 비어있어야 함 — atomic SPOP
	popped2, err := tr.PopByHost(ctx, host, 10)
	require.NoError(t, err)
	assert.Empty(t, popped2)

	t.Cleanup(func() {
		client.Raw().Del(context.Background(), prefix+":"+host)
	})
}

// TestRedisRawIDTracker_PopByHost_RespectsLimit:
// limit 초과분은 Set 에 남음.
func TestRedisRawIDTracker_PopByHost_RespectsLimit(t *testing.T) {
	client := newTestRedisClient(t)
	prefix := uniqueKeyPrefix(t)
	tr, err := fetcherRule.NewRedisRawIDTracker(client.Raw(), time.Hour, prefix, nil)
	require.NoError(t, err)

	host := "naver.com"
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		require.NoError(t, tr.Track(ctx, host, "raw-"+string(rune('a'+i))))
	}

	popped, err := tr.PopByHost(ctx, host, 3)
	require.NoError(t, err)
	assert.Len(t, popped, 3)

	popped2, err := tr.PopByHost(ctx, host, 10)
	require.NoError(t, err)
	assert.Len(t, popped2, 2)

	t.Cleanup(func() {
		client.Raw().Del(context.Background(), prefix+":"+host)
	})
}

// TestRedisRawIDTracker_EmptyArgs_NoOp:
// 빈 host / 빈 rawID / limit<=0 모두 noop (Redis 호출 없음, 에러 없음).
func TestRedisRawIDTracker_EmptyArgs_NoOp(t *testing.T) {
	client := newTestRedisClient(t)
	tr, err := fetcherRule.NewRedisRawIDTracker(client.Raw(), time.Hour, "", nil)
	require.NoError(t, err)

	ctx := context.Background()

	require.NoError(t, tr.Track(ctx, "", "raw-1"))
	require.NoError(t, tr.Track(ctx, "host.com", ""))

	popped, err := tr.PopByHost(ctx, "", 10)
	require.NoError(t, err)
	assert.Empty(t, popped)

	popped, err = tr.PopByHost(ctx, "host.com", 0)
	require.NoError(t, err)
	assert.Empty(t, popped)

	popped, err = tr.PopByHost(ctx, "host.com", -1)
	require.NoError(t, err)
	assert.Empty(t, popped)
}

// TestRedisRawIDTracker_PopByHost_NonexistentKey_ReturnsEmpty:
// 키 자체가 없는 host 의 Pop 은 redis.Nil 정규화 → 빈 슬라이스.
func TestRedisRawIDTracker_PopByHost_NonexistentKey_ReturnsEmpty(t *testing.T) {
	client := newTestRedisClient(t)
	tr, err := fetcherRule.NewRedisRawIDTracker(client.Raw(), time.Hour, "test:nonexistent:"+t.Name(), nil)
	require.NoError(t, err)

	popped, err := tr.PopByHost(context.Background(), "no-such-host", 10)
	require.NoError(t, err)
	assert.Empty(t, popped)
}

// TestNoopRawIDTracker_AllNoop:
// Noop tracker 는 Track / PopByHost 모두 nil 반환.
func TestNoopRawIDTracker_AllNoop(t *testing.T) {
	tr := fetcherRule.NewNoopRawIDTracker()
	ctx := context.Background()

	require.NoError(t, tr.Track(ctx, "host.com", "raw-1"))

	popped, err := tr.PopByHost(ctx, "host.com", 10)
	require.NoError(t, err)
	assert.Empty(t, popped)
}
