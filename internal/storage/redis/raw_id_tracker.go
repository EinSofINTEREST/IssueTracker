package redisstore

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"issuetracker/internal/storage"
	"issuetracker/pkg/logger"
)

// rawIDTracker 는 Redis ZSET 기반 RawIDTracker 구현체입니다.
//
// 알고리즘:
//
//   - Track:        ZADD key={prefix}:{host}, score=now-unix-nano, member=rawID + EXPIRE ttl
//   - PeekByHost:   ZREVRANGE key 0 limit-1 (score DESC = 최근 우선)
//   - RemoveByHost: ZREM key member1 member2 ...
type rawIDTracker struct {
	client    *goredis.Client
	keyPrefix string
	ttl       time.Duration
	now       func() time.Time // 테스트 주입용 — 일반 사용 시 nil → time.Now
	log       *logger.Logger
}

// NewRawIDTracker 는 Redis ZSET 기반 RawIDTracker 를 생성합니다.
//
// client 가 nil 이거나 ttl 이 비정상이면 error 반환.
// keyPrefix 가 빈 문자열이면 "fetcher:failed_raws" 사용.
// ttl 은 host 별 ZSET 의 자연 정리 시간 — FailureCounter 의 window 와 동기화 권장.
func NewRawIDTracker(client *goredis.Client, ttl time.Duration, keyPrefix string, log *logger.Logger) (storage.RawIDTracker, error) {
	if client == nil {
		return nil, errors.New("redisstore: NewRawIDTracker requires non-nil redis client")
	}
	if ttl <= 0 {
		return nil, fmt.Errorf("redisstore: NewRawIDTracker requires positive ttl, got %s", ttl)
	}
	if keyPrefix == "" {
		keyPrefix = "fetcher:failed_raws"
	}
	return &rawIDTracker{
		client:    client,
		keyPrefix: keyPrefix,
		ttl:       ttl,
		log:       log,
	}, nil
}

func (r *rawIDTracker) keyFor(host string) string {
	return r.keyPrefix + ":" + host
}

func (r *rawIDTracker) Track(ctx context.Context, host, rawID string) error {
	if host == "" || rawID == "" {
		return nil
	}
	key := r.keyFor(host)
	now := time.Now()
	if r.now != nil {
		now = r.now()
	}
	score := float64(now.UnixNano())

	pipe := r.client.Pipeline()
	pipe.ZAdd(ctx, key, goredis.Z{Score: score, Member: rawID})
	pipe.Expire(ctx, key, r.ttl)
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("redis raw id tracker ZADD for host %s: %w", host, err)
	}
	return nil
}

func (r *rawIDTracker) PeekByHost(ctx context.Context, host string, limit int) ([]string, error) {
	if host == "" || limit <= 0 {
		return nil, nil
	}
	key := r.keyFor(host)
	// ZRangeArgs with Rev — score DESC 순으로 N 개 (가장 최근 우선).
	// ZRevRange 는 go-redis v9 에서 deprecated (Redis 6.2.0+ 권장 API).
	res, err := r.client.ZRangeArgs(ctx, goredis.ZRangeArgs{
		Key:   key,
		Start: 0,
		Stop:  int64(limit - 1),
		Rev:   true,
	}).Result()
	if err != nil {
		if errors.Is(err, goredis.Nil) {
			return nil, nil
		}
		return nil, fmt.Errorf("redis raw id tracker ZREVRANGE for host %s: %w", host, err)
	}
	return res, nil
}

func (r *rawIDTracker) RemoveByHost(ctx context.Context, host string, rawIDs []string) error {
	if host == "" || len(rawIDs) == 0 {
		return nil
	}
	key := r.keyFor(host)
	members := make([]interface{}, 0, len(rawIDs))
	for _, id := range rawIDs {
		if id == "" {
			continue
		}
		members = append(members, id)
	}
	if len(members) == 0 {
		return nil
	}
	if _, err := r.client.ZRem(ctx, key, members...).Result(); err != nil {
		return fmt.Errorf("redis raw id tracker ZREM for host %s (count=%s): %w", host, strconv.Itoa(len(members)), err)
	}
	return nil
}
