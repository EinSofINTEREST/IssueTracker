package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"issuetracker/internal/processor/validate"
	"issuetracker/pkg/config"
	"issuetracker/pkg/logger"
	"issuetracker/pkg/queue"
)

// validateWorkerCount는 validate 단계 처리 goroutine 수입니다.
// issuetracker.normalized 토픽 파티션(32) 이하로 설정합니다.
const validateWorkerCount = 8

func main() {
	logConfig := logger.DefaultConfig()
	logConfig.Level = logger.LevelInfo
	logConfig.Pretty = false
	log := logger.New(logConfig)

	log.Info("starting IssueTracker processor")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ctx = log.ToContext(ctx)

	// ── 1. 검증 임계값 설정 로드 ──────────────────────────────────────────────
	validateCfg, err := config.LoadValidate()
	if err != nil {
		log.WithError(err).Fatal("failed to load validate config")
	}

	// ── 2. Kafka 설정 ──────────────────────────────────────────────────────────
	kafkaCfg := queue.DefaultConfig()
	kafkaCfg.GroupID = queue.GroupValidators

	log.WithFields(map[string]interface{}{
		"brokers":  kafkaCfg.Brokers,
		"group_id": kafkaCfg.GroupID,
	}).Info("kafka configuration loaded")

	// ── 3. Consumer / Producer 생성 ───────────────────────────────────────────
	// Consumer: issuetracker.normalized 구독
	// Producer: issuetracker.validated, issuetracker.dlq 발행
	consumer := queue.NewConsumer(kafkaCfg, queue.TopicNormalized)
	defer consumer.Close()

	producer := queue.NewProducer(kafkaCfg)
	defer producer.Close()

	// ── 4. Validate Worker 시작 ───────────────────────────────────────────────
	worker := validate.NewWorker(consumer, producer, validateWorkerCount, validateCfg)
	worker.Start(ctx)

	log.WithFields(map[string]interface{}{
		"worker_count": validateWorkerCount,
		"input_topic":  queue.TopicNormalized,
		"output_topic": queue.TopicValidated,
	}).Info("validate worker started")

	// ── 5. 종료 시그널 대기 ────────────────────────────────────────────────────
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	<-sigChan
	log.Warn("shutdown signal received, draining workers...")
	cancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	if err := worker.Stop(shutdownCtx); err != nil {
		log.WithError(err).Error("error during validate worker shutdown")
	}

	log.Info("processor shutdown completed")
}
