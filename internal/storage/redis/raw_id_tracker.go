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
//   - PeekByHost:   freshness>0 → ZRANGE BYSCORE REV (score >= now-freshness),
//     freshness=0 → ZRANGE REV (back-compat)
//   - RemoveByHost: ZREM key member1 member2 ...
//
// **이슈 #299 — freshness 의 역할**:
//
//	ZSET key-level EXPIRE 는 매 Track 마다 refresh 되어 활발한 host 의 오래된 entry 가 key 가
//	살아있는 한 만료되지 않음. 한편 raw_contents 테이블은 별도 cleanup 이 StaleTTL (default 1h)
//	기준 row 삭제 → ZSET 항목은 살아있으나 DB row 는 사라진 stale state 가 발생, Upgrader 가
//	그 stale ID 로 GetByID 호출하여 ErrNotFound (STORAGE_001) 노이즈 유발.
//	freshness 로 score-cutoff (member 별 age) 를 적용하여 read-side 에서 자동 제외.
type rawIDTracker struct {
	client    *goredis.Client
	keyPrefix string
	ttl       time.Duration
	freshness time.Duration    // 0 = score 필터 없음 (back-compat)
	now       func() time.Time // 테스트 주입용 — 일반 사용 시 nil → time.Now
	log       *logger.Logger
}

// NewRawIDTracker 는 Redis ZSET 기반 RawIDTracker 를 생성합니다 (freshness=0 — score 필터 비활성).
//
// client 가 nil 이거나 ttl 이 비정상이면 error 반환.
// keyPrefix 가 빈 문자열이면 "fetcher:failed_raws" 사용.
// ttl 은 host 별 ZSET 의 자연 정리 시간 — FailureCounter 의 window 와 동기화 권장.
//
// 신규 wiring 은 NewRawIDTrackerWithFreshness 사용 권장 — freshness 적용으로 #299 race 차단.
func NewRawIDTracker(client *goredis.Client, ttl time.Duration, keyPrefix string, log *logger.Logger) (storage.RawIDTracker, error) {
	return NewRawIDTrackerWithFreshness(client, ttl, 0, keyPrefix, log)
}

// NewRawIDTrackerWithFreshness 는 read-side score 필터링이 적용된 RawIDTracker 를 생성합니다.
//
// freshness > 0 이면 PeekByHost 가 (now - freshness) 이상인 score 의 member 만 반환 — raw_contents
// cleanup StaleTTL 과 동일/짧은 값으로 설정하여 stale ZSET 항목이 Upgrader 의 GetByID 호출로
// 누설되는 것을 차단 (이슈 #299).
//
// freshness=0 또는 음수면 score 필터 비활성 — NewRawIDTracker 와 동일 동작.
func NewRawIDTrackerWithFreshness(client *goredis.Client, ttl, freshness time.Duration, keyPrefix string, log *logger.Logger) (storage.RawIDTracker, error) {
	if client == nil {
		return nil, errors.New("redisstore: NewRawIDTracker requires non-nil redis client")
	}
	if ttl <= 0 {
		return nil, fmt.Errorf("redisstore: NewRawIDTracker requires positive ttl, got %s", ttl)
	}
	if freshness < 0 {
		freshness = 0
	}
	if keyPrefix == "" {
		keyPrefix = "fetcher:failed_raws"
	}
	return &rawIDTracker{
		client:    client,
		keyPrefix: keyPrefix,
		ttl:       ttl,
		freshness: freshness,
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

	// freshness=0 (back-compat) → 인덱스 기반 ZRANGE REV.
	if r.freshness <= 0 {
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

	// freshness>0 → score 필터 적용 (이슈 #299):
	//   ZRANGE BYSCORE REV — Rev=true 이면 Start/Stop 의 의미가 뒤집힘: Start=max, Stop=min.
	//   score (unix-nano) 가 (now - freshness) 이상인 member 만 반환.
	now := time.Now()
	if r.now != nil {
		now = r.now()
	}
	minScore := now.Add(-r.freshness).UnixNano()

	res, err := r.client.ZRangeArgs(ctx, goredis.ZRangeArgs{
		Key:     key,
		Start:   "+inf",
		Stop:    strconv.FormatInt(minScore, 10),
		ByScore: true,
		Rev:     true,
		Count:   int64(limit),
	}).Result()
	if err != nil {
		if errors.Is(err, goredis.Nil) {
			return nil, nil
		}
		return nil, fmt.Errorf("redis raw id tracker ZRANGE BYSCORE for host %s: %w", host, err)
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
