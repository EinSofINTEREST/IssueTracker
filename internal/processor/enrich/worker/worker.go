// Package worker 는 enrich 단계의 Kafka consumer worker 를 제공합니다 (이슈 #446).
//
// 본 sub-issue 는 스켈레톤 단계 — 입력 (TopicValidated) 메시지를 그대로 TopicEnriched 로
// forward 만 수행. 실제 enrichment 로직 (claudegen 호출, 교차 검증, 외부 맥락, 신뢰도 점수) 은
// 후속 sub-issue (#447 ~ #450) 에서 점진적으로 채워집니다.
//
// 패키지 구조는 validate worker (internal/processor/validate/worker) 를 미러링.
package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"issuetracker/internal/bus"
	"issuetracker/internal/locks"
	"issuetracker/internal/processor/fetcher/core"
	"issuetracker/internal/workerpool"
	"issuetracker/pkg/logger"
	"issuetracker/pkg/queue"
)

// StageName 은 enrich 단계의 식별자입니다 (locks.StageEnricher 와 일치).
const StageName = "enricher"

// drainTimeout 은 graceful shutdown 후 Kafka publish 를 한 번 더 시도할 때 사용하는 별도
// context 의 타임아웃입니다 (at-least-once).
const drainTimeout = workerpool.DefaultDrainTimeout

// Worker 는 issuetracker.validated 토픽을 소비하여 enrich 후 issuetracker.enriched 에 발행합니다.
//
// 본 sub-issue (#446) 에서는 enrichment 미적용 — passthrough 로 메시지를 그대로 forward.
// 후속 sub-issue 에서 claudegen 호출 / DB 조회 / 신뢰도 산출이 process 메소드 내부에 점진적으로
// 추가됩니다.
type Worker struct {
	consumer    bus.Consumer
	pub         *bus.Publisher
	gate        locks.StageGate // nil 허용 → NoopStageGate 로 fallback
	workerCount int

	pool *workerpool.ConsumerPool
}

// NewWorker 는 새로운 Worker 를 생성합니다.
// pub 이 nil 이면 panic — validate worker 패턴과 일관 (fail-fast).
func NewWorker(
	consumer bus.Consumer,
	pub *bus.Publisher,
	gate locks.StageGate,
	workerCount int,
) *Worker {
	if pub == nil {
		panic("enrich.NewWorker: pub must not be nil")
	}
	if gate == nil {
		gate = locks.NewNoopStageGate()
	}
	return &Worker{
		consumer:    consumer,
		pub:         pub,
		gate:        gate,
		workerCount: workerCount,
	}
}

// Start 는 workerpool harness 를 기동합니다. 인스턴스당 1회만 호출 (2회차는 panic).
func (w *Worker) Start(ctx context.Context) {
	if w.pool != nil {
		panic("enrich worker: Start called more than once on the same instance")
	}
	plainLog := logger.FromContext(ctx)
	ctx = plainLog.WithField("worker_pool", StageName).ToContext(ctx)

	w.pool = workerpool.New(workerpool.Config{
		Consumer:     w.consumer,
		Handler:      w,
		WorkerCount:  w.workerCount,
		DrainTimeout: drainTimeout,
		Log:          plainLog,
		Name:         StageName,
	})
	w.pool.Start(ctx)
}

// Stop 은 workerpool harness 의 정상 종료를 수행합니다.
// 미기동 (Start 미호출) 상태에서는 consumer.Close 만 수행.
func (w *Worker) Stop(ctx context.Context) error {
	if w.pool == nil {
		return w.consumer.Close()
	}
	return w.pool.Stop(ctx)
}

// Handle 은 workerpool.Handler 구현 — 각 메시지마다 호출됩니다.
func (w *Worker) Handle(ctx context.Context, msg *queue.Message) {
	log := logger.FromContext(ctx)
	if err := w.process(ctx, msg); err != nil {
		if errors.Is(err, context.Canceled) {
			log.WithError(err).Debug("enrich worker canceled during shutdown")
		} else {
			log.WithError(err).Error("enrich worker failed to process message")
		}
	}
}

// process 는 단일 메시지를 처리합니다.
//
// 본 sub-issue 의 동작: 메시지 deserialize → ContentRef.URL 기반 StageGate acquire → passthrough
// publish (TopicEnriched) → commit. 후속 sub-issue 가 acquire 이후 enrichment 로직을 삽입.
func (w *Worker) process(ctx context.Context, msg *queue.Message) error {
	log := logger.FromContext(ctx)

	var pm core.ProcessingMessage
	if err := json.Unmarshal(msg.Value, &pm); err != nil {
		log.WithError(err).Error("failed to unmarshal processing message, sending to dlq")
		if dlqErr := w.sendToDLQ(ctx, msg, err); dlqErr != nil {
			return fmt.Errorf("send to dlq (unmarshal): %w", dlqErr)
		}
		return w.commit(ctx, msg)
	}

	var ref core.ContentRef
	if err := json.Unmarshal(pm.Data, &ref); err != nil {
		log.WithFields(map[string]interface{}{
			"job_id": pm.ID,
		}).WithError(err).Error("failed to unmarshal content ref, sending to dlq")
		if dlqErr := w.sendToDLQ(ctx, msg, err); dlqErr != nil {
			return fmt.Errorf("send to dlq (ref unmarshal): %w", dlqErr)
		}
		return w.commit(ctx, msg)
	}

	release, acquired, gateErr := w.gate.Acquire(ctx, ref.URL)
	if gateErr != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("enricher stage gate acquire aborted by ctx: %w", gateErr)
		}
		log.WithFields(map[string]interface{}{
			"job_id": pm.ID,
			"ref_id": ref.ID,
		}).WithError(gateErr).Warn("failed to acquire enricher stage gate, proceeding without gate")
	} else if !acquired {
		log.WithFields(map[string]interface{}{
			"job_id": pm.ID,
			"ref_id": ref.ID,
		}).Debug("enricher processing lock already held by another worker, skipping")
		return nil
	} else {
		defer release()
	}

	// 이슈 #446 — 본 sub-issue 는 passthrough 만. enrichment 로직은 후속 sub-issue 에서 삽입.
	if err := w.publishEnriched(ctx, &ref, &pm, msg); err != nil {
		if errors.Is(err, context.Canceled) {
			drainCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), drainTimeout)
			defer cancel()
			if drainErr := w.publishEnriched(drainCtx, &ref, &pm, msg); drainErr != nil {
				return fmt.Errorf("publish enriched ref %s (drain retry failed): %w", ref.ID, drainErr)
			}
			return w.commit(drainCtx, msg)
		}
		return fmt.Errorf("publish enriched ref %s: %w", ref.ID, err)
	}

	return w.commit(ctx, msg)
}

// publishEnriched 는 ContentRef 를 TopicEnriched 에 발행합니다.
// 본 sub-issue 에서는 payload 가 입력 ref 와 동일 — 후속 sub-issue 가 EnrichedFacts 등을 첨부.
func (w *Worker) publishEnriched(ctx context.Context, ref *core.ContentRef, pm *core.ProcessingMessage, orig *queue.Message) error {
	data, err := json.Marshal(ref)
	if err != nil {
		return fmt.Errorf("marshal content ref: %w", err)
	}

	out := core.ProcessingMessage{
		ID:        pm.ID,
		Timestamp: time.Now(),
		Country:   ref.Country,
		Stage:     "enriched",
		Data:      data,
		Metadata:  pm.Metadata,
	}

	outBytes, err := json.Marshal(out)
	if err != nil {
		return fmt.Errorf("marshal processing message: %w", err)
	}

	// 원본 메시지 헤더 (trace ID / request ID 등 observability 메타데이터) 를 base 로 복사 후
	// stage-specific 키만 덮어쓰기 — sendToDLQ 패턴과 일관 (gemini-review #451 반영).
	headers := make(map[string]string, len(orig.Headers)+3)
	for k, v := range orig.Headers {
		headers[k] = v
	}
	headers["source"] = ref.SourceInfo.Name
	headers["country"] = ref.Country
	headers["stage"] = "enriched"

	outMsg := queue.Message{
		Topic:   queue.TopicEnriched,
		Key:     orig.Key,
		Value:   outBytes,
		Headers: headers,
	}

	return w.pub.Forward(ctx, outMsg)
}

// sendToDLQ 는 메시지를 DLQ 토픽으로 발행합니다.
func (w *Worker) sendToDLQ(ctx context.Context, msg *queue.Message, reason error) error {
	log := logger.FromContext(ctx)

	headers := make(map[string]string, len(msg.Headers)+2)
	for k, v := range msg.Headers {
		headers[k] = v
	}
	headers["original-topic"] = msg.Topic
	headers["error"] = reason.Error()

	dlqMsg := queue.Message{
		Topic:   queue.TopicDLQ,
		Key:     msg.Key,
		Value:   msg.Value,
		Headers: headers,
	}

	err := w.pub.Forward(ctx, dlqMsg)
	if err != nil && errors.Is(err, context.Canceled) {
		drainCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), drainTimeout)
		defer cancel()
		err = w.pub.Forward(drainCtx, dlqMsg)
	}
	if err != nil {
		log.WithError(err).Error("failed to send message to dlq")
		return err
	}
	return nil
}

// commit 은 Kafka offset 을 commit 합니다 (drain 재시도 포함).
func (w *Worker) commit(ctx context.Context, msg *queue.Message) error {
	if w.pool == nil {
		return workerpool.CommitWithDrain(ctx, w.consumer, msg, drainTimeout)
	}
	return w.pool.Commit(ctx, msg)
}
