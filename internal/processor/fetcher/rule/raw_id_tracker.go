package rule

import (
	"context"
	"errors"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"issuetracker/pkg/logger"
)

// RawIDTracker 는 host 단위로 실패한 raw_id 를 추적합니다 (이슈 #221).
//
// 단계 3 의 자동 chromedp 전환 트리거가 Pop 으로 같은 host 의 실패 raw_id 들을 가져와
// 새 CrawlJob 으로 republish 합니다. raw_contents 테이블에 host 컬럼이 없어서 Redis Set
// 으로 application 레이어에서 host → raw_id 관계를 추적.
//
// 본 인터페이스는 단계 2 의 FailureCounter 와 직교적 — 카운터는 host 단위 임계값 도달
// 시점만 감지, Tracker 는 실제 republish 대상 raw_id 의 수집을 담당.
type RawIDTracker interface {
	// Track 은 host 의 실패 raw_id 를 등록합니다. ttl 만료 시 자연 정리.
	// host 또는 rawID 가 빈 문자열이면 noop.
	Track(ctx context.Context, host, rawID string) error

	// PopByHost 는 host 의 실패 raw_id 를 최대 limit 개 가져오고 Set 에서 제거합니다.
	// 단일 트리거 사이클에서 1회 호출 — 동일 raw_id 가 두 번 republish 되지 않도록 atomic.
	// Set 비어있거나 host 매칭 entry 없으면 빈 슬라이스 + nil 에러.
	PopByHost(ctx context.Context, host string, limit int) ([]string, error)
}

// noopRawIDTracker 는 Redis 미연결 / FETCHER_AUTO_UPGRADE_ENABLED=false 시 fallback.
type noopRawIDTracker struct{}

// NewNoopRawIDTracker 는 항상 (nil, nil) 을 반환하는 RawIDTracker 를 반환합니다.
func NewNoopRawIDTracker() RawIDTracker { return noopRawIDTracker{} }

func (noopRawIDTracker) Track(ctx context.Context, host, rawID string) error { return nil }
func (noopRawIDTracker) PopByHost(ctx context.Context, host string, limit int) ([]string, error) {
	return nil, nil
}

// redisRawIDTracker 는 Redis Set 기반 RawIDTracker 입니다.
//
// 알고리즘:
//
//   - Track: SADD key={prefix}:{host}, member=rawID + EXPIRE ttl (sliding)
//   - PopByHost: SPOP key 가 atomic 으로 limit 개 pop — 동시 트리거가 같은 raw_id 를
//     중복 republish 하는 race 를 SPOP 의 atomic 성으로 회피.
type redisRawIDTracker struct {
	client    *goredis.Client
	keyPrefix string
	ttl       time.Duration
	log       *logger.Logger
}

// NewRedisRawIDTracker 는 Redis Set 기반 RawIDTracker 를 생성합니다.
//
// client 가 nil 이거나 ttl 이 비정상이면 error 반환 (이슈 #208 정책).
// keyPrefix 가 빈 문자열이면 "fetcher:failed_raws" 사용.
// ttl 은 host 별 Set 의 자연 정리 시간 — FailureCounter 의 window 와 동기화 권장.
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

	pipe := r.client.Pipeline()
	pipe.SAdd(ctx, key, rawID)
	pipe.Expire(ctx, key, r.ttl)
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("redis raw id tracker SADD for host %s: %w", host, err)
	}
	return nil
}

func (r *redisRawIDTracker) PopByHost(ctx context.Context, host string, limit int) ([]string, error) {
	if host == "" || limit <= 0 {
		return nil, nil
	}
	key := r.keyFor(host)
	res, err := r.client.SPopN(ctx, key, int64(limit)).Result()
	if err != nil {
		// 키 자체가 없을 때는 redis.Nil — 빈 슬라이스 + nil 로 정규화.
		if errors.Is(err, goredis.Nil) {
			return nil, nil
		}
		return nil, fmt.Errorf("redis raw id tracker SPOP for host %s: %w", host, err)
	}
	return res, nil
}
