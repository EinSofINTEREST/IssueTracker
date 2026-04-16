package worker

import (
	"context"
	"time"
)

// URLCache는 URL 중복 fetch를 방지하는 캐시 인터페이스입니다.
// 구현체는 goroutine-safe해야 합니다.
type URLCache interface {
	// Exists는 URL이 이미 캐시에 존재하는지 확인합니다.
	// 이미 fetch한 URL이면 true를 반환합니다.
	Exists(ctx context.Context, url string) (bool, error)

	// Set은 URL을 캐시에 등록합니다.
	// fetch 성공 후 호출하여 이후 중복 fetch를 방지합니다.
	Set(ctx context.Context, url string) error
}

// RedisURLCache는 Redis 기반 URLCache 구현체입니다.
type RedisURLCache struct {
	client redisURLCacheClient
	ttl    time.Duration
}

// redisURLCacheClient는 Redis URL 캐시 조작을 추상화하는 내부 인터페이스입니다.
// pkg/redis.Client의 메서드 집합과 일치하며 테스트에서 mock으로 교체됩니다.
type redisURLCacheClient interface {
	SetURL(ctx context.Context, url string, ttl time.Duration) error
	ExistsURL(ctx context.Context, url string) (bool, error)
}

// NewRedisURLCache는 RedisURLCache를 생성합니다.
func NewRedisURLCache(client redisURLCacheClient, ttl time.Duration) *RedisURLCache {
	return &RedisURLCache{client: client, ttl: ttl}
}

func (c *RedisURLCache) Exists(ctx context.Context, url string) (bool, error) {
	return c.client.ExistsURL(ctx, url)
}

func (c *RedisURLCache) Set(ctx context.Context, url string) error {
	return c.client.SetURL(ctx, url, c.ttl)
}

// NoopURLCache는 캐시를 사용하지 않는 no-op 구현체입니다.
// Redis가 없는 환경(단일 인스턴스, 테스트)에서 사용합니다.
type NoopURLCache struct{}

func (NoopURLCache) Exists(_ context.Context, _ string) (bool, error) { return false, nil }
func (NoopURLCache) Set(_ context.Context, _ string) error            { return nil }
