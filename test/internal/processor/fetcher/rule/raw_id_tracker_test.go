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

// TestRedisRawIDTracker_TrackAndPeek_Basic:
// 같은 host 에 N건 Track → PeekByHost 가 N건 조회 (제거 안 함). 두 번째 Peek 도 같음.
func TestRedisRawIDTracker_TrackAndPeek_Basic(t *testing.T) {
	client := newTestRedisClient(t)
	prefix := uniqueKeyPrefix(t)
	tr, err := fetcherRule.NewRedisRawIDTracker(client.Raw(), time.Hour, prefix, nil)
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
	tr, err := fetcherRule.NewRedisRawIDTracker(client.Raw(), time.Hour, prefix, nil)
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
	tr, err := fetcherRule.NewRedisRawIDTracker(client.Raw(), time.Hour, prefix, nil)
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
	tr, err := fetcherRule.NewRedisRawIDTracker(client.Raw(), time.Hour, prefix, nil)
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
	tr, err := fetcherRule.NewRedisRawIDTracker(client.Raw(), time.Hour, "", nil)
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
	tr, err := fetcherRule.NewRedisRawIDTracker(client.Raw(), time.Hour, "test:nonexistent:"+t.Name(), nil)
	require.NoError(t, err)

	peeked, err := tr.PeekByHost(context.Background(), "no-such-host", 10)
	require.NoError(t, err)
	assert.Empty(t, peeked)
}

// TestNoopRawIDTracker_AllNoop:
// Noop tracker 의 모든 메소드 nil.
func TestNoopRawIDTracker_AllNoop(t *testing.T) {
	tr := fetcherRule.NewNoopRawIDTracker()
	ctx := context.Background()

	require.NoError(t, tr.Track(ctx, "host.com", "raw-1"))

	peeked, err := tr.PeekByHost(ctx, "host.com", 10)
	require.NoError(t, err)
	assert.Empty(t, peeked)

	require.NoError(t, tr.RemoveByHost(ctx, "host.com", []string{"raw-1"}))
}

// TestForceFetcherToken_InitAndValidate:
// process-local token 초기화 후 ValidateForceFetcherToken 가 일치 시 true.
func TestForceFetcherToken_InitAndValidate(t *testing.T) {
	require.NoError(t, fetcherRule.InitForceFetcherToken())

	tok := fetcherRule.ForceFetcherTokenValue()
	assert.NotEmpty(t, tok)
	assert.True(t, fetcherRule.ValidateForceFetcherToken(tok))
	assert.False(t, fetcherRule.ValidateForceFetcherToken(""))
	assert.False(t, fetcherRule.ValidateForceFetcherToken("wrong-token"))
}
