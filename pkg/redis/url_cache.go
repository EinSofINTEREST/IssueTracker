package redis

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"
)

// URLCacheKeyPrefix는 URL 캐시 키의 공통 접두사입니다.
const URLCacheKeyPrefix = "urlcache:"

// SetURL은 URL을 캐시에 등록합니다.
// ttl 동안 동일 URL에 대한 중복 fetch를 방지합니다.
//
// SetURL marks a URL as visited in the cache with the given TTL.
func (c *Client) SetURL(ctx context.Context, url string, ttl time.Duration) error {
	key := urlCacheKey(url)
	if err := c.rdb.Set(ctx, key, 1, ttl).Err(); err != nil {
		return fmt.Errorf("set url cache %s: %w", key, err)
	}
	return nil
}

// ExistsURL은 URL이 캐시에 존재하는지 확인합니다.
// 이미 fetch한 URL이면 true를 반환합니다.
//
// ExistsURL checks whether a URL has been recently visited.
func (c *Client) ExistsURL(ctx context.Context, url string) (bool, error) {
	key := urlCacheKey(url)
	n, err := c.rdb.Exists(ctx, key).Result()
	if err != nil {
		return false, fmt.Errorf("check url cache %s: %w", key, err)
	}
	return n > 0, nil
}

func urlCacheKey(url string) string {
	hash := sha256.Sum256([]byte(url))
	return URLCacheKeyPrefix + hex.EncodeToString(hash[:])
}
