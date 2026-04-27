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
	"issuetracker/pkg/redis"
)

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

	// Redis 기반 JobLocker: 동일 job_id가 여러 worker/인스턴스에서 중복 처리되는 것을 방지합니다.
	// worker/manager가 JobLocker nil을 NoopJobLocker로 fallback 처리하는 설계와 일관되게,
	// Redis 초기화 실패 시에도 크롤링이 중단되지 않도록 graceful degrade합니다.
	var jobLocker crawlerWorker.JobLocker
	var urlCache crawlerWorker.URLCache
	redisCfg, err := config.LoadRedis()
	if err != nil {
		log.WithError(err).Warn("failed to load redis config, falling back to noop job locker and url cache")
	} else {
		redisClient, redisErr := redis.New(ctx, redisCfg)
		if redisErr != nil {
			log.WithError(redisErr).Warn("failed to connect to redis, falling back to noop job locker and url cache")
		} else {
			defer redisClient.Close()
			log.WithFields(map[string]interface{}{
				"host": redisCfg.Host,
				"port": redisCfg.Port,
			}).Info("redis connected for job locker and url cache")
			jobLocker = crawlerWorker.NewRedisJobLocker(redisClient, crawlerWorker.DefaultJobLockTTL)
			urlCache = crawlerWorker.NewRedisURLCache(redisClient, redisCfg.URLCacheTTL)
		}
	}

	managerCfg := crawlerWorker.ManagerConfig{
		High:      crawlerWorker.PoolConfig{Consumer: highConsumer, WorkerCount: 3},
		Normal:    crawlerWorker.PoolConfig{Consumer: normalConsumer, WorkerCount: 6},
		Low:       crawlerWorker.PoolConfig{Consumer: lowConsumer, WorkerCount: 2},
		JobLocker: jobLocker,
		URLCache:  urlCache,
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
	// 셧다운 시작 시점부터 logger 에 shutting_down=true 를 부여하여 이후 발생하는
	// 로그(특히 in-flight 작업의 context.Canceled 흔적)를 모니터링에서 필터링할 수 있게 합니다.
	// 이슈 #72 의 4번째 TODO 대응.
	log = log.WithField("shutting_down", true)
	log.Warn("shutdown signal received, draining workers...")
	cancel()

	// shutdownCtx 에도 동일한 logger 를 주입하여 Stop 내부에서 logger.FromContext 가
	// shutting_down 필드를 자동으로 상속받도록 합니다.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()
	shutdownCtx = log.ToContext(shutdownCtx)

	sched.Stop()

	if err := manager.Stop(shutdownCtx); err != nil {
		log.WithError(err).Error("error during crawler shutdown")
	}
	if err := validateWorker.Stop(shutdownCtx); err != nil {
		log.WithError(err).Error("error during validate worker shutdown")
	}

	log.Info("shutdown completed")
}
