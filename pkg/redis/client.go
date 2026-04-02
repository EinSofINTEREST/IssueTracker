// Package redis는 Redis 연결 및 분산 락 기능을 제공합니다.
//
// Package redis provides Redis connection and distributed lock utilities.
package redis

import (
	"context"
	"fmt"

	goredis "github.com/redis/go-redis/v9"
	"issuetracker/pkg/config"
)

// Client는 Redis 연결 래퍼입니다.
//
// Client wraps a Redis connection and exposes lock and health check operations.
type Client struct {
	rdb *goredis.Client
}

// New는 RedisConfig로 새 Redis 클라이언트를 생성하고 Ping으로 연결을 검증합니다.
//
// New creates a new Client from RedisConfig and validates the connection with Ping.
func New(ctx context.Context, cfg config.RedisConfig) (*Client, error) {
	rdb := goredis.NewClient(&goredis.Options{
		Addr:         fmt.Sprintf("%s:%d", cfg.Host, cfg.Port),
		Password:     cfg.Password,
		DB:           cfg.DB,
		DialTimeout:  cfg.DialTimeout,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
		PoolSize:     cfg.PoolSize,
	})

	if err := rdb.Ping(ctx).Err(); err != nil {
		_ = rdb.Close()
		return nil, fmt.Errorf("redis connect %s:%d: %w", cfg.Host, cfg.Port, err)
	}

	return &Client{rdb: rdb}, nil
}

// HealthCheck는 Ping으로 Redis 연결 상태를 확인합니다.
//
// HealthCheck verifies the connection is alive via Ping.
func (c *Client) HealthCheck(ctx context.Context) error {
	if err := c.rdb.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("redis health check: %w", err)
	}
	return nil
}

// Close는 Redis 연결 풀을 닫습니다.
func (c *Client) Close() error {
	return c.rdb.Close()
}
