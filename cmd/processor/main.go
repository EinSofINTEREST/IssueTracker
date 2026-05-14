package main

import (
	"context"
	appcfg "issuetracker/pkg/config/app"
	processorcfg "issuetracker/pkg/config/processor"
	storagecfg "issuetracker/pkg/config/storage"
	"os"
	"os/signal"
	"syscall"

	"issuetracker/internal/bus"
	"issuetracker/internal/locks"
	validateWorker "issuetracker/internal/processor/validate/worker"
	"issuetracker/internal/storage/decorator"
	pgstore "issuetracker/internal/storage/postgres"
	"issuetracker/internal/storage/service"
	"issuetracker/pkg/logger"
	"issuetracker/pkg/metrics"
	"issuetracker/pkg/queue"
)

// validateWorkerCount는 validate 단계 처리 goroutine 수입니다.
// issuetracker.normalized 토픽 파티션(32) 이하로 설정합니다.
const validateWorkerCount = 8

func main() {
	log := logger.New(logger.DefaultConfig())

	logCfg, err := appcfg.LoadLog()
	if err != nil {
		log.WithError(err).Fatal("failed to load log config")
	}
	loggerCfg := logger.DefaultConfig()
	loggerCfg.Level = logger.Level(logCfg.Level)
	loggerCfg.Pretty = logCfg.Pretty
	log = logger.New(loggerCfg)

	log.Info("starting IssueTracker processor")

	shutdownCfg, err := appcfg.LoadShutdown()
	if err != nil {
		log.WithError(err).Fatal("failed to load shutdown config")
	}
	log.WithField("shutdown_timeout", shutdownCfg.Timeout.String()).Info("shutdown timeout loaded")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ctx = log.ToContext(ctx)

	// ── Metrics endpoint ──────────────────────────────────────────
	metricsCfg, err := appcfg.LoadMetrics()
	if err != nil {
		log.WithError(err).Fatal("failed to load metrics config")
	}
	metricsRegistry := metrics.NewRegistry()
	if _, err := metrics.Serve(ctx, metricsCfg.Addr, metricsRegistry, log); err != nil {
		log.WithError(err).Fatal("failed to start metrics endpoint")
	}

	// ── 1. 검증 임계값 설정 로드 ──────────────────────────────────────────────
	validateCfg, err := processorcfg.LoadValidate()
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
	dbCfg, err := storagecfg.Load()
	if err != nil {
		log.WithError(err).Fatal("failed to load db config")
	}

	pool, err := pgstore.NewPool(ctx, dbCfg, log)
	if err != nil {
		log.WithError(err).Fatal("failed to connect to db")
	}
	defer pool.Close()

	// query-level timeout 적용 (이슈 #427).
	contentRepo := decorator.WrapContentWithTimeout(pgstore.NewContentRepository(pool, log), dbCfg.QueryTimeout)
	contentSvc := service.NewContentService(contentRepo, log)

	// ── 4. Consumer / Producer 생성 ───────────────────────────────────────────
	// Consumer: issuetracker.normalized 구독
	// Producer: issuetracker.validated, issuetracker.dlq 발행
	consumer := queue.NewConsumer(kafkaCfg, queue.TopicNormalized)
	defer consumer.Close()

	producer := queue.NewProducer(kafkaCfg)
	defer producer.Close()

	// 이슈 #393 — validate worker 가 publisher facade 의존으로 변경. processor 단독 실행은
	// resolver / guard 가 필요 없으므로 nil resolver 로 thin publisher 만 생성.
	pub := bus.New(producer, nil, log)

	// ── 5. Validate Worker 시작 ───────────────────────────────────────────────
	// validator 결과 (passed/rejected) 는 contentSvc.UpdateValidationStatus 로 contents 에 기록.
	// processor 단독 실행은 dev/test 시나리오 — Redis wiring 없이 NoopStageGate 사용 (dedup + cap 비활성).
	// 다중 인스턴스 운영은 cmd/issuetracker 통합 바이너리에서 Redis 기반 ProcessingLock 합성된 StageGate 공유.
	worker := validateWorker.NewWorker(consumer, pub, contentSvc, locks.NewNoopStageGate(), validateWorkerCount, validateCfg)
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
	// 셧다운 시작 시점부터 logger 에 shutting_down=true 를 부여합니다.
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

	// WithoutCancel(ctx) 로 parent cancellation 은 분리하되 ctx values (logger 등) 보존.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownCfg.Timeout)
	defer shutdownCancel()
	shutdownCtx = log.ToContext(shutdownCtx)

	if err := worker.Stop(shutdownCtx); err != nil {
		log.WithError(err).Error("error during validate worker shutdown")
	}

	log.Info("processor shutdown completed")
}
