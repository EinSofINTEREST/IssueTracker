package redisstore_test

import (
	"context"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/storage/primitive"
	redisstore "issuetracker/internal/storage/redis"
)

// redisZ 는 테스트에서 goredis.Z 를 간결하게 생성하기 위한 helper.
func redisZ(member string, score float64) goredis.Z {
	return goredis.Z{Score: score, Member: member}
}

// TestNewRedisRawIDTracker_NilClient_ReturnsError:
// 이슈 #208 panic-on-nil → error 정책.
func TestNewRedisRawIDTracker_NilClient_ReturnsError(t *testing.T) {
	_, err := redisstore.NewRawIDTracker(nil, time.Hour, "", nil)
	assert.Error(t, err)
}

// TestNewRedisRawIDTracker_BadTTL_ReturnsError:
// ttl 0 또는 음수 → 의미 없음.
func TestNewRedisRawIDTracker_BadTTL_ReturnsError(t *testing.T) {
	client := newTestRedisClient(t)
	_, err := redisstore.NewRawIDTracker(client.Raw(), 0, "", nil)
	assert.Error(t, err)
}

// TestRedisRawIDTracker_TrackAndPeek_Basic:
// 같은 host 에 N건 Track → PeekByHost 가 N건 조회 (제거 안 함). 두 번째 Peek 도 같음.
func TestRedisRawIDTracker_TrackAndPeek_Basic(t *testing.T) {
	client := newTestRedisClient(t)
	prefix := uniqueKeyPrefix(t)
	tr, err := redisstore.NewRawIDTracker(client.Raw(), time.Hour, prefix, nil)
	require.NoError(t, err)

	host := "edition.cnn.com"
	ctx := context.Background()
	rawIDs := []string{"raw-1", "raw-2", "raw-3"}

	for _, id := range rawIDs {
		require.NoError(t, tr.Track(ctx, host, id))
		// 같은 timestamp 충돌 회피 + score 분산
		time.Sleep(1 * time.Millisecond)
	}

	peeked, err := tr.PeekByHost(ctx, host, 10)
	require.NoError(t, err)
	assert.ElementsMatch(t, rawIDs, peeked)

	// 두 번째 Peek 도 동일 — 제거 안 함 (Peek-then-Remove 의 핵심)
	peeked2, err := tr.PeekByHost(ctx, host, 10)
	require.NoError(t, err)
	assert.ElementsMatch(t, rawIDs, peeked2)

	t.Cleanup(func() {
		client.Raw().Del(context.Background(), prefix+":"+host)
	})
}

// TestRedisRawIDTracker_PeekByHost_RecencyOrder:
// ZREVRANGE 가 score DESC 순 → 가장 최근 추가가 첫 번째.
func TestRedisRawIDTracker_PeekByHost_RecencyOrder(t *testing.T) {
	client := newTestRedisClient(t)
	prefix := uniqueKeyPrefix(t)
	tr, err := redisstore.NewRawIDTracker(client.Raw(), time.Hour, prefix, nil)
	require.NoError(t, err)

	host := "naver.com"
	ctx := context.Background()
	for _, id := range []string{"oldest", "middle", "newest"} {
		require.NoError(t, tr.Track(ctx, host, id))
		time.Sleep(1 * time.Millisecond)
	}

	peeked, err := tr.PeekByHost(ctx, host, 10)
	require.NoError(t, err)
	require.Len(t, peeked, 3)
	assert.Equal(t, "newest", peeked[0], "score DESC — most recent first")
	assert.Equal(t, "oldest", peeked[2])

	t.Cleanup(func() {
		client.Raw().Del(context.Background(), prefix+":"+host)
	})
}

// TestRedisRawIDTracker_PeekByHost_RespectsLimit:
// limit 적용 — N개 중 limit 만 반환.
func TestRedisRawIDTracker_PeekByHost_RespectsLimit(t *testing.T) {
	client := newTestRedisClient(t)
	prefix := uniqueKeyPrefix(t)
	tr, err := redisstore.NewRawIDTracker(client.Raw(), time.Hour, prefix, nil)
	require.NoError(t, err)

	host := "yonhap.co.kr"
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		require.NoError(t, tr.Track(ctx, host, "raw-"+string(rune('a'+i))))
		time.Sleep(1 * time.Millisecond)
	}

	peeked, err := tr.PeekByHost(ctx, host, 3)
	require.NoError(t, err)
	assert.Len(t, peeked, 3)

	t.Cleanup(func() {
		client.Raw().Del(context.Background(), prefix+":"+host)
	})
}

// TestRedisRawIDTracker_RemoveByHost_RemovesOnlyListed:
// RemoveByHost 가 지정된 raw_id 만 제거 — 나머지 잔존.
func TestRedisRawIDTracker_RemoveByHost_RemovesOnlyListed(t *testing.T) {
	client := newTestRedisClient(t)
	prefix := uniqueKeyPrefix(t)
	tr, err := redisstore.NewRawIDTracker(client.Raw(), time.Hour, prefix, nil)
	require.NoError(t, err)

	host := "daum.net"
	ctx := context.Background()
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		require.NoError(t, tr.Track(ctx, host, id))
		time.Sleep(1 * time.Millisecond)
	}

	require.NoError(t, tr.RemoveByHost(ctx, host, []string{"a", "c", "e"}))

	peeked, err := tr.PeekByHost(ctx, host, 10)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"b", "d"}, peeked)

	t.Cleanup(func() {
		client.Raw().Del(context.Background(), prefix+":"+host)
	})
}

// TestRedisRawIDTracker_EmptyArgs_NoOp:
// 빈 host / 빈 rawID / limit<=0 모두 noop.
func TestRedisRawIDTracker_EmptyArgs_NoOp(t *testing.T) {
	client := newTestRedisClient(t)
	tr, err := redisstore.NewRawIDTracker(client.Raw(), time.Hour, "", nil)
	require.NoError(t, err)

	ctx := context.Background()

	require.NoError(t, tr.Track(ctx, "", "raw-1"))
	require.NoError(t, tr.Track(ctx, "host.com", ""))

	peeked, err := tr.PeekByHost(ctx, "", 10)
	require.NoError(t, err)
	assert.Empty(t, peeked)

	peeked, err = tr.PeekByHost(ctx, "host.com", 0)
	require.NoError(t, err)
	assert.Empty(t, peeked)

	require.NoError(t, tr.RemoveByHost(ctx, "", []string{"a"}))
	require.NoError(t, tr.RemoveByHost(ctx, "host.com", []string{}))
}

// TestRedisRawIDTracker_PeekByHost_NonexistentKey_ReturnsEmpty:
// 키 없음 → 빈 슬라이스.
func TestRedisRawIDTracker_PeekByHost_NonexistentKey_ReturnsEmpty(t *testing.T) {
	client := newTestRedisClient(t)
	tr, err := redisstore.NewRawIDTracker(client.Raw(), time.Hour, "test:nonexistent:"+t.Name(), nil)
	require.NoError(t, err)

	peeked, err := tr.PeekByHost(context.Background(), "no-such-host", 10)
	require.NoError(t, err)
	assert.Empty(t, peeked)
}

// TestRedisRawIDTracker_FreshnessFilter_ExcludesStaleEntries:
// freshness>0 일 때 (now - freshness) 보다 오래된 entry 가 PeekByHost 결과에서 제외 (이슈 #299).
//
// 시나리오:
//  1. 같은 host 에 3건 Track — 첫 2건은 freshness 임계 직전 시각, 마지막 1건은 그 이후
//  2. Track 후 직접 ZADD 로 oldest entry 의 score 를 충분히 과거로 강제 변경 (cleanup 으로 raw_contents
//     row 가 삭제된 상황 simulate)
//  3. PeekByHost 결과에 oldest 가 포함되지 않음을 확인.
func TestRedisRawIDTracker_FreshnessFilter_ExcludesStaleEntries(t *testing.T) {
	client := newTestRedisClient(t)
	prefix := uniqueKeyPrefix(t)
	freshness := 500 * time.Millisecond
	tr, err := redisstore.NewRawIDTrackerWithFreshness(client.Raw(), time.Hour, freshness, prefix, nil)
	require.NoError(t, err)

	host := "stale.example.com"
	ctx := context.Background()
	t.Cleanup(func() {
		client.Raw().Del(context.Background(), prefix+":"+host)
	})

	// stale entry: Track 후 score 를 과거로 직접 overwrite — cleanup 이 raw_contents 삭제한 상황 simulate.
	require.NoError(t, tr.Track(ctx, host, "raw-stale"))
	staleScore := float64(time.Now().Add(-2 * freshness).UnixNano())
	key := prefix + ":" + host
	require.NoError(t, client.Raw().ZAdd(ctx, key, redisZ("raw-stale", staleScore)).Err())

	// fresh entry: 정상 Track — 현재 시각 score.
	require.NoError(t, tr.Track(ctx, host, "raw-fresh"))

	peeked, err := tr.PeekByHost(ctx, host, 10)
	require.NoError(t, err)
	assert.Equal(t, []string{"raw-fresh"}, peeked, "freshness 보다 오래된 raw-stale 은 제외")
}

// TestRedisRawIDTracker_FreshnessFilter_ZeroDisablesFilter:
// freshness=0 (또는 음수) 이면 기존 ZREVRANGE 동작 그대로 — 모든 entry 반환.
func TestRedisRawIDTracker_FreshnessFilter_ZeroDisablesFilter(t *testing.T) {
	client := newTestRedisClient(t)
	prefix := uniqueKeyPrefix(t)
	tr, err := redisstore.NewRawIDTrackerWithFreshness(client.Raw(), time.Hour, 0, prefix, nil)
	require.NoError(t, err)

	host := "all.example.com"
	ctx := context.Background()
	t.Cleanup(func() {
		client.Raw().Del(context.Background(), prefix+":"+host)
	})

	require.NoError(t, tr.Track(ctx, host, "raw-old"))
	// 의도적으로 score 를 과거로 overwrite — freshness=0 이라 필터링 없음.
	oldScore := float64(time.Now().Add(-24 * time.Hour).UnixNano())
	key := prefix + ":" + host
	require.NoError(t, client.Raw().ZAdd(ctx, key, redisZ("raw-old", oldScore)).Err())

	time.Sleep(1 * time.Millisecond)
	require.NoError(t, tr.Track(ctx, host, "raw-new"))

	peeked, err := tr.PeekByHost(ctx, host, 10)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"raw-old", "raw-new"}, peeked, "freshness=0 — 모든 entry 반환")
}

// TestRedisRawIDTracker_FreshnessFilter_RespectsLimit:
// freshness 적용 시에도 limit 가 정상 동작 — count 옵션.
func TestRedisRawIDTracker_FreshnessFilter_RespectsLimit(t *testing.T) {
	client := newTestRedisClient(t)
	prefix := uniqueKeyPrefix(t)
	tr, err := redisstore.NewRawIDTrackerWithFreshness(client.Raw(), time.Hour, time.Hour, prefix, nil)
	require.NoError(t, err)

	host := "limit.example.com"
	ctx := context.Background()
	t.Cleanup(func() {
		client.Raw().Del(context.Background(), prefix+":"+host)
	})
	for i := 0; i < 5; i++ {
		require.NoError(t, tr.Track(ctx, host, "raw-"+string(rune('a'+i))))
		time.Sleep(1 * time.Millisecond)
	}

	peeked, err := tr.PeekByHost(ctx, host, 2)
	require.NoError(t, err)
	assert.Len(t, peeked, 2)
}

// TestNewRedisRawIDTracker_NegativeFreshnessNormalized:
// freshness=-1 등 음수 입력은 0 으로 정규화 — error 아님 (back-compat).
func TestNewRedisRawIDTracker_NegativeFreshnessNormalized(t *testing.T) {
	client := newTestRedisClient(t)
	_, err := redisstore.NewRawIDTrackerWithFreshness(client.Raw(), time.Hour, -1*time.Second, "", nil)
	assert.NoError(t, err, "음수 freshness 는 0 으로 정규화 — 생성 성공")
}

// TestNoopRawIDTracker_AllNoop:
// Noop tracker 의 모든 메소드 nil.
func TestNoopRawIDTracker_AllNoop(t *testing.T) {
	tr := primitive.NewNoopRawIDTracker()
	ctx := context.Background()

	require.NoError(t, tr.Track(ctx, "host.com", "raw-1"))

	peeked, err := tr.PeekByHost(ctx, "host.com", 10)
	require.NoError(t, err)
	assert.Empty(t, peeked)

	require.NoError(t, tr.RemoveByHost(ctx, "host.com", []string{"raw-1"}))
}
