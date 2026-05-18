package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"issuetracker/internal/bus"
	"issuetracker/internal/locks"
	"issuetracker/internal/processor/fetcher/core"
	"issuetracker/internal/storage"
	"issuetracker/internal/storage/model"
	"issuetracker/internal/storage/service"
	"issuetracker/internal/workerpool"
	processorcfg "issuetracker/pkg/config/processor"
	"issuetracker/pkg/logger"
	"issuetracker/pkg/queue"
)

// StageName 은 validate 단계의 식별자입니다 (locks.StageValidator 와 일치).
// stage.go 의 Stage.Name() 과 worker.go 의 workerpool.Config.Name 가 공유 (이슈 #417).
const StageName = "validate"

// drainTimeout 은 graceful shutdown 으로 ctx 가 canceled 된 뒤 Kafka publish 를 한 번 더
// 시도할 때 사용하는 별도 context 의 타임아웃입니다.
// at-least-once 시맨틱 보장 — workerpool harness 와 동일 값.
const drainTimeout = workerpool.DefaultDrainTimeout

// Worker는 issuetracker.normalized 토픽을 소비하여 검증 후 issuetracker.validated에 발행합니다.
// ProcessingMessage.Data는 ContentRef를 담고 있으며, Worker는 ref.ID로 contents DB에서
// 전체 데이터를 조회하여 검증합니다.
// 검증 실패 시 contents에서 해당 레코드를 삭제하고 DLQ로 라우팅합니다.
//
// Worker consumes from issuetracker.normalized, fetches Content from DB via ContentRef,
// validates it, and publishes ContentRef to issuetracker.validated.
// On failure, deletes the contents record and routes to DLQ.
type Worker struct {
	consumer    bus.Consumer
	pub         *bus.Publisher
	contentSvc  service.ContentService
	gate        locks.StageGate // nil 허용 → NoopStageGate 로 fallback (이슈 #356)
	cfg         processorcfg.ValidateConfig
	workerCount int

	// pool 은 workerpool harness — Start 에서 lazy 생성. lifecycle (poll / dispatch / shutdown /
	// commit-with-drain) 위임 (이슈 #407 — 메타 #403 Sub 4).
	pool *workerpool.ConsumerPool
}

// NewWorker는 새로운 Worker를 생성합니다.
// workerCount는 동시에 실행되는 처리 goroutine 수를 결정합니다.
// gate 는 nil 허용 — nil 이면 NoopStageGate 로 fallback (단일 인스턴스 환경에서 dedup + cap 비활성).
//
// validator 결과 (passed/rejected) 는 contentSvc.UpdateValidationStatus 로 contents 테이블에
// 기록됩니다.
//
// StageGate (ProcessingLock + Semaphore 합성, 이슈 #353/#354/#356) 로 fetcher / parser / validator
// 가 동일 인터페이스로 단계별 dedup + per-stage 동시성 cap. validator 단계는 ContentRef.URL 단위로
// acquire — Kafka rebalance 시 같은 ref 가 두 worker 에 도달해도 1회만 검증.
//
// 이슈 #393 — 구 queue.Consumer / queue.Producer 직접 주입 → publisher facade 주입으로 변경.
// Kafka 구현체에 직접 의존하지 않음 (consumer 는 bus.Consumer 별칭, publish 는 pub.Forward).
func NewWorker(
	consumer bus.Consumer,
	pub *bus.Publisher,
	contentSvc service.ContentService,
	gate locks.StageGate,
	workerCount int,
	cfg processorcfg.ValidateConfig,
) *Worker {
	// pub 은 4 publish 사이트 (validated / dlq / requeue / reparse) 가 dereference 하는 hard
	// dependency. nil 주입 시 첫 publish 에서 bus.Forward 가 error 를 반환하지만, validate
	// worker 는 publish 실패 시 commit skip → Kafka 무한 재배달 — 운영 진단이 매우 어려워짐.
	// Fail-fast 가 silent failure 보다 안전 (coderabbit PR #408 피드백).
	if pub == nil {
		panic("validate.NewWorker: pub must not be nil")
	}
	if gate == nil {
		gate = locks.NewNoopStageGate()
	}
	return &Worker{
		consumer:    consumer,
		pub:         pub,
		contentSvc:  contentSvc,
		gate:        gate,
		cfg:         cfg,
		workerCount: workerCount,
	}
}

// Start 는 workerpool harness 를 기동합니다 (이슈 #407 — 메타 #403 Sub 4).
//
// harness 가 N 개 worker goroutine + poll goroutine 을 관리하며, 각 메시지마다 본 worker 의
// Handle 메소드를 호출합니다. ctx 에 worker_pool 필드 logger 가 주입되어 Handle 내부에서
// FromContext 로 접근 가능.
//
// 단일 호출 contract — 인스턴스당 1회만 호출 (gemini PR #415 패턴). 2회차 호출은 이전 pool 의
// reference 가 손실되어 자원 누수 위험이라 fail-fast panic.
func (w *Worker) Start(ctx context.Context) {
	if w.pool != nil {
		panic("validate worker: Start called more than once on the same instance")
	}
	// StageName ("validate") 사용 — locks.StageValidator / stage.go / 로그 메시지 전반과
	// 일관성 유지 (gemini PR #416 피드백).
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
//
// pool 이 미기동 (Start 미호출) 상태에서 Stop 호출 시 consumer.Close 만 수행 — Kafka 연결
// 자원 누수 방지 (gemini PR #415 패턴).
func (w *Worker) Stop(ctx context.Context) error {
	if w.pool == nil {
		return w.consumer.Close()
	}
	return w.pool.Stop(ctx)
}

// Handle 은 workerpool.Handler 구현 — 각 메시지마다 호출됩니다.
//
// process 의 결과에 따른 로깅:
//   - nil → 성공 (process 내부에서 commit 이미 수행)
//   - context.Canceled → graceful shutdown 으로 인한 정상 종료 흐름 (DEBUG 강등)
//   - 그 외 → process 실패 (ERROR). commit 안 된 메시지는 재기동 시 재소비.
func (w *Worker) Handle(ctx context.Context, msg *queue.Message) {
	log := logger.FromContext(ctx)
	if err := w.process(ctx, msg); err != nil {
		if errors.Is(err, context.Canceled) {
			log.WithError(err).Debug("validate worker canceled during shutdown")
		} else {
			log.WithError(err).Error("validate worker failed to process message")
		}
	}
}

func (w *Worker) process(ctx context.Context, msg *queue.Message) error {
	log := logger.FromContext(ctx)

	var pm core.ProcessingMessage
	if err := json.Unmarshal(msg.Value, &pm); err != nil {
		log.WithError(err).Error("failed to unmarshal processing message, sending to dlq")
		if dlqErr := w.sendToDLQ(ctx, msg, err); dlqErr != nil {
			// DLQ 실패 시 commit 하면 메시지 유실 → 에러 반환하여 재소비 보장
			return fmt.Errorf("send to dlq (unmarshal): %w", dlqErr)
		}
		return w.commit(ctx, msg)
	}

	// Data 필드에는 ContentRef가 직렬화되어 있음
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

	// validator 단계 StageGate (ProcessingLock + Semaphore 합성, 이슈 #356) —
	// 같은 ref.URL 의 동시 검증 차단 + per-stage 동시 슬롯 cap.
	// Kafka rebalance / 재배달 시 같은 ref 가 두 validator 에 도달해도 1회만 처리.
	release, acquired, gateErr := w.gate.Acquire(ctx, ref.URL)
	if gateErr != nil {
		// ctx cancel / deadline → fail-open 시 취소된 ctx 로 불필요 작업 + per-stage cap 무력화 위험.
		// commit 안 하고 종료 → Kafka redeliver 보장 (PR #358 패턴).
		if ctx.Err() != nil {
			return fmt.Errorf("validator stage gate acquire aborted by ctx: %w", gateErr)
		}
		// 그 외 인프라 에러 (예: Redis 장애) — fail-open. 다른 worker 와의 dedup 일시 비활성화.
		log.WithFields(map[string]interface{}{
			"job_id": pm.ID,
			"ref_id": ref.ID,
		}).WithError(gateErr).Warn("failed to acquire validator stage gate, proceeding without gate")
	} else if !acquired {
		log.WithFields(map[string]interface{}{
			"job_id": pm.ID,
			"ref_id": ref.ID,
		}).Debug("validator processing lock already held by another worker, skipping")
		// 다른 validator 가 처리 중 — commit 없이 종료. 처리 담당 worker 의 commit 에 의존.
		// validate 의 work runner 는 process error 시 commit 안 함 → Kafka redeliver 보장.
		return nil
	} else {
		defer release()
	}

	// DB에서 Content 조회 (content_bodies, content_meta 포함)
	content, err := w.contentSvc.GetByID(ctx, ref.ID)
	if err != nil {
		// ErrNotFound = 이미 처리되어 row 삭제된 상태 (이슈 #321).
		// Kafka at-least-once + 상위 producer retry / validate 자체 requeue loop 로 같은 ref.ID 가
		// 여러 번 deliver 될 수 있음. 첫 처리에서 validation fail (max retries) → contents.Delete →
		// commit. 그 후 도착한 duplicate 들은 row 없음 → 본 분기. **idempotent 정상 분기** —
		// DLQ 보낼 사안 아님 + ERROR 알람 false-positive 회피. info 레벨로 가시성 유지.
		if errors.Is(err, storage.ErrNotFound) {
			log.WithFields(map[string]interface{}{
				"job_id": pm.ID,
				"ref_id": ref.ID,
			}).Info("content already processed (duplicate delivery), skipping")
			return w.commit(ctx, msg)
		}
		log.WithFields(map[string]interface{}{
			"job_id": pm.ID,
			"ref_id": ref.ID,
		}).WithError(err).Error("failed to fetch content from db, sending to dlq")
		if dlqErr := w.sendToDLQ(ctx, msg, err); dlqErr != nil {
			return fmt.Errorf("send to dlq (db fetch): %w", dlqErr)
		}
		return w.commit(ctx, msg)
	}

	log.WithFields(map[string]interface{}{
		"job_id":  pm.ID,
		"ref_id":  ref.ID,
		"source":  content.SourceID,
		"country": content.Country,
	}).Debug("starting content validation")

	v := NewValidator(content.SourceType, w.cfg)

	_, err = RunValidation(ctx, v, content)
	if err != nil {
		// 본 함수의 모든 validation-fail emit 사이트가 공유하는 공통 필드 빌더.
		// CanonicalURL 이 URL 과 다를 때만 canonical_url 필드 추가 — redirect / canonical 차이 추적
		// 가능 (Copilot PR #513 피드백). URL 자체는 contents.url NOT NULL 이라 항상 채워짐.
		mkFields := func(base map[string]interface{}) map[string]interface{} {
			base["url"] = content.URL
			if content.CanonicalURL != "" && content.CanonicalURL != content.URL {
				base["canonical_url"] = content.CanonicalURL
			}
			return base
		}
		// validator → parser 재학습 트리거 분기 (이슈 #363/#364) — selector 보강으로 해결
		// 가능한 에러 (PublishedAt required / Title-Body min_length) 가 trigger 대상.
		// 본 분기는 cfg.ReparseEnabled + 횟수 < max 일 때만 동작 — 그 외에는 기존 DLQ/requeue 흐름.
		if w.cfg.ReparseEnabled && IsReparseEligible(err) {
			reparseCount := readReparseCount(msg)
			if reparseCount < core.MaxValidateReparseCount {
				// validation 실패는 콘텐츠 필터 결과 (시스템 에러 아님) — .WithError 부착 시
				// 다운스트림 대시보드/알림이 시스템 에러로 오인. 대신 reject_reason 구조화 필드로
				// 평탄화하고 url / source / country 를 일관 포함하여 사후 추적·분석 가능하도록.
				log.WithFields(mkFields(map[string]interface{}{
					"job_id":        pm.ID,
					"ref_id":        ref.ID,
					"source":        content.SourceID,
					"country":       content.Country,
					"reparse_count": reparseCount,
					"max":           core.MaxValidateReparseCount,
					"reject_reason": err.Error(),
				})).Info("content validation failed, triggering parser reparse (LLM rule relearn)")

				// reparse cycle 시작 — 발행 성공 후 contents row 정리 (순서 중요, gemini 반영).
				// Delete 가 republish 보다 먼저면 Delete 성공 + republish 실패 시 Kafka 재처리에서
				// GetByID 가 ErrNotFound 로 본 분기 자체를 못 타고 idempotent skip 되어 trigger 누락.
				// → republish 성공 후에만 Delete 호출 → republish 실패해도 Kafka 재처리 시 재시도 가능.
				if rpErr := w.republishForReparse(ctx, content, reparseCount, err, msg); rpErr != nil {
					return fmt.Errorf("republish reparse: %w", rpErr)
				}
				if delErr := w.contentSvc.Delete(ctx, ref.ID); delErr != nil {
					log.WithFields(map[string]interface{}{
						"job_id": pm.ID,
						"ref_id": ref.ID,
					}).WithError(delErr).Warn("failed to delete content after reparse publish (non-fatal — reparse cycle will overwrite)")
				}
				// commit 은 shutdown 시 ctx cancel 영향 회피 — context.WithoutCancel 로 metadata 보존 (gemini 반영).
				return w.commit(context.WithoutCancel(ctx), msg)
			}
			// max 도달 — 기존 DLQ/requeue 흐름으로 폴스루. pm.RetryCount/maxRetries 에 따라
			// DLQ 또는 requeue 가 결정되므로 "permanent DLQ" 단정 금지 (Copilot 반영).
			log.WithFields(map[string]interface{}{
				"job_id":        pm.ID,
				"ref_id":        ref.ID,
				"reparse_count": reparseCount,
				"max":           core.MaxValidateReparseCount,
			}).Info("reparse count reached max, falling through to existing DLQ/requeue flow")
		}

		// 검증 실패: contents에서 삭제 후 DLQ 또는 재큐잉.
		// 본 emit 들은 validation 필터 결과 (콘텐츠 거부) — 시스템 에러 아니므로 .WithError 미부착.
		// 모든 사이트에서 동일한 공통 필드 셋 (source / country / url / ref_id / reject_reason +
		// CanonicalURL 이 url 과 다를 때 canonical_url 추가) 을 유지 (Copilot PR #513 피드백).
		if pm.RetryCount >= maxRetries(msg) {
			log.WithFields(mkFields(map[string]interface{}{
				"job_id":        pm.ID,
				"ref_id":        ref.ID,
				"source":        content.SourceID,
				"country":       content.Country,
				"reject_reason": err.Error(),
			})).Info("content validation failed, deleting content and sending to dlq")

			// contents.Delete 직전에 reject 사유를 contents 컬럼에 기록.
			// 순서가 중요: Delete 후엔 사후 추적 단일 source 가 깨진다.
			w.recordValidationRejected(ctx, ref.ID, err)

			if delErr := w.contentSvc.Delete(ctx, ref.ID); delErr != nil {
				log.WithFields(map[string]interface{}{
					"job_id": pm.ID,
					"ref_id": ref.ID,
				}).WithError(delErr).Error("failed to delete content after validation failure")
			}
			if dlqErr := w.sendToDLQ(ctx, msg, err); dlqErr != nil {
				// DLQ 실패 시 commit 하면 메시지 유실 → 에러 반환하여 재소비 보장
				return fmt.Errorf("send to dlq (max retries): %w", dlqErr)
			}
		} else {
			log.WithFields(mkFields(map[string]interface{}{
				"job_id":        pm.ID,
				"ref_id":        ref.ID,
				"source":        content.SourceID,
				"country":       content.Country,
				"retry_count":   pm.RetryCount,
				"reject_reason": err.Error(),
			})).Info("content validation failed, requeueing")
			if rqErr := w.requeue(ctx, msg, &pm); rqErr != nil {
				// requeue 실패 시 commit 하면 재시도 기회 상실 → 에러 반환하여 재소비 보장
				return fmt.Errorf("requeue: %w", rqErr)
			}
		}
		return w.commit(ctx, msg)
	}

	// 검증 통과: contents 의 passed 기록 (publish 전에 호출하여 publish 실패 시
	// 재처리되더라도 status 는 이미 정확. UpdateValidationStatus 는 idempotent 라 재호출 안전).
	w.recordValidationPassed(ctx, ref.ID)

	if err := w.publishValidatedRef(ctx, &ref, &pm, msg); err != nil {
		// graceful shutdown 으로 ctx 가 canceled 된 경우, drain context 로 publish-then-commit 재시도.
		// 검증 자체는 이미 통과했고 DB 에는 record 가 있으므로, validated 토픽에 한 번 더
		// 발행 시도하여 다음 stage 에서 처리 가능한 상태로 만드는 것이 at-least-once 정확도를 높임.
		// drain 도 실패하면 commit 하지 않고 에러 반환 → 다음 기동 시 재소비(at-least-once 의 정상 동작).
		if errors.Is(err, context.Canceled) {
			drainCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), drainTimeout)
			defer cancel()
			if drainErr := w.publishValidatedRef(drainCtx, &ref, &pm, msg); drainErr != nil {
				return fmt.Errorf("publish validated ref %s (drain retry failed): %w", ref.ID, drainErr)
			}
			// drain publish 성공: 같은 drain context 로 commit
			return w.commit(drainCtx, msg)
		}
		return fmt.Errorf("publish validated ref %s: %w", ref.ID, err)
	}

	return w.commit(ctx, msg)
}

// publishValidatedRef는 검증을 통과한 ContentRef를 issuetracker.validated 토픽에 발행합니다.
// 다운스트림 소비자는 ref.ID로 DB에서 전체 데이터를 조회합니다.
func (w *Worker) publishValidatedRef(ctx context.Context, ref *core.ContentRef, pm *core.ProcessingMessage, orig *queue.Message) error {
	data, err := json.Marshal(ref)
	if err != nil {
		return fmt.Errorf("marshal content ref: %w", err)
	}

	out := core.ProcessingMessage{
		ID:        pm.ID,
		Timestamp: time.Now(),
		Country:   ref.Country,
		Stage:     "validated",
		Data:      data,
		Metadata:  pm.Metadata,
	}

	outBytes, err := json.Marshal(out)
	if err != nil {
		return fmt.Errorf("marshal processing message: %w", err)
	}

	outMsg := queue.Message{
		Topic: queue.TopicValidated,
		Key:   orig.Key,
		Value: outBytes,
		Headers: map[string]string{
			"source":  ref.SourceInfo.Name,
			"country": ref.Country,
			"stage":   "validated",
		},
	}

	return w.pub.Forward(ctx, outMsg)
}

// recordValidationRejected 는 validator 영구 실패 시 news_articles 에 reject 메타데이터를
// 기록합니다. 호출은 contentSvc.Delete 직전에 이루어져야 합니다.
//
// 본 메소드는 모든 실패를 best-effort 로 처리합니다 — id 미존재(ErrNotFound), DB 일시 장애 등
// 어떤 실패도 메인 처리 흐름을 차단하지 않습니다. 추적이 끊겨도 contents.Delete 와 DLQ 라우팅은
// 그대로 진행되어야 하기 때문입니다.
//
// reject_code 는 errors.As 로 *core.CrawlerError 를 추출하여 .Code (VAL_xxx) 를 사용합니다.
// reject_detail 은 err.Error() 의 message 부분 — VAL_005 의 quality breakdown 보강은
// 별도 단계 에서 진행됩니다.
func (w *Worker) recordValidationRejected(ctx context.Context, id string, reason error) {
	if id == "" {
		return
	}
	log := logger.FromContext(ctx)

	var (
		code   string
		detail = reason.Error()
	)
	var crawlerErr *core.CrawlerError
	if errors.As(reason, &crawlerErr) {
		code = crawlerErr.Code
		// CrawlerError.Error() 는 "[<cat>:<code>] <msg>" 포맷이라 reject_code 와 중복.
		// reject_detail 에는 message 본문만 저장한다 (Gemini code review 피드백).
		detail = crawlerErr.Message
	}

	if err := w.contentSvc.UpdateValidationStatus(
		ctx, id, model.ValidationStatusRejected, code, detail,
	); err != nil {
		log.WithFields(map[string]interface{}{
			"content_id":  id,
			"reject_code": code,
		}).WithError(err).Warn("failed to record validation rejection in contents")
	}
}

// recordValidationPassed 는 validator 통과 시 contents.validation_status 를
// 'passed' 로 갱신합니다. best-effort — 실패가 메인 흐름을 차단하지 않습니다.
func (w *Worker) recordValidationPassed(ctx context.Context, id string) {
	if id == "" {
		return
	}
	log := logger.FromContext(ctx)

	if err := w.contentSvc.UpdateValidationStatus(
		ctx, id, model.ValidationStatusPassed, "", "",
	); err != nil {
		log.WithFields(map[string]interface{}{
			"content_id": id,
		}).WithError(err).Warn("failed to record validation pass in contents")
	}
}

// sendToDLQ는 메시지를 DLQ 토픽으로 발행합니다.
// graceful shutdown 시 ctx.Canceled 로 첫 시도가 실패하면 drain context 로 한 번 더 시도합니다.
//
// 반환된 에러는 호출자(process)가 commit 여부를 결정하는 데 사용해야 합니다 — DLQ 발행 실패
// 상태에서 commit 하면 메시지가 유실(message loss)되므로, 에러 시에는 commit 을 건너뛰어야 합니다.
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

// requeue는 검증 실패 메시지를 normalized 토픽에 재발행합니다.
// graceful shutdown 시 ctx.Canceled 로 첫 시도가 실패하면 drain context 로 한 번 더 시도합니다.
//
// 반환된 에러는 호출자(process)가 commit 여부를 결정하는 데 사용해야 합니다 — 재큐잉 실패
// 상태에서 commit 하면 재시도 기회가 사라지므로, 에러 시에는 commit 을 건너뛰어야 합니다.
func (w *Worker) requeue(ctx context.Context, msg *queue.Message, pm *core.ProcessingMessage) error {
	log := logger.FromContext(ctx)

	pm.RetryCount++

	data, err := json.Marshal(pm)
	if err != nil {
		log.WithFields(map[string]interface{}{
			"job_id": pm.ID,
		}).WithError(err).Error("failed to marshal processing message for retry")
		return err
	}

	requeueMsg := queue.Message{
		Topic: queue.TopicNormalized,
		Key:   msg.Key,
		Value: data,
		Headers: map[string]string{
			"retry-count": fmt.Sprintf("%d", pm.RetryCount),
		},
	}

	err = w.pub.Forward(ctx, requeueMsg)
	if err != nil && errors.Is(err, context.Canceled) {
		drainCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), drainTimeout)
		defer cancel()
		err = w.pub.Forward(drainCtx, requeueMsg)
	}
	if err != nil {
		log.WithFields(map[string]interface{}{
			"job_id": pm.ID,
		}).WithError(err).Error("failed to requeue processing message for retry")
		return err
	}
	return nil
}

// reparseJobTimeoutDefault 는 원본 timeout_ms 헤더 부재 시 사용할 reparse CrawlJob timeout.
const reparseJobTimeoutDefault = 30 * time.Second

// republishForReparse 는 validate 실패 시 parser 재학습 트리거용 새 CrawlJob 을
// TopicCrawlNormal 에 발행합니다 (이슈 #364).
//
// 헤더 정책:
//   - 원본 메시지 (orig) 의 모든 헤더를 base 로 복사 후 reparse 전용 키만 덮어쓰기 —
//     timeout_ms / trace ID / 기타 observability 메타데이터 보존 (gemini 반영)
//   - validate_reparse_count = currentCount + 1
//   - validate_reparse_reason = err.Error()
//   - target_type = string(TargetTypeArticle) — fetcher chain handler 가 article 분기 사용
//     (Copilot 반영 — wire 포맷은 core.TargetType 문자열 "article" 이지 "page" 아님)
//   - crawler = content.SourceID (없으면 base 유지)
//
// Kafka key 는 job.ID — 기존 crawl 토픽 발행 패턴과 일관 (scheduler/emitter.go, fetcher/rule/upgrader.go).
//
// timeout 은 원본 timeout_ms 헤더 계승 (gemini 반영). 부재 시 reparseJobTimeoutDefault.
//
// 호출 사전 조건 (호출자가 보장):
//   - cfg.ReparseEnabled == true
//   - IsReparseEligible(err) == true
//   - currentCount < core.MaxValidateReparseCount
func (w *Worker) republishForReparse(ctx context.Context, content *core.Content, currentCount int, reason error, orig *queue.Message) error {
	log := logger.FromContext(ctx)
	nextCount := currentCount + 1

	// timeout — 원본 timeout_ms 헤더 계승, 부재 시 기본.
	jobTimeout := reparseJobTimeoutDefault
	if raw := orig.Headers[core.HeaderTimeoutMs]; raw != "" {
		if ms, perr := strconv.Atoi(raw); perr == nil && ms > 0 {
			jobTimeout = time.Duration(ms) * time.Millisecond
		}
	}

	job := core.CrawlJob{
		ID:          fmt.Sprintf("reparse-%s-%d", content.ID, nextCount),
		CrawlerName: content.SourceID,
		Target: core.Target{
			URL:  content.URL,
			Type: core.TargetTypeArticle, // validate 는 article (page) 단계만 처리
		},
		Priority:    core.PriorityNormal,
		ScheduledAt: time.Now(),
		Timeout:     jobTimeout,
		RetryCount:  0,
		MaxRetries:  3,
	}
	payload, err := json.Marshal(&job)
	if err != nil {
		return fmt.Errorf("marshal reparse crawl job: %w", err)
	}

	// 원본 헤더 base 복사 + reparse 전용 키 덮어쓰기 (gemini 반영).
	headers := make(map[string]string, len(orig.Headers)+4)
	for k, v := range orig.Headers {
		headers[k] = v
	}
	headers[core.HeaderTargetType] = string(core.TargetTypeArticle) // wire 포맷 (Copilot 반영)
	if content.SourceID != "" {
		headers[core.HeaderCrawler] = content.SourceID
	}
	headers[core.HeaderTimeoutMs] = strconv.FormatInt(jobTimeout.Milliseconds(), 10)
	headers[core.HeaderValidateReparseCount] = strconv.Itoa(nextCount)
	headers[core.HeaderValidateReparseReason] = reason.Error()

	reparseMsg := queue.Message{
		Topic:   queue.TopicCrawlNormal,
		Key:     []byte(job.ID), // 기존 crawl 토픽 패턴 일관 (Copilot 반영)
		Value:   payload,
		Headers: headers,
	}

	pubErr := w.pub.Forward(ctx, reparseMsg)
	if pubErr != nil && errors.Is(pubErr, context.Canceled) {
		drainCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), drainTimeout)
		defer cancel()
		pubErr = w.pub.Forward(drainCtx, reparseMsg)
	}
	if pubErr != nil {
		log.WithFields(map[string]interface{}{
			"content_id":    content.ID,
			"url":           content.URL,
			"reparse_count": nextCount,
		}).WithError(pubErr).Error("failed to publish reparse crawl job")
		return pubErr
	}

	log.WithFields(map[string]interface{}{
		"content_id":    content.ID,
		"url":           content.URL,
		"source":        content.SourceID,
		"reparse_count": nextCount,
		"max":           core.MaxValidateReparseCount,
		"reason":        reason.Error(),
	}).Info("published reparse crawl job for validator-driven rule relearn")
	return nil
}

// commit은 Kafka offset 을 commit 합니다.
// ctx 가 이미 canceled (graceful shutdown) 된 상태에서 commit 이 실패하면,
// drainTimeout 짜리 fresh context 로 한 번 더 시도하여 at-least-once 정확도를 높입니다.
//
// 재시도까지 실패하면 에러를 반환합니다 — 호출자(work)는 이 에러를 보고 적절한 레벨로 로깅하고,
// commit 되지 않은 offset 은 다음 worker 기동 시 재소비되어 동일 메시지가 다시 처리됩니다
// (at-least-once 의 정상 동작).
func (w *Worker) commit(ctx context.Context, msg *queue.Message) error {
	// pool 이 미기동 (Start 미호출) 케이스 — 단위 테스트가 process / commit 만 격리 호출 시
	// fallback. 실제 운영 경로에서는 Start 이후라 pool 항상 set.
	if w.pool == nil {
		return workerpool.CommitWithDrain(ctx, w.consumer, msg, drainTimeout)
	}
	return w.pool.Commit(ctx, msg)
}

// maxRetries는 메시지 헤더에서 최대 재시도 횟수를 결정합니다.
// 헤더에 없으면 기본값 3을 사용합니다.
func maxRetries(msg *queue.Message) int {
	_ = msg // 향후 헤더 기반 설정으로 확장 가능
	return 3
}
