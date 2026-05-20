// ZSetIntake — Validate 의 Kafka → Redis ZSET 인입 단계 (이슈 #523 / 메타 #515 Phase 2).
//
// 단일 goroutine 이 TopicNormalized Kafka 메시지를 받아 priority + arrival timestamp 로
// score 를 계산해 ZSET 에 적재 + Kafka commit. Worker pool 은 ZSET 에서 BZPOPMIN 으로
// pop 하여 처리 — Kafka partition FIFO 제약을 우회한 priority sub-ordering 보장.
//
// 흐름:
//  1. consumer.FetchMessage — Kafka 에서 1건 fetch
//  2. ProcessingMessage / ContentRef.ID 추출 (ZSET member key)
//  3. priority header 추출 (1/2/3, 잘못된 값은 normal)
//  4. zsetQueue.Push(priority, id, payload)
//  5. consumer.CommitMessages — Kafka commit
//
// 실패 정책 (Parser ZSetIntake 와 동일):
//   - Unmarshal 실패: 메시지 형식 깨짐 — commit (재시도 무의미)
//   - 빈 ID: commit + skip
//   - ZSET push 실패: commit skip (Kafka redeliver)
//   - Kafka commit 실패: ZSET 에 이미 push, redeliver 시 동일 ID 재push (idempotent)
package worker

import (
	"context"
	"encoding/json"
	"time"

	"issuetracker/internal/bus"
	"issuetracker/internal/processor/fetcher/core"
	"issuetracker/pkg/logger"
	"issuetracker/pkg/queue"
)

// ZSetIntake 는 Kafka → ZSET 인입 단계의 컴포넌트입니다.
//
// zsetQueue 는 queue.PriorityPusher 인터페이스 — *queue.PriorityZSetQueue 가 자동 만족하며,
// 단위 테스트에서는 in-memory stub 으로 교체 가능.
type ZSetIntake struct {
	consumer  bus.Consumer
	zsetQueue queue.PriorityPusher
	log       *logger.Logger
}

// NewZSetIntake 는 ZSetIntake 인스턴스를 생성합니다.
//
// 모든 인자 nil 불허 — wiring 단계에서 사전 검증. nil 시 nil 반환.
func NewZSetIntake(consumer bus.Consumer, zsetQueue queue.PriorityPusher, log *logger.Logger) *ZSetIntake {
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
// goroutine 으로 시작 권장 — 본 함수는 blocking. 종료 시 defer consumer.Close() 로 Kafka
// reader 자원 누수 방지.
func (i *ZSetIntake) Run(ctx context.Context) {
	i.log.Info("validate zset intake started")
	defer func() {
		if err := i.consumer.Close(); err != nil {
			i.log.WithError(err).Warn("intake consumer close failed")
		}
		i.log.Info("validate zset intake stopped")
	}()

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

// HandleOneForTest 는 handleOne 의 테스트 전용 export 입니다.
func (i *ZSetIntake) HandleOneForTest(ctx context.Context, msg *queue.Message) {
	i.handleOne(ctx, msg)
}

// handleOne 은 단일 메시지를 ZSET 에 적재 + Kafka commit 합니다.
//
// Validate 는 ProcessingMessage 한 단계 wrap 위에 ContentRef 가 들어 있으므로 인입 시점에는
// ContentRef.ID 를 ZSET key 로 사용 — Worker.process 가 동일 형식으로 다시 unmarshal 함.
func (i *ZSetIntake) handleOne(ctx context.Context, msg *queue.Message) {
	log := i.log.WithFields(map[string]interface{}{
		"offset":    msg.Offset,
		"partition": msg.Partition,
	})

	var pm core.ProcessingMessage
	if err := json.Unmarshal(msg.Value, &pm); err != nil {
		log.WithError(err).Warn("intake unmarshal failed, committing to avoid redeliver loop")
		if cerr := i.consumer.CommitMessages(ctx, msg); cerr != nil && ctx.Err() == nil {
			log.WithError(cerr).Warn("intake commit (after unmarshal failure) failed")
		}
		return
	}

	// ContentRef.ID 를 ZSET member 로 사용 — 동일 ref 가 재차 도착해도 idempotent.
	var ref core.ContentRef
	if err := json.Unmarshal(pm.Data, &ref); err != nil {
		log.WithError(err).Warn("intake content ref unmarshal failed, committing")
		if cerr := i.consumer.CommitMessages(ctx, msg); cerr != nil && ctx.Err() == nil {
			log.WithError(cerr).Warn("intake commit (after ref unmarshal failure) failed")
		}
		return
	}
	if ref.ID == "" {
		log.Warn("intake skipping message with empty ContentRef.ID")
		if cerr := i.consumer.CommitMessages(ctx, msg); cerr != nil && ctx.Err() == nil {
			log.WithError(cerr).Warn("intake commit (empty id) failed")
		}
		return
	}

	priority := PriorityFromHeader(msg.Headers)

	if err := i.zsetQueue.Push(ctx, priority, ref.ID, msg.Value); err != nil {
		log.WithError(err).WithField("ref_id", ref.ID).Warn("intake zset push failed, skipping commit for redeliver")
		return
	}

	if err := i.consumer.CommitMessages(ctx, msg); err != nil {
		if ctx.Err() != nil {
			return
		}
		log.WithError(err).WithField("ref_id", ref.ID).Warn("intake commit failed (zset already pushed, idempotent on redeliver)")
		return
	}

	log.WithFields(map[string]interface{}{
		"ref_id":   ref.ID,
		"priority": priority,
	}).Debug("intake pushed to zset and committed")
}
