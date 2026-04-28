package validate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"issuetracker/internal/crawler/core"
	"issuetracker/internal/storage"
	"issuetracker/internal/storage/service"
	"issuetracker/pkg/config"
	"issuetracker/pkg/logger"
	"issuetracker/pkg/queue"
)

// drainTimeout은 graceful shutdown 으로 ctx 가 canceled 된 뒤 Kafka commit 또는 publish 를
// 한 번 더 시도할 때 사용하는 별도 context 의 타임아웃입니다.
// at-least-once 시맨틱 보장을 위해 ctx canceled 직후 메시지 커밋·발행을 마무리할 시간을 확보합니다.
const drainTimeout = 5 * time.Second

// Worker는 issuetracker.normalized 토픽을 소비하여 검증 후 issuetracker.validated에 발행합니다.
// ProcessingMessage.Data는 ContentRef를 담고 있으며, Worker는 ref.ID로 contents DB에서
// 전체 데이터를 조회하여 검증합니다.
// 검증 실패 시 contents에서 해당 레코드를 삭제하고 DLQ로 라우팅합니다.
//
// Worker consumes from issuetracker.normalized, fetches Content from DB via ContentRef,
// validates it, and publishes ContentRef to issuetracker.validated.
// On failure, deletes the contents record and routes to DLQ.
type Worker struct {
	consumer        queue.Consumer
	producer        queue.Producer
	contentSvc      service.ContentService
	newsArticleRepo storage.NewsArticleRepository // 이슈 #135 — validator 결과 추적 (nil 허용, 미주입 시 update 단계 skip)
	cfg             config.ValidateConfig
	workerCount     int
	jobs            chan *queue.Message
	wg              sync.WaitGroup
	pollWg          sync.WaitGroup
	pollCancel      context.CancelFunc
}

// NewWorker는 새로운 Worker를 생성합니다.
// workerCount는 동시에 실행되는 처리 goroutine 수를 결정합니다.
//
// newsArticleRepo (이슈 #135) 가 nil 이 아니면 validator 결과 (passed/rejected) 를
// news_articles 테이블에 기록합니다. nil 이면 update 단계를 건너뜁니다 — 기존 동작 보존.
func NewWorker(
	consumer queue.Consumer,
	producer queue.Producer,
	contentSvc service.ContentService,
	newsArticleRepo storage.NewsArticleRepository,
	workerCount int,
	cfg config.ValidateConfig,
) *Worker {
	return &Worker{
		consumer:        consumer,
		producer:        producer,
		contentSvc:      contentSvc,
		newsArticleRepo: newsArticleRepo,
		cfg:             cfg,
		workerCount:     workerCount,
		jobs:            make(chan *queue.Message, workerCount*2),
	}
}

// Start는 worker goroutine들과 message polling goroutine을 시작합니다.
func (w *Worker) Start(ctx context.Context) {
	for i := 0; i < w.workerCount; i++ {
		w.wg.Add(1)
		go w.work(ctx)
	}

	pollCtx, cancel := context.WithCancel(ctx)
	w.pollCancel = cancel
	w.pollWg.Add(1)
	go w.poll(pollCtx)
}

// Stop은 Worker를 정상 종료합니다.
// poll goroutine이 닫힌 jobs 채널에 송신하여 패닉이 발생하는 것을 방지하기 위해,
// poll 종료를 먼저 보장한 뒤 jobs 채널을 닫습니다.
func (w *Worker) Stop(ctx context.Context) error {
	// poll goroutine의 FetchMessage 루프를 중단시켜 jobs 송신이 완전히 멈추도록 보장
	w.pollCancel()
	w.pollWg.Wait()

	close(w.jobs)

	done := make(chan struct{})
	go func() {
		w.wg.Wait()
		close(done)
	}()

	log := logger.FromContext(ctx)

	select {
	case <-done:
		log.Info("all validate workers finished gracefully")
	case <-ctx.Done():
		log.Warn("validate worker shutdown timeout, forcing close")
	}

	return w.consumer.Close()
}

func (w *Worker) poll(ctx context.Context) {
	defer w.pollWg.Done()

	log := logger.FromContext(ctx)

	for {
		msg, err := w.consumer.FetchMessage(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.WithError(err).Error("failed to receive kafka message")
			continue
		}

		select {
		case w.jobs <- msg:
		case <-ctx.Done():
			return
		}
	}
}

func (w *Worker) work(ctx context.Context) {
	defer w.wg.Done()

	log := logger.FromContext(ctx)

	for msg := range w.jobs {
		if err := w.process(ctx, msg); err != nil {
			// graceful shutdown 으로 발생한 context.Canceled 는 운영 장애가 아니라 정상 종료 흐름이므로
			// DEBUG 로 강등하여 알림·대시보드에서 오탐을 만들지 않도록 합니다.
			// drain context 로 재시도해도 실패한 경우(드물게 broker 다운 등)도 함께 강등되며,
			// 이 경우 offset 은 commit 되지 않아 다음 기동에서 재소비되므로 메시지 유실은 발생하지 않습니다.
			if errors.Is(err, context.Canceled) {
				log.WithError(err).Debug("validate worker canceled during shutdown")
			} else {
				log.WithError(err).Error("validate worker failed to process message")
			}
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

	// DB에서 Content 조회 (content_bodies, content_meta 포함)
	content, err := w.contentSvc.GetByID(ctx, ref.ID)
	if err != nil {
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
	cp := NewContentProcessor(v)

	_, err = cp.Process(ctx, content)
	if err != nil {
		// 검증 실패: contents에서 삭제 후 DLQ 또는 재큐잉
		if pm.RetryCount >= maxRetries(msg) {
			log.WithFields(map[string]interface{}{
				"job_id":  pm.ID,
				"ref_id":  ref.ID,
				"source":  content.SourceID,
				"country": content.Country,
			}).WithError(err).Info("content validation failed, deleting content and sending to dlq")

			// 이슈 #135 — contents.Delete 직전에 news_articles 에 reject 사유 기록.
			// 순서가 중요: Delete 후엔 ref.URL 확보 어렵고, 사후 추적 단일 source 가 깨진다.
			w.recordValidationRejected(ctx, content.URL, err)

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
			log.WithFields(map[string]interface{}{
				"job_id":      pm.ID,
				"retry_count": pm.RetryCount,
			}).WithError(err).Info("content validation failed, requeueing")
			if rqErr := w.requeue(ctx, msg, &pm); rqErr != nil {
				// requeue 실패 시 commit 하면 재시도 기회 상실 → 에러 반환하여 재소비 보장
				return fmt.Errorf("requeue: %w", rqErr)
			}
		}
		return w.commit(ctx, msg)
	}

	// 이슈 #135 — 검증 통과: news_articles 에 passed 기록 (publish 전에 호출하여 publish 실패 시
	// 재처리되더라도 status 는 이미 정확. UpdateValidationStatus 는 idempotent 라 재호출 안전).
	w.recordValidationPassed(ctx, content.URL)

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

	return w.producer.Publish(ctx, outMsg)
}

// recordValidationRejected 는 validator 영구 실패 시 news_articles 에 reject 메타데이터를
// 기록합니다 (이슈 #135). 호출은 contentSvc.Delete 직전에 이루어져야 합니다.
//
// 본 메소드는 모든 실패를 best-effort 로 처리합니다 — repo 미주입(nil), URL 미존재(ErrNotFound),
// DB 일시 장애 등 어떤 실패도 메인 처리 흐름을 차단하지 않습니다. 추적이 끊겨도 contents.Delete
// 와 DLQ 라우팅은 그대로 진행되어야 하기 때문입니다.
//
// reject_code 는 errors.As 로 *core.CrawlerError 를 추출하여 .Code (VAL_xxx) 를 사용합니다.
// reject_detail 은 err.Error() 의 message 부분 — VAL_005 의 quality breakdown 보강은
// 별도 단계 (이슈 #135 P0-4) 에서 진행됩니다.
func (w *Worker) recordValidationRejected(ctx context.Context, url string, reason error) {
	if w.newsArticleRepo == nil || url == "" {
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
	}

	if err := w.newsArticleRepo.UpdateValidationStatus(
		ctx, url, storage.ValidationStatusRejected, code, detail,
	); err != nil {
		log.WithFields(map[string]interface{}{
			"url":         url,
			"reject_code": code,
		}).WithError(err).Warn("failed to record validation rejection in news_articles")
	}
}

// recordValidationPassed 는 validator 통과 시 news_articles.validation_status 를
// 'passed' 로 갱신합니다 (이슈 #135). best-effort — 실패가 메인 흐름을 차단하지 않습니다.
func (w *Worker) recordValidationPassed(ctx context.Context, url string) {
	if w.newsArticleRepo == nil || url == "" {
		return
	}
	log := logger.FromContext(ctx)

	if err := w.newsArticleRepo.UpdateValidationStatus(
		ctx, url, storage.ValidationStatusPassed, "", "",
	); err != nil {
		log.WithFields(map[string]interface{}{
			"url": url,
		}).WithError(err).Warn("failed to record validation pass in news_articles")
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

	err := w.producer.Publish(ctx, dlqMsg)
	if err != nil && errors.Is(err, context.Canceled) {
		drainCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), drainTimeout)
		defer cancel()
		err = w.producer.Publish(drainCtx, dlqMsg)
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

	err = w.producer.Publish(ctx, requeueMsg)
	if err != nil && errors.Is(err, context.Canceled) {
		drainCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), drainTimeout)
		defer cancel()
		err = w.producer.Publish(drainCtx, requeueMsg)
	}
	if err != nil {
		log.WithFields(map[string]interface{}{
			"job_id": pm.ID,
		}).WithError(err).Error("failed to requeue processing message for retry")
		return err
	}
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
	err := w.consumer.CommitMessages(ctx, msg)
	if err == nil {
		return nil
	}

	// graceful shutdown 으로 ctx 가 canceled 된 경우, drain context 로 한 번 더 시도.
	// context.WithoutCancel 로 cancellation 만 분리하고 trace ID·logger 필드 등 메타데이터는 보존.
	if errors.Is(err, context.Canceled) {
		drainCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), drainTimeout)
		defer cancel()
		if retryErr := w.consumer.CommitMessages(drainCtx, msg); retryErr == nil {
			return nil
		} else {
			return fmt.Errorf("commit offset (drain retry failed): %w", retryErr)
		}
	}

	return fmt.Errorf("commit offset: %w", err)
}

// maxRetries는 메시지 헤더에서 최대 재시도 횟수를 결정합니다.
// 헤더에 없으면 기본값 3을 사용합니다.
func maxRetries(msg *queue.Message) int {
	_ = msg // 향후 헤더 기반 설정으로 확장 가능
	return 3
}
