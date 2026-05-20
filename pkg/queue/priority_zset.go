// Package queue 의 Redis ZSET 기반 priority queue (이슈 #522, 메타 #515 Phase 2).
//
// 단일 Kafka topic 의 partition FIFO 가 priority sub-ordering 을 제공 못 하는 한계를
// 해소하기 위한 intermediate queue. Kafka 가 transport 역할만 하고, ZSET 이 priority
// 정렬을 담당합니다.
//
// 사용 흐름 (Parser/Validate/Enrich 공통):
//
//  1. Kafka consumer 가 메시지 수신 → Push(priority, id, payload) → Kafka commit
//  2. Worker pool 이 BZPopMin 으로 가장 낮은 score (high priority + oldest) 1건 pop
//  3. Pop 실패 시 처리자 (caller) 가 bus.RetryScheduler 경유 → Kafka 로 재발행
//
// score 계산: priority(1=high/2=normal/3=low) × 1e10 + arrival_timestamp_ms
//   - 1e10 가 priority 간 간격 — arrival_ts (~1.7e12) 가 같은 priority 안에서 FIFO 결정
//   - 다른 priority 간 차이가 1e10 이상이라 high 가 항상 normal/low 보다 먼저 pop
//   - float64 mantissa 한계 2^53 (~9e15) 이내 — 정밀도 손실 없음
package queue

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// PriorityZSetEntryTTL 은 entry STRING 의 TTL 기본값입니다.
// ZSET 에 잔존하는 동안 entry 가 만료되지 않도록 충분히 큰 값.
const PriorityZSetEntryTTL = 24 * time.Hour

// PriorityZSetMaxSize 는 ZSET 의 최대 항목 수 기본값입니다 — overflow 보호.
// 초과 시 score 가 가장 큰 (low priority + oldest) 항목들 drop.
const PriorityZSetMaxSize = 100000

// priorityFactor 는 priority 와 timestamp 사이의 score 간격 계수입니다.
//
// 정렬 정책: priority 가 dominant, timestamp 가 같은 priority 내 sub-ordering.
//
// 값 결정 (gemini PR #526 high-priority 피드백):
//   - arrival_ts = UnixMilli (~1.7e12 in 2026, 100년 후도 ~5e12 미만)
//   - priorityFactor 가 arrival_ts 보다 충분히 커야 priority 가 dominant
//   - 1e10 (이전 값) 은 ~10초 이상 시간 차이만 나도 priority 영향이 ts 차이에 묻힘
//   - 1e13 으로 두면 priority 간 차이 (1e13) 가 ts 변동 (~1e12) 보다 ~10x 큼
//   - float64 mantissa 한계 9e15 — priority=3 * 1e13 + ts ≈ 3.018e13, 정밀도 안전
const priorityFactor = 1e13

// PriorityZSetConfig 는 PriorityZSetQueue 의 동작을 제어하는 설정입니다.
type PriorityZSetConfig struct {
	// ZSetKey 는 priority + timestamp 를 score 로 갖는 ZSET 의 Redis 키입니다.
	// 예: "parser:zset:queue", "validate:zset:queue", "enrich:zset:queue".
	ZSetKey string

	// EntryKeyPrefix 는 개별 entry payload (STRING) 의 키 접두사입니다.
	// 실제 키: <EntryKeyPrefix><id>. 예: "parser:zset:entry:".
	EntryKeyPrefix string

	// MaxSize 는 ZSET 의 최대 크기입니다. 0 또는 음수면 unlimited.
	// 초과 시 score 가 가장 큰 (low priority + 오래된) 항목 drop.
	MaxSize int64

	// EntryTTL 은 entry STRING 의 TTL 입니다. 0 또는 음수면 PriorityZSetEntryTTL.
	EntryTTL time.Duration
}

// PriorityZSetQueue 는 Redis ZSET 기반 priority queue 입니다.
//
// 모든 메소드는 goroutine-safe — 내부적으로 go-redis 의 thread-safe client 사용.
type PriorityZSetQueue struct {
	rdb      *goredis.Client
	zsetKey  string
	entryKey string
	maxSize  int64
	entryTTL time.Duration
}

// NewPriorityZSetQueue 는 PriorityZSetQueue 를 생성합니다.
//
// rdb 는 nil 불허 — 호출자가 사전 검증.
// cfg.ZSetKey / EntryKeyPrefix 가 빈 문자열이면 ErrPriorityZSetInvalidConfig.
func NewPriorityZSetQueue(rdb *goredis.Client, cfg PriorityZSetConfig) (*PriorityZSetQueue, error) {
	if rdb == nil {
		return nil, errors.New("priority zset queue: redis client required")
	}
	if cfg.ZSetKey == "" || cfg.EntryKeyPrefix == "" {
		return nil, ErrPriorityZSetInvalidConfig
	}
	maxSize := cfg.MaxSize
	if maxSize <= 0 {
		maxSize = PriorityZSetMaxSize
	}
	entryTTL := cfg.EntryTTL
	if entryTTL <= 0 {
		entryTTL = PriorityZSetEntryTTL
	}
	return &PriorityZSetQueue{
		rdb:      rdb,
		zsetKey:  cfg.ZSetKey,
		entryKey: cfg.EntryKeyPrefix,
		maxSize:  maxSize,
		entryTTL: entryTTL,
	}, nil
}

// ErrPriorityZSetInvalidConfig 는 NewPriorityZSetQueue 의 설정이 잘못된 경우 반환됩니다.
var ErrPriorityZSetInvalidConfig = errors.New("priority zset queue: ZSetKey and EntryKeyPrefix required")

// Push 는 id 와 payload 를 priority + 현재 시각 기준 score 로 ZSET 에 적재합니다.
//
// 동일 id 재호출 시 ZSET score / entry payload 모두 덮어쓰기 (idempotent for retry).
//
// maxSize 초과 시 ZREMRANGEBYRANK 로 score 가 가장 큰 (low priority + 오래된) 항목 drop.
// drop 실패는 ERROR 가 아닌 운영 가시성 신호로 호출자가 처리 (본 메소드는 nil 반환).
//
// priority 는 core.Priority 와 동일 매핑 (1=high / 2=normal / 3=low). 1~3 범위 밖이면
// PriorityNormal (2) 로 보정.
func (q *PriorityZSetQueue) Push(ctx context.Context, priority int, id string, payload []byte) error {
	if id == "" {
		return errors.New("priority zset push: id required")
	}
	if len(payload) == 0 {
		return errors.New("priority zset push: empty payload")
	}
	score := priorityScore(priority, time.Now())

	pipe := q.rdb.Pipeline()
	pipe.ZAdd(ctx, q.zsetKey, goredis.Z{Score: score, Member: id})
	pipe.Set(ctx, q.entryKey+id, payload, q.entryTTL)
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("priority zset push (id=%s): %w", id, err)
	}

	// maxSize 초과 시 가장 큰 score 들 drop. 실패는 무시 (운영 가시성은 호출자가 Len 으로 확인).
	if q.maxSize > 0 {
		_ = q.rdb.ZRemRangeByRank(ctx, q.zsetKey, q.maxSize, -1)
	}
	return nil
}

// PopResult 는 Pop 의 단일 반환 항목입니다.
type PopResult struct {
	ID       string
	Score    float64
	Priority int
	Payload  []byte
}

// Pop 은 ZSET 의 가장 낮은 score (high priority + oldest) 1건을 atomic 으로 pop 합니다.
//
// timeout 은 BZPOPMIN 의 Redis-side blocking 시간입니다. 0 이면 unlimited (ctx cancel 까지).
// ctx cancel 시 즉시 ctx.Err() 반환.
//
// 빈 큐에서 timeout 만료 시 (nil, nil) — 호출자가 polling loop 에서 재시도.
//
// entry STRING 이 만료된 경우 (TTL 초과) Payload=nil 로 반환. 호출자가 로그 + skip.
func (q *PriorityZSetQueue) Pop(ctx context.Context, timeout time.Duration) (*PopResult, error) {
	res, err := q.rdb.BZPopMin(ctx, timeout, q.zsetKey).Result()
	if err != nil {
		if errors.Is(err, goredis.Nil) {
			return nil, nil
		}
		return nil, fmt.Errorf("priority zset bzpop: %w", err)
	}
	if res == nil {
		return nil, nil
	}
	id, ok := res.Member.(string)
	if !ok {
		return nil, fmt.Errorf("priority zset bzpop: unexpected member type %T", res.Member)
	}

	priority := priorityFromScore(res.Score)
	payload, err := q.rdb.Get(ctx, q.entryKey+id).Bytes()
	if err != nil && !errors.Is(err, goredis.Nil) {
		return nil, fmt.Errorf("priority zset entry get (id=%s): %w", id, err)
	}
	// pop 된 후 entry 도 cleanup — TTL 로도 자연 만료되지만 명시적 삭제로 메모리 즉시 회수.
	q.rdb.Del(ctx, q.entryKey+id)

	return &PopResult{
		ID:       id,
		Score:    res.Score,
		Priority: priority,
		Payload:  payload,
	}, nil
}

// Len 은 ZSET 의 현재 항목 수를 반환합니다 (메트릭 / overflow 감지용).
func (q *PriorityZSetQueue) Len(ctx context.Context) (int64, error) {
	n, err := q.rdb.ZCard(ctx, q.zsetKey).Result()
	if err != nil {
		return 0, fmt.Errorf("priority zset len: %w", err)
	}
	return n, nil
}

// priorityScore 는 priority + arrival timestamp 로부터 ZSET score 를 계산합니다.
// priority 범위 밖 (0 / <1 / >3) 은 PriorityNormal=2 로 보정.
func priorityScore(priority int, arrival time.Time) float64 {
	p := priority
	if p < 1 || p > 3 {
		p = 2
	}
	return float64(p)*priorityFactor + float64(arrival.UnixMilli())
}

// priorityFromScore 는 ZSET score 에서 priority 를 역추출합니다.
// score / 1e10 의 정수부.
func priorityFromScore(score float64) int {
	return int(score / priorityFactor)
}

// PriorityZSetConsumer 는 PriorityZSetQueue 를 Consumer 인터페이스로 노출하는 어댑터입니다.
//
// workerpool.ConsumerPool 이 별도 변경 없이 ZSET 기반 큐를 그대로 사용 가능 — Kafka 모드와
// 동일한 폴링 / 핸들링 인프라 재활용.
//
// CommitMessages 는 no-op — ZSET 의 BZPOPMIN 이 곧 ack (pop = remove). 처리 실패 시 메시지
// 손실 방지는 호출자가 bus.RetryScheduler 경유 (Kafka 재발행 → 다음 intake → ZSET 재진입).
type PriorityZSetConsumer struct {
	queue      *PriorityZSetQueue
	topicLabel string // logical topic label for Message.Topic (e.g., "parser:zset")
	popTimeout time.Duration
}

// NewPriorityZSetConsumer 는 PriorityZSetConsumer 를 생성합니다.
// popTimeout 은 BZPOPMIN 의 Redis-side blocking 시간. 0 또는 음수면 1초 default.
func NewPriorityZSetConsumer(q *PriorityZSetQueue, topicLabel string, popTimeout time.Duration) *PriorityZSetConsumer {
	if popTimeout <= 0 {
		popTimeout = time.Second
	}
	return &PriorityZSetConsumer{
		queue:      q,
		topicLabel: topicLabel,
		popTimeout: popTimeout,
	}
}

// FetchMessage 는 ZSET 에서 1건 pop 하여 Message 로 반환합니다.
//
// 빈 큐에서 timeout 만료 시 polling loop 로 재시도 — ctx cancel 시 즉시 ctx.Err() 반환.
// entry expired (payload=nil) 인 경우 다음 항목으로 진행 (호출자가 받지 않음).
func (c *PriorityZSetConsumer) FetchMessage(ctx context.Context) (*Message, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		res, err := c.queue.Pop(ctx, c.popTimeout)
		if err != nil {
			return nil, err
		}
		if res == nil {
			// 빈 큐 — polling 재시도.
			continue
		}
		if res.Payload == nil {
			// entry TTL 만료 — payload 손실. 다음 항목으로 진행.
			continue
		}
		return &Message{
			Topic: c.topicLabel,
			Key:   []byte(res.ID),
			Value: res.Payload,
			Headers: map[string]string{
				"priority": strconv.Itoa(res.Priority),
			},
			Time: time.Now(),
		}, nil
	}
}

// CommitMessages 는 no-op 입니다 — ZSET pop 이 곧 ack.
//
// Kafka consumer 와 동일 시그니처를 만족하기 위해 존재합니다. workerpool.ConsumerPool 이
// 본 메소드를 호출해도 추가 동작 없음. 실패 retry 는 bus.RetryScheduler 경유.
func (c *PriorityZSetConsumer) CommitMessages(ctx context.Context, msgs ...*Message) error {
	return nil
}

// Close 는 no-op 입니다 — Redis 클라이언트는 외부에서 관리.
func (c *PriorityZSetConsumer) Close() error {
	return nil
}
