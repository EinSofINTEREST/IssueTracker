package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"issuetracker/internal/processor/validate"
	pgstore "issuetracker/internal/storage/postgres"
	"issuetracker/internal/storage/service"
	"issuetracker/pkg/config"
	"issuetracker/pkg/logger"
	"issuetracker/pkg/queue"
)

// validateWorkerCount는 validate 단계 처리 goroutine 수입니다.
// issuetracker.normalized 토픽 파티션(32) 이하로 설정합니다.
const validateWorkerCount = 8

func main() {
	log := logger.New(logger.DefaultConfig())

	logCfg, err := config.LoadLog()
	if err != nil {
		log.WithError(err).Fatal("failed to load log config")
	}
	loggerCfg := logger.DefaultConfig()
	loggerCfg.Level = logger.Level(logCfg.Level)
	loggerCfg.Pretty = logCfg.Pretty
	log = logger.New(loggerCfg)

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

	// ── 3. DB 연결 및 ContentService 생성 ────────────────────────────────────
	dbCfg, err := config.Load()
	if err != nil {
		log.WithError(err).Fatal("failed to load db config")
	}

	pool, err := pgstore.NewPool(ctx, dbCfg, log)
	if err != nil {
		log.WithError(err).Fatal("failed to connect to db")
	}
	defer pool.Close()

	contentRepo := pgstore.NewContentRepository(pool, log)
	contentSvc := service.NewContentService(contentRepo, log)

	// ── 4. Consumer / Producer 생성 ───────────────────────────────────────────
	// Consumer: issuetracker.normalized 구독
	// Producer: issuetracker.validated, issuetracker.dlq 발행
	consumer := queue.NewConsumer(kafkaCfg, queue.TopicNormalized)
	defer consumer.Close()

	producer := queue.NewProducer(kafkaCfg)
	defer producer.Close()

	// ── 5. Validate Worker 시작 ───────────────────────────────────────────────
	worker := validate.NewWorker(consumer, producer, contentSvc, validateWorkerCount, validateCfg)
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
	// 셧다운 시작 시점부터 logger 에 shutting_down=true 를 부여합니다 (이슈 #72 TODO #4).
	//
	// 적용 범위 (중요):
	//   - 본 변수 'log' 와 shutdownCtx 를 통해 전달되는 로그에만 부착됩니다 (Stop 경로).
	//   - Start(ctx) 로 이미 워커 goroutine 에 캡쳐된 ctx 의 logger 는 별개 포인터이므로
	//     in-flight 작업이 남기는 로그에는 본 필드가 부착되지 않습니다.
	//   - in-flight 로그까지 일관 필터링하려면 별도 atomic shutdownFlag 를 logger hook 으로
	//     주입하는 후속 PR 이 필요합니다 (현재 범위 외).
	log = log.WithField("shutting_down", true)
	log.Warn("shutdown signal received, draining workers...")
	cancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()
	shutdownCtx = log.ToContext(shutdownCtx)

	if err := worker.Stop(shutdownCtx); err != nil {
		log.WithError(err).Error("error during validate worker shutdown")
	}

	log.Info("processor shutdown completed")
}
