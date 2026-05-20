// ZSetIntake — Parser 의 Kafka → Redis ZSET 인입 단계 (이슈 #522 / 메타 #515 Phase 2).
//
// 단일 goroutine 이 TopicFetched Kafka 메시지를 받아 priority + arrival timestamp 로
// score 를 계산해 ZSET 에 적재 + Kafka commit. Worker pool 은 ZSET 에서 BZPOPMIN 으로
// pop 하여 처리 — Kafka partition FIFO 제약을 우회한 priority sub-ordering 보장.
//
// 흐름:
//  1. consumer.FetchMessage — Kafka 에서 1건 fetch
//  2. RawContentRef.ID 추출 (ZSET member key)
//  3. priority header 추출 (1/2/3, 잘못된 값은 normal)
//  4. zsetQueue.Push(priority, id, payload)
//  5. consumer.CommitMessages — Kafka commit
//
// 실패 정책:
//   - Unmarshal 실패: 메시지 형식이 잘못된 정상적 데이터 — commit (재시도 무의미)
//   - ZSET push 실패: Redis 일시 장애 — commit skip (Kafka 재배달로 자연 복구)
//   - Kafka commit 실패: ctx cancel 시 정상 종료 흐름, 그 외에는 WARN — 중복 push 가능성 (idempotent)
//
// 단일 goroutine 운용: priority 결정과 push 사이의 race 없음. 다중 인스턴스 환경은 Kafka
// consumer group 이 partition 자동 분배로 충분.
package worker

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"issuetracker/internal/bus"
	"issuetracker/internal/processor/fetcher/core"
	"issuetracker/pkg/logger"
	"issuetracker/pkg/queue"
)

// ZSetIntake 는 Kafka → ZSET 인입 단계의 컴포넌트입니다.
type ZSetIntake struct {
	consumer  bus.Consumer
	zsetQueue *queue.PriorityZSetQueue
	log       *logger.Logger
}

// NewZSetIntake 는 ZSetIntake 인스턴스를 생성합니다.
//
// 모든 인자 nil 불허 — wiring 단계에서 사전 검증. nil 시 nil 반환.
func NewZSetIntake(consumer bus.Consumer, zsetQueue *queue.PriorityZSetQueue, log *logger.Logger) *ZSetIntake {
	if consumer == nil || zsetQueue == nil || log == nil {
		return nil
	}
	return &ZSetIntake{
		consumer:  consumer,
		zsetQueue: zsetQueue,
		log:       log,
	}
}

// Run 은 ctx 가 cancel 될 때까지 Kafka FetchMessage → ZSET push → Kafka commit 루프를 실행합니다.
//
// goroutine 으로 시작 권장 — 본 함수는 blocking. 종료 시 consumer.Close 는 호출자 책임.
func (i *ZSetIntake) Run(ctx context.Context) {
	i.log.Info("parser zset intake started")
	defer i.log.Info("parser zset intake stopped")

	for {
		if err := ctx.Err(); err != nil {
			return
		}
		msg, err := i.consumer.FetchMessage(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			i.log.WithError(err).Error("intake fetch failed")
			// 짧은 sleep 으로 ctx cancel 흡수 + 무한 빠른 loop 회피.
			select {
			case <-ctx.Done():
				return
			case <-time.After(100 * time.Millisecond):
			}
			continue
		}
		i.handleOne(ctx, msg)
	}
}

// handleOne 은 단일 메시지를 ZSET 에 적재 + Kafka commit 합니다.
// 단위 테스트 용이성을 위해 분리.
func (i *ZSetIntake) handleOne(ctx context.Context, msg *queue.Message) {
	log := i.log.WithFields(map[string]interface{}{
		"offset":    msg.Offset,
		"partition": msg.Partition,
	})

	var ref core.RawContentRef
	if err := json.Unmarshal(msg.Value, &ref); err != nil {
		// 형식 잘못된 메시지 — Kafka 재배달로는 복구 불가. commit + skip.
		log.WithError(err).Warn("intake unmarshal failed, committing to avoid redeliver loop")
		if cerr := i.consumer.CommitMessages(ctx, msg); cerr != nil && ctx.Err() == nil {
			log.WithError(cerr).Warn("intake commit (after unmarshal failure) failed")
		}
		return
	}
	if ref.ID == "" {
		log.Warn("intake skipping message with empty RawContentRef.ID")
		if cerr := i.consumer.CommitMessages(ctx, msg); cerr != nil && ctx.Err() == nil {
			log.WithError(cerr).Warn("intake commit (empty id) failed")
		}
		return
	}

	priority := priorityFromHeader(msg.Headers)

	if err := i.zsetQueue.Push(ctx, priority, ref.ID, msg.Value); err != nil {
		// Redis 일시 장애 — commit skip 으로 Kafka 가 redeliver. 다음 polling 에서 자연 retry.
		log.WithError(err).WithField("raw_id", ref.ID).Warn("intake zset push failed, skipping commit for redeliver")
		return
	}

	if err := i.consumer.CommitMessages(ctx, msg); err != nil {
		if ctx.Err() != nil {
			return
		}
		// commit 실패해도 ZSET 에 이미 push 됨 — Kafka redeliver 시 동일 ID 재push (idempotent).
		log.WithError(err).WithField("raw_id", ref.ID).Warn("intake commit failed (zset already pushed, idempotent on redeliver)")
		return
	}

	log.WithFields(map[string]interface{}{
		"raw_id":   ref.ID,
		"priority": priority,
	}).Debug("intake pushed to zset and committed")
}

// priorityFromHeader 는 Kafka 메시지 헤더의 "priority" 값을 int 로 파싱합니다.
// 미설정 / 파싱 실패 / 범위 밖 (1~3 외) 은 PriorityNormal (2) 로 보정.
func priorityFromHeader(headers map[string]string) int {
	v, ok := headers["priority"]
	if !ok {
		return int(core.PriorityNormal)
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > 3 {
		return int(core.PriorityNormal)
	}
	return n
}
