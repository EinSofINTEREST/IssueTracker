package rule

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"issuetracker/pkg/logger"
)

// RawIDTracker 는 host 단위로 실패한 raw_id 를 timestamp 순으로 추적합니다 (이슈 #221).
//
// 단계 3 의 자동 chromedp 전환 trigger 가 PeekByHost 로 가장 최근 실패 raw_id 들을 가져와
// 새 CrawlJob 으로 republish 합니다. raw_contents 테이블에 host 컬럼이 없어서 Redis ZSET 으로
// application 레이어에서 host → raw_id (recency 순) 관계를 추적.
//
// Peek-then-Remove 패턴 (CodeRabbit 피드백):
//   - PeekByHost 는 최근 N 개를 조회만 — 실패 시 ID 가 손실되지 않음
//   - 호출자 (Upgrader) 가 publish 성공 후 RemoveByHost 로 삭제
//   - publish 실패 시 ID 가 잔존 → 다음 trigger 가 자연스럽게 재시도
//
// Set 대신 ZSET (score=timestamp) 채택 이유 (CodeRabbit 피드백):
//   - "최근 실패 raw_ids" contract 보장 (Set 은 unordered 라 stale 항목 republish 위험)
//   - ZREVRANGE 로 score DESC 순 (최근 우선) Peek 가능
type RawIDTracker interface {
	// Track 은 host 의 실패 raw_id 를 timestamp score 와 함께 등록합니다. ttl 만료 시 자연 정리.
	// host 또는 rawID 가 빈 문자열이면 noop.
	Track(ctx context.Context, host, rawID string) error

	// PeekByHost 는 host 의 가장 최근 실패 raw_id 를 최대 limit 개 조회합니다 (제거 안 함).
	// score DESC 순 — 가장 최근 항목이 첫 번째.
	// 호출자가 republish 성공 후 RemoveByHost 로 명시적 제거.
	PeekByHost(ctx context.Context, host string, limit int) ([]string, error)

	// RemoveByHost 는 ZSET 에서 지정된 raw_id 들을 제거합니다. 존재하지 않는 ID 는 무시.
	// 빈 슬라이스 / 빈 host 는 noop.
	RemoveByHost(ctx context.Context, host string, rawIDs []string) error
}

// noopRawIDTracker 는 Redis 미연결 / FETCHER_AUTO_UPGRADE_ENABLED=false 시 fallback.
type noopRawIDTracker struct{}

// NewNoopRawIDTracker 는 항상 (nil, nil) 을 반환하는 RawIDTracker 를 반환합니다.
func NewNoopRawIDTracker() RawIDTracker { return noopRawIDTracker{} }

func (noopRawIDTracker) Track(ctx context.Context, host, rawID string) error { return nil }
func (noopRawIDTracker) PeekByHost(ctx context.Context, host string, limit int) ([]string, error) {
	return nil, nil
}
func (noopRawIDTracker) RemoveByHost(ctx context.Context, host string, rawIDs []string) error {
	return nil
}

// redisRawIDTracker 는 Redis ZSET 기반 RawIDTracker 입니다.
//
// 알고리즘:
//
//   - Track:        ZADD key={prefix}:{host}, score=now-unix-nano, member=rawID + EXPIRE ttl
//   - PeekByHost:   ZREVRANGE key 0 limit-1 (score DESC = 최근 우선)
//   - RemoveByHost: ZREM key member1 member2 ...
type redisRawIDTracker struct {
	client    *goredis.Client
	keyPrefix string
	ttl       time.Duration
	log       *logger.Logger
}

// NewRedisRawIDTracker 는 Redis ZSET 기반 RawIDTracker 를 생성합니다.
//
// client 가 nil 이거나 ttl 이 비정상이면 error 반환 (이슈 #208 정책).
// keyPrefix 가 빈 문자열이면 "fetcher:failed_raws" 사용.
// ttl 은 host 별 ZSET 의 자연 정리 시간 — FailureCounter 의 window 와 동기화 권장.
func NewRedisRawIDTracker(client *goredis.Client, ttl time.Duration, keyPrefix string, log *logger.Logger) (RawIDTracker, error) {
	if client == nil {
		return nil, errors.New("rule: NewRedisRawIDTracker requires non-nil redis client")
	}
	if ttl <= 0 {
		return nil, fmt.Errorf("rule: NewRedisRawIDTracker requires positive ttl, got %s", ttl)
	}
	if keyPrefix == "" {
		keyPrefix = "fetcher:failed_raws"
	}
	return &redisRawIDTracker{
		client:    client,
		keyPrefix: keyPrefix,
		ttl:       ttl,
		log:       log,
	}, nil
}

func (r *redisRawIDTracker) keyFor(host string) string {
	return r.keyPrefix + ":" + host
}

func (r *redisRawIDTracker) Track(ctx context.Context, host, rawID string) error {
	if host == "" || rawID == "" {
		return nil
	}
	key := r.keyFor(host)
	score := float64(time.Now().UnixNano())

	pipe := r.client.Pipeline()
	pipe.ZAdd(ctx, key, goredis.Z{Score: score, Member: rawID})
	pipe.Expire(ctx, key, r.ttl)
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("redis raw id tracker ZADD for host %s: %w", host, err)
	}
	return nil
}

func (r *redisRawIDTracker) PeekByHost(ctx context.Context, host string, limit int) ([]string, error) {
	if host == "" || limit <= 0 {
		return nil, nil
	}
	key := r.keyFor(host)
	// ZREVRANGE 0 limit-1 — score DESC 순으로 N 개 (가장 최근 우선).
	res, err := r.client.ZRevRange(ctx, key, 0, int64(limit-1)).Result()
	if err != nil {
		if errors.Is(err, goredis.Nil) {
			return nil, nil
		}
		return nil, fmt.Errorf("redis raw id tracker ZREVRANGE for host %s: %w", host, err)
	}
	return res, nil
}

func (r *redisRawIDTracker) RemoveByHost(ctx context.Context, host string, rawIDs []string) error {
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
