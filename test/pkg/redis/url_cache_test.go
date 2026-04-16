package redis_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pkgredis "issuetracker/pkg/redis"
)

func TestURLCacheKey_Format(t *testing.T) {
	key := pkgredis.URLCacheKeyPrefix + "https://example.com/article"
	assert.Equal(t, "urlcache:https://example.com/article", key)
}

func TestSetURL_And_ExistsURL(t *testing.T) {
	client := newTestClient(t)
	ctx := context.Background()
	url := "https://example.com/test-set-exists-" + time.Now().Format(time.RFC3339Nano)

	// 고유한 URL을 사용해 사전 정리 없이 빈 캐시 상태를 보장

	// 캐시에 없는 상태 확인
	exists, err := client.ExistsURL(ctx, url)
	require.NoError(t, err)
	assert.False(t, exists, "캐시에 없는 URL은 false를 반환해야 함")

	// URL 캐시 등록
	err = client.SetURL(ctx, url, 5*time.Second)
	require.NoError(t, err)

	// 캐시에 있는 상태 확인
	exists, err = client.ExistsURL(ctx, url)
	require.NoError(t, err)
	assert.True(t, exists, "캐시에 등록된 URL은 true를 반환해야 함")
}

func TestExistsURL_TTLExpiry(t *testing.T) {
	client := newTestClient(t)
	ctx := context.Background()
	url := "https://example.com/test-ttl"

	// 짧은 TTL로 등록
	err := client.SetURL(ctx, url, 100*time.Millisecond)
	require.NoError(t, err)

	exists, err := client.ExistsURL(ctx, url)
	require.NoError(t, err)
	assert.True(t, exists)

	// TTL 만료 대기 후 확인
	require.Eventually(t, func() bool {
		exists, err := client.ExistsURL(ctx, url)
		require.NoError(t, err)
		return !exists
	}, 2*time.Second, 50*time.Millisecond, "TTL 만료 후 false를 반환해야 함")
}

func TestExistsURL_NotSet(t *testing.T) {
	client := newTestClient(t)
	ctx := context.Background()
	url := "https://example.com/never-set-" + time.Now().Format(time.RFC3339Nano)

	exists, err := client.ExistsURL(ctx, url)
	require.NoError(t, err)
	assert.False(t, exists, "등록되지 않은 URL은 false를 반환해야 함")
}
