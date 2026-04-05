package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"issuetracker/internal/crawler/core"
	"issuetracker/internal/crawler/domain/news/kr"
	"issuetracker/internal/crawler/domain/news/us"
	"issuetracker/internal/crawler/handler"
	crawlerWorker "issuetracker/internal/crawler/worker"
	"issuetracker/internal/processor/validate"
	"issuetracker/internal/publisher"
	"issuetracker/internal/scheduler"
	pgstore "issuetracker/internal/storage/postgres"
	"issuetracker/internal/storage/service"
	"issuetracker/pkg/config"
	"issuetracker/pkg/logger"
	"issuetracker/pkg/queue"
)

const validateWorkerCount = 8

func main() {
	logCfg, err := config.LoadLog()
	if err != nil {
		panic("failed to load log config: " + err.Error())
	}
	loggerCfg := logger.DefaultConfig()
	loggerCfg.Level = logger.Level(logCfg.Level)
	loggerCfg.Pretty = logCfg.Pretty
	log := logger.New(loggerCfg)

	log.Info("starting IssueTracker")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ctx = log.ToContext(ctx)

	// ══════════════════════════════════════════════════════════════════════════
	// Crawler
	// ══════════════════════════════════════════════════════════════════════════

	crawlerKafkaCfg := queue.DefaultConfig()
	crawlerKafkaCfg.GroupID = queue.GroupCrawlerWorkers

	crawlerProducer := queue.NewProducer(crawlerKafkaCfg)
	defer crawlerProducer.Close()

	resolver := crawlerWorker.NewCompositeResolver(core.PriorityNormal)
	resolver.Add(crawlerWorker.NewSourcePriorityResolver(core.PriorityNormal))
	resolver.Add(crawlerWorker.NewRuleBasedPriorityResolver(core.PriorityNormal))

	highConsumer := queue.NewConsumer(crawlerKafkaCfg, queue.TopicCrawlHigh)
	defer highConsumer.Close()
	normalConsumer := queue.NewConsumer(crawlerKafkaCfg, queue.TopicCrawlNormal)
	defer normalConsumer.Close()
	lowConsumer := queue.NewConsumer(crawlerKafkaCfg, queue.TopicCrawlLow)
	defer lowConsumer.Close()

	registry := handler.NewRegistry(log)

	dbCfg, err := config.Load()
	if err != nil {
		log.WithError(err).Fatal("failed to load db config")
	}

	pool, err := pgstore.NewPool(ctx, dbCfg, log)
	if err != nil {
		log.WithError(err).Fatal("failed to connect to db")
	}
	defer pool.Close()

	newsRepo := pgstore.NewNewsArticleRepository(pool, log)
	jobPublisher := publisher.New(crawlerProducer, resolver, log)
	kr.Register(registry, core.DefaultConfig(), newsRepo, jobPublisher, log)
	us.Register(registry, core.DefaultConfig(), newsRepo, jobPublisher, log)

	contentRepo := pgstore.NewContentRepository(pool, log)
	contentSvc := service.NewContentService(contentRepo, log)

	managerCfg := crawlerWorker.ManagerConfig{
		High:   crawlerWorker.PoolConfig{Consumer: highConsumer, WorkerCount: 3},
		Normal: crawlerWorker.PoolConfig{Consumer: normalConsumer, WorkerCount: 6},
		Low:    crawlerWorker.PoolConfig{Consumer: lowConsumer, WorkerCount: 2},
	}

	manager := crawlerWorker.NewPoolManager(managerCfg, crawlerProducer, registry, contentSvc, resolver, log)
	manager.Start(ctx)

	log.WithFields(map[string]interface{}{
		"high_workers":   managerCfg.High.WorkerCount,
		"normal_workers": managerCfg.Normal.WorkerCount,
		"low_workers":    managerCfg.Low.WorkerCount,
	}).Info("crawler pool manager started")

	// ── Scheduler (시드 Job 발행) ─────────────────────────────────────────────
	schedulerCfg, err := config.LoadScheduler()
	if err != nil {
		log.WithError(err).Fatal("failed to load scheduler config")
	}

	emitter := scheduler.NewJobEmitter(crawlerProducer, log)
	entries := scheduler.DefaultEntries(schedulerCfg)
	sched := scheduler.New(entries, emitter, log, schedulerCfg.MaxRetries)
	sched.Start(ctx)

	log.WithField("entry_count", len(entries)).Info("scheduler started")

	// ══════════════════════════════════════════════════════════════════════════
	// Processor (Validate)
	// ══════════════════════════════════════════════════════════════════════════

	validateCfg, err := config.LoadValidate()
	if err != nil {
		log.WithError(err).Fatal("failed to load validate config")
	}

	validateKafkaCfg := queue.DefaultConfig()
	validateKafkaCfg.GroupID = queue.GroupValidators

	validateConsumer := queue.NewConsumer(validateKafkaCfg, queue.TopicNormalized)
	defer validateConsumer.Close()

	validateProducer := queue.NewProducer(validateKafkaCfg)
	defer validateProducer.Close()

	validateWorker := validate.NewWorker(validateConsumer, validateProducer, contentSvc, validateWorkerCount, validateCfg)
	validateWorker.Start(ctx)

	log.WithFields(map[string]interface{}{
		"worker_count": validateWorkerCount,
		"input_topic":  queue.TopicNormalized,
		"output_topic": queue.TopicValidated,
	}).Info("validate worker started")

	// ══════════════════════════════════════════════════════════════════════════
	// 종료 시그널 대기
	// ══════════════════════════════════════════════════════════════════════════

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	<-sigChan
	log.Warn("shutdown signal received, draining workers...")
	cancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	sched.Stop()

	if err := manager.Stop(shutdownCtx); err != nil {
		log.WithError(err).Error("error during crawler shutdown")
	}
	if err := validateWorker.Stop(shutdownCtx); err != nil {
		log.WithError(err).Error("error during validate worker shutdown")
	}

	log.Info("shutdown completed")
}
