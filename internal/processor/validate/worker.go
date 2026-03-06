package validate

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"issuetracker/internal/crawler/core"
	"issuetracker/internal/storage/service"
	"issuetracker/pkg/config"
	"issuetracker/pkg/logger"
	"issuetracker/pkg/queue"
)

// Worker는 issuetracker.normalized 토픽을 소비하여 검증 후 issuetracker.validated에 발행합니다.
// ProcessingMessage.Data는 ContentRef를 담고 있으며, Worker는 ref.ID로 contents DB에서
// 전체 데이터를 조회하여 검증합니다.
// 검증 실패 시 contents에서 해당 레코드를 삭제하고 DLQ로 라우팅합니다.
//
// Worker consumes from issuetracker.normalized, fetches Content from DB via ContentRef,
// validates it, and publishes ContentRef to issuetracker.validated.
// On failure, deletes the contents record and routes to DLQ.
type Worker struct {
	consumer    queue.Consumer
	producer    queue.Producer
	contentSvc  service.ContentService
	cfg         config.ValidateConfig
	workerCount int
	jobs        chan *queue.Message
	wg          sync.WaitGroup
	pollWg      sync.WaitGroup
	pollCancel  context.CancelFunc
}

// NewWorker는 새로운 Worker를 생성합니다.
// workerCount는 동시에 실행되는 처리 goroutine 수를 결정합니다.
func NewWorker(
	consumer queue.Consumer,
	producer queue.Producer,
	contentSvc service.ContentService,
	workerCount int,
	cfg config.ValidateConfig,
) *Worker {
	return &Worker{
		consumer:    consumer,
		producer:    producer,
		contentSvc:  contentSvc,
		cfg:         cfg,
		workerCount: workerCount,
		jobs:        make(chan *queue.Message, workerCount*2),
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
			log.WithError(err).Error("validate worker failed to process message")
		}
	}
}

func (w *Worker) process(ctx context.Context, msg *queue.Message) error {
	log := logger.FromContext(ctx)

	var pm core.ProcessingMessage
	if err := json.Unmarshal(msg.Value, &pm); err != nil {
		log.WithError(err).Error("failed to unmarshal processing message, sending to dlq")
		w.sendToDLQ(ctx, msg, err)
		return w.commit(ctx, msg)
	}

	// Data 필드에는 ContentRef가 직렬화되어 있음
	var ref core.ContentRef
	if err := json.Unmarshal(pm.Data, &ref); err != nil {
		log.WithFields(map[string]interface{}{
			"job_id": pm.ID,
		}).WithError(err).Error("failed to unmarshal content ref, sending to dlq")
		w.sendToDLQ(ctx, msg, err)
		return w.commit(ctx, msg)
	}

	// DB에서 Content 조회 (content_bodies, content_meta 포함)
	content, err := w.contentSvc.GetByID(ctx, ref.ID)
	if err != nil {
		log.WithFields(map[string]interface{}{
			"job_id": pm.ID,
			"ref_id": ref.ID,
		}).WithError(err).Error("failed to fetch content from db, sending to dlq")
		w.sendToDLQ(ctx, msg, err)
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
			}).WithError(err).Error("content validation failed, deleting content and sending to dlq")
			_ = w.contentSvc.Delete(ctx, ref.ID)
			w.sendToDLQ(ctx, msg, err)
		} else {
			log.WithFields(map[string]interface{}{
				"job_id":      pm.ID,
				"retry_count": pm.RetryCount,
			}).WithError(err).Warn("content validation failed, requeueing")
			w.requeue(ctx, msg, &pm)
		}
		return w.commit(ctx, msg)
	}

	if err := w.publishValidatedRef(ctx, &ref, &pm, msg); err != nil {
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

func (w *Worker) sendToDLQ(ctx context.Context, msg *queue.Message, reason error) {
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

	if err := w.producer.Publish(ctx, dlqMsg); err != nil {
		log.WithError(err).Error("failed to send message to dlq")
	}
}

func (w *Worker) requeue(ctx context.Context, msg *queue.Message, pm *core.ProcessingMessage) {
	log := logger.FromContext(ctx)

	pm.RetryCount++

	data, err := json.Marshal(pm)
	if err != nil {
		log.WithFields(map[string]interface{}{
			"job_id": pm.ID,
		}).WithError(err).Error("failed to marshal processing message for retry")
		return
	}

	requeueMsg := queue.Message{
		Topic: queue.TopicNormalized,
		Key:   msg.Key,
		Value: data,
		Headers: map[string]string{
			"retry-count": fmt.Sprintf("%d", pm.RetryCount),
		},
	}

	if err := w.producer.Publish(ctx, requeueMsg); err != nil {
		log.WithFields(map[string]interface{}{
			"job_id": pm.ID,
		}).WithError(err).Error("failed to requeue processing message for retry")
	}
}

func (w *Worker) commit(ctx context.Context, msg *queue.Message) error {
	if err := w.consumer.CommitMessages(ctx, msg); err != nil {
		return fmt.Errorf("commit offset: %w", err)
	}
	return nil
}

// maxRetries는 메시지 헤더에서 최대 재시도 횟수를 결정합니다.
// 헤더에 없으면 기본값 3을 사용합니다.
func maxRetries(msg *queue.Message) int {
	_ = msg // 향후 헤더 기반 설정으로 확장 가능
	return 3
}
