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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
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

	// MaxSize 는 ZSET 의 최대 크기입니다 — 운영 안전망 (Redis 메모리 보호).
	// 0 또는 음수면 PriorityZSetMaxSize default (100000) — unlimited 옵션은 의도적으로 미지원.
	// 초과 시 score 가 가장 큰 (low priority + 오래된) 항목 drop (Copilot #3274731323 정합).
	MaxSize int64

	// EntryTTL 은 entry STRING 의 TTL 입니다. 0 또는 음수면 PriorityZSetEntryTTL.
	EntryTTL time.Duration

	// HeaderKeyPrefix 는 보존된 Kafka 헤더 (STRING) 의 키 접두사입니다 (이슈 #561).
	//
	// 비우면 headerPrefixMarker + EntryKeyPrefix 로 도출합니다. EntryKeyPrefix 의 하위로
	// 두지 않는 이유는 키 충돌 때문입니다 — 자세한 내용은 NewPriorityZSetQueue 참조.
	HeaderKeyPrefix string
}

// headerPrefixMarker 는 HeaderKeyPrefix 기본값의 접두사입니다 (이슈 #561).
//
// entry 키 공간 **밖** 에 두려고 뒤가 아니라 앞에 붙입니다. 뒤에 붙이면
// (EntryKeyPrefix + "hdr:") entry 키 공간의 부분집합이 되어 id 조작으로 충돌이 가능합니다.
const headerPrefixMarker = "hdr:"

// PriorityPusher 는 PriorityZSetQueue 의 Push 책임만 추상화한 인터페이스입니다 (이슈 #522).
//
// ZSetIntake 등 push-only 호출자가 *PriorityZSetQueue 대신 본 인터페이스에 의존하면 단위 테스트
// 에서 in-memory mock 으로 손쉽게 교체 가능 — Copilot #3274731563 피드백.
//
// Validate/Enrich 의 인입 단계도 동일 인터페이스를 재사용 예정 (메타 #515 Phase 2).
type PriorityPusher interface {
	Push(ctx context.Context, priority int, id string, payload []byte) error
}

// PriorityHeaderPusher 는 payload 와 함께 **Kafka 헤더까지 보존** 하는 push 를 제공합니다 (이슈 #561).
//
// PriorityPusher 를 embed 해 기존 호출자는 그대로 두고, 헤더 보존이 필요한 인입 단계만 본
// 인터페이스에 의존합니다. PriorityPusher 의 시그니처를 바꾸지 않는 이유는 pkg/ 가 공개
// 라이브러리이기 때문입니다 — 외부 구현체를 깨지 않고 기능을 넓힙니다.
type PriorityHeaderPusher interface {
	PriorityPusher
	PushWithHeaders(ctx context.Context, priority int, id string, payload []byte, headers map[string]string) error
}

// PriorityZSetQueue 는 Redis ZSET 기반 priority queue 입니다.
//
// 모든 메소드는 goroutine-safe — 내부적으로 go-redis 의 thread-safe client 사용.
type PriorityZSetQueue struct {
	rdb       *goredis.Client
	zsetKey   string
	entryKey  string
	headerKey string
	maxSize   int64
	entryTTL  time.Duration
}

// headerEnvelope 는 :hdr 키에 저장되는 값입니다 (이슈 #561).
//
// PayloadSHA 를 함께 두는 이유 — 롤링 업그레이드 중 **구 버전의 Push 는 :hdr 를 건드리지
// 않습니다.** 같은 id 를 구 버전이 재push 해 payload 만 덮어쓰면, 신 버전이 pop 할 때 이전
// 사이클의 헤더가 새 payload 에 결합되어 target_type / gate_skip_count 가 잘못 복원됩니다
// (Copilot 피드백). payload 다이제스트를 함께 저장해 두면 그 불일치를 pop 시점에 스스로
// 발견해 헤더를 버릴 수 있습니다 — 구/신 버전 어느 쪽이 payload 를 썼든 동작합니다.
type headerEnvelope struct {
	PayloadSHA string            `json:"payload_sha"`
	Headers    map[string]string `json:"headers"`
}

// payloadDigest 는 payload 의 SHA-256 앞 16바이트를 hex 로 반환합니다.
//
// 충돌 방지가 아니라 "같은 사이클의 payload 인가" 만 구분하면 되므로 전체 해시를 쓰지 않습니다.
func payloadDigest(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:16])
}

// headerKeyFor 는 entry 의 헤더 저장 키를 만듭니다 (이슈 #561).
//
// payload 와 **별도 STRING 키** 로 둡니다. entry 키에 envelope 을 씌우면 업그레이드 시점에
// Redis 에 남아 있는 기존 entry (raw payload) 가 파싱에 실패하므로, 키를 나눠 헤더 부재를
// 자연스러운 하위 호환 경로로 만듭니다 — 헤더 키가 없으면 그냥 헤더 없는 메시지입니다.
func (q *PriorityZSetQueue) headerKeyFor(id string) string {
	return q.headerKey + id
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

	headerKey := cfg.HeaderKeyPrefix
	if headerKey == "" {
		headerKey = headerPrefixMarker + cfg.EntryKeyPrefix
	}
	// 두 키 공간이 겹치면 id 를 고르는 것만으로 서로의 데이터를 덮어쓸 수 있다 (CodeRabbit 피드백).
	// 예: entry="e:" 에 id="hdr:x" 를 push 하면 entry 키가 "e:hdr:x" 가 되어, header 가
	// "e:hdr:" + "x" 를 쓸 때와 충돌한다. 한쪽이 다른 쪽의 접두사이면 그런 id 가 항상 존재하므로
	// 생성 시점에 거부한다.
	if strings.HasPrefix(headerKey, cfg.EntryKeyPrefix) || strings.HasPrefix(cfg.EntryKeyPrefix, headerKey) {
		return nil, fmt.Errorf("%w: entry/header key prefixes overlap (entry=%q header=%q)",
			ErrPriorityZSetInvalidConfig, cfg.EntryKeyPrefix, headerKey)
	}

	return &PriorityZSetQueue{
		rdb:       rdb,
		zsetKey:   cfg.ZSetKey,
		entryKey:  cfg.EntryKeyPrefix,
		headerKey: headerKey,
		maxSize:   maxSize,
		entryTTL:  entryTTL,
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
	return q.PushWithHeaders(ctx, priority, id, payload, nil)
}

// PushWithHeaders 는 Push 와 동일하되 Kafka 헤더를 함께 보존합니다 (이슈 #561).
//
// 헤더를 버리면 pop 시 재구성되는 메시지가 priority 하나만 갖게 되어, 재시도 경로에서
// target_type / crawler / timeout_ms / gate_skip_count 가 모두 사라집니다. 특히
// target_type 부재는 category job 을 article 로 떨어뜨리고, gate_skip_count 부재는
// 재큐 예산 (이슈 #540) 을 무력화해 무한 재큐를 허용합니다.
//
// headers 가 비어 있으면 헤더 키를 쓰지 않습니다 — Push 와 동일한 저장 형태.
func (q *PriorityZSetQueue) PushWithHeaders(ctx context.Context, priority int, id string, payload []byte, headers map[string]string) error {
	if id == "" {
		return errors.New("priority zset push: id required")
	}
	if len(payload) == 0 {
		return errors.New("priority zset push: empty payload")
	}
	score := priorityScore(priority, time.Now())

	// 명령 순서가 곧 가시성 순서다 (CodeRabbit 피드백).
	//
	// 평범한 pipeline 은 원자적이지 않아 다른 클라이언트의 명령이 사이에 끼어들 수 있다.
	// ZAdd 를 먼저 보내면 payload / header 가 쓰이기 전에 consumer 가 BZPOPMIN 으로 멤버를
	// 가져가 헤더 없는 (또는 payload 없는) 메시지를 받고 entry 를 지워 버린다.
	// 따라서 **payload → header → ZAdd** 순으로 보낸다. 멤버는 두 값이 모두 자리잡은 뒤에야
	// pop 가능해지므로 MULTI/EXEC 없이도 torn read 가 생기지 않는다.
	pipe := q.rdb.Pipeline()
	pipe.Set(ctx, q.entryKey+id, payload, q.entryTTL)
	if len(headers) > 0 {
		// payload 다이제스트를 함께 저장 — pop 시 다른 사이클의 헤더인지 검증한다.
		encoded, err := json.Marshal(headerEnvelope{
			PayloadSHA: payloadDigest(payload),
			Headers:    headers,
		})
		if err != nil {
			// 헤더 직렬화 실패로 메시지 자체를 버리지 않는다 — payload 는 정상이므로
			// 헤더 없이 진행한다. 남아 있을 수 있는 이전 헤더는 지운다.
			// (map[string]string 이라 실제로는 발생하지 않는 경로)
			pipe.Del(ctx, q.headerKeyFor(id))
		} else {
			pipe.Set(ctx, q.headerKeyFor(id), encoded, q.entryTTL)
		}
	} else {
		// 같은 id 로 재push 될 때 이전 사이클의 헤더가 남아 되살아나는 것을 막는다.
		pipe.Del(ctx, q.headerKeyFor(id))
	}
	// 마지막에 멤버를 노출한다 — 위 주석 참조.
	pipe.ZAdd(ctx, q.zsetKey, goredis.Z{Score: score, Member: id})
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

	// Headers 는 push 시 보존된 Kafka 헤더입니다 (이슈 #561).
	// 헤더 없이 push 된 entry 나 업그레이드 이전에 쌓인 entry 는 nil.
	Headers map[string]string
}

// Pop 은 ZSET 의 가장 낮은 score (high priority + oldest) 1건을 atomic 으로 pop 합니다.
//
// timeout 은 BZPOPMIN 의 Redis-side blocking 시간입니다. 0 이면 unlimited (ctx cancel 까지).
// ctx cancel 시 즉시 ctx.Err() 반환.
//
// 빈 큐에서 timeout 만료 시 (nil, nil) — 호출자가 polling loop 에서 재시도.
//
// entry STRING 이 만료된 경우 (TTL 초과) Payload=nil 로 반환. 호출자가 로그 + skip.
//
// 메시지 손실 방지 (Copilot #3274731302):
// BZPOPMIN 으로 ZSET 에서 먼저 제거된 후 entry GET 이 Redis 오류 (timeout 등) 로 실패하면
// 메시지가 영구 손실됨. 이를 피하기 위해 GET 실패 시 동일 score 로 ZADD 복구를 시도하고,
// 복구도 실패하면 진짜 손실로 분류하여 error 반환. 호출자가 RetryScheduler 등으로 후속 처리 가능.
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
		// entry GET 이 Redis 오류로 실패 — 이미 ZSET 에서 pop 된 ID 가 손실되는 것을 막기 위해
		// 동일 score 로 ZADD 복구 시도. 복구 성공 시 caller 가 재시도 가능.
		if zaddErr := q.rdb.ZAdd(ctx, q.zsetKey, goredis.Z{Score: res.Score, Member: id}).Err(); zaddErr != nil {
			return nil, fmt.Errorf("priority zset entry get failed and zset restore failed (id=%s): get=%w; zadd=%v", id, err, zaddErr)
		}
		return nil, fmt.Errorf("priority zset entry get (id=%s, restored to zset): %w", id, err)
	}
	// 보존된 헤더 복원 (이슈 #561).
	//
	// **전송 오류와 데이터 오류를 다르게 다룬다** (CodeRabbit 피드백).
	//
	//   - Redis 읽기 실패 (일시적): BZPOPMIN 이 이미 member 를 제거했으므로 그대로 진행하면
	//     target_type / gate_skip_count 를 영구 유실한다. member 를 되돌리고 에러를 올려
	//     호출자가 재시도하게 한다. 다음 시도에서 Redis 가 회복되면 성공한다.
	//   - 헤더 디코드 실패 (결정적): 재시도해도 같은 바이트를 다시 읽으므로 영원히 실패한다.
	//     되돌리면 그 member 가 가장 낮은 score 라 다음 BZPOPMIN 이 같은 항목을 집어
	//     **큐 머리가 entry TTL (기본 24h) 까지 막힌다.** 따라서 되돌리지 않고 헤더만
	//     버린 뒤 payload 를 전달한다 — 다이제스트 불일치와 동일한 degrade 경로다.
	var headers map[string]string
	raw, herr := q.rdb.Get(ctx, q.headerKeyFor(id)).Bytes()
	switch {
	case herr == nil && len(raw) > 0:
		var env headerEnvelope
		if uerr := json.Unmarshal(raw, &env); uerr != nil {
			// 헤더를 잃지만 메시지는 흘려보낸다 — 큐를 막는 것보다 낫다.
			headers = nil
			break
		}
		// 다른 사이클의 payload 에 붙은 헤더면 버린다 — 롤링 업그레이드 중 구 버전이
		// payload 만 덮어쓴 경우를 자가 검출한다.
		if env.PayloadSHA != "" && env.PayloadSHA != payloadDigest(payload) {
			headers = nil
		} else {
			headers = env.Headers
		}
	case herr != nil && !errors.Is(herr, goredis.Nil):
		return nil, q.restoreOnPopFailure(ctx, id, res.Score,
			fmt.Errorf("priority zset header get (id=%s): %w", id, herr))
	}

	// pop 된 후 entry 도 cleanup — TTL 로도 자연 만료되지만 명시적 삭제로 메모리 즉시 회수.
	q.rdb.Del(ctx, q.entryKey+id, q.headerKeyFor(id))

	return &PopResult{
		ID:       id,
		Score:    res.Score,
		Priority: priority,
		Payload:  payload,
		Headers:  headers,
	}, nil
}

// restoreOnPopFailure 는 pop 중 실패한 항목을 원래 score 로 ZSET 에 되돌립니다 (이슈 #561).
//
// BZPOPMIN 이 이미 member 를 제거한 뒤라, 복구하지 않으면 메시지가 영구 손실된다.
// 복구까지 실패하면 두 에러를 합쳐 반환해 호출자가 진짜 손실을 구분할 수 있게 한다.
func (q *PriorityZSetQueue) restoreOnPopFailure(ctx context.Context, id string, score float64, cause error) error {
	if zaddErr := q.rdb.ZAdd(ctx, q.zsetKey, goredis.Z{Score: score, Member: id}).Err(); zaddErr != nil {
		return fmt.Errorf("%w; zset restore failed: %v", cause, zaddErr)
	}
	return fmt.Errorf("%w (restored to zset)", cause)
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
		// 보존된 헤더를 복원한 뒤 priority 를 덮어쓴다 (이슈 #561).
		// priority 는 ZSET score 가 단일 출처 — push 당시 헤더 값보다 score 가 정확하다
		// (재push 시 score 만 갱신되는 경로가 있으므로).
		headers := make(map[string]string, len(res.Headers)+1)
		for k, v := range res.Headers {
			headers[k] = v
		}
		headers["priority"] = strconv.Itoa(res.Priority)

		return &Message{
			Topic:   c.topicLabel,
			Key:     []byte(res.ID),
			Value:   res.Payload,
			Headers: headers,
			Time:    time.Now(),
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
