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
	"issuetracker/internal/crawler/worker"
	"issuetracker/internal/publisher"
	"issuetracker/internal/scheduler"
	pgstore "issuetracker/internal/storage/postgres"
	"issuetracker/internal/storage/service"
	"issuetracker/pkg/config"
	"issuetracker/pkg/logger"
	"issuetracker/pkg/queue"
	pkgredis "issuetracker/pkg/redis"
)

func main() {
	logConfig := logger.DefaultConfig()
	logConfig.Level = logger.LevelInfo
	logConfig.Pretty = false
	log := logger.New(logConfig)

	log.Info("starting IssueTracker crawler")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ctx = log.ToContext(ctx)

	// ── Kafka 설정 ────────────────────────────────────────────────────────
	kafkaCfg := queue.DefaultConfig()
	kafkaCfg.GroupID = queue.GroupCrawlerWorkers

	log.WithFields(map[string]interface{}{
		"brokers":  kafkaCfg.Brokers,
		"group_id": kafkaCfg.GroupID,
	}).Info("kafka configuration loaded")

	// ── Redis 연결 ────────────────────────────────────────────────────────
	redisCfg, err := config.LoadRedis()
	if err != nil {
		log.WithError(err).Fatal("failed to load redis config")
	}

	redisClient, err := pkgredis.New(ctx, redisCfg)
	if err != nil {
		log.WithError(err).Fatal("failed to connect to redis")
	}
	defer redisClient.Close()

	log.WithFields(map[string]interface{}{
		"host": redisCfg.Host,
		"port": redisCfg.Port,
	}).Info("redis connected")

	// ── 1. 발행 (Job → crawl.high / normal / low) ───────────────────────────
	producer := queue.NewProducer(kafkaCfg)
	defer producer.Close()

	// ── 2. 우선순위 결정 (어느 crawl 큐로 보낼지) ──────────────────────────────
	// CompositeResolver: 체인 순서대로 평가, 마지막엔 Default(Normal)로 fallback
	//
	//   1순위: SourcePriorityResolver — 등록된 소스 이름에 한해 결정
	//   2순위: RuleBasedPriorityResolver — 조건 규칙에 한해 결정
	//   3순위: DefaultPriorityResolver — 항상 Normal (자동 추가)
	resolver := worker.NewCompositeResolver(core.PriorityNormal)

	// 1순위: 소스 이름 기반 (등록된 소스만 결정, 미등록은 다음 resolver로)
	// TODO: 소스별 크롤러 구현 후 Register 패턴으로 우선순위 지정
	//   sourceResolver.Register("cnn-breaking", core.PriorityHigh)
	//   sourceResolver.Register("naver-archive", core.PriorityLow)
	sourceResolver := worker.NewSourcePriorityResolver(core.PriorityNormal)
	resolver.Add(sourceResolver)

	// 2순위: 규칙 기반 (매치되는 규칙이 없으면 다음 resolver로)
	// TODO: 크롤링 정책에 맞게 규칙 추가
	//   ruleResolver.AddRule(func(j *core.CrawlJob) bool { return j.RetryCount >= 2 }, core.PriorityLow)
	ruleResolver := worker.NewRuleBasedPriorityResolver(core.PriorityNormal)
	resolver.Add(ruleResolver)

	// ── 3. 소비 (crawl.high / normal / low → Worker) ──────────────────────────
	// 각 consumer가 서로 다른 토픽을 구독하므로 동일 GroupID 사용 가능
	// consumer close는 pool.Stop() 내부에서 수행합니다.
	// 여기서 defer Close를 추가하면 pool.Stop 이후 중복 close가 발생합니다.
	highConsumer := queue.NewConsumer(kafkaCfg, queue.TopicCrawlHigh)
	normalConsumer := queue.NewConsumer(kafkaCfg, queue.TopicCrawlNormal)
	lowConsumer := queue.NewConsumer(kafkaCfg, queue.TopicCrawlLow)

	// ── 4. 크롤러 Registry + DB 연결 ─────────────────────────────────────────
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

	// ── 6. Publisher (체이닝 Job 발행) ────────────────────────────────────────
	// 크롤러가 카테고리/피드 페이지에서 발견한 URL을 다음 CrawlJob으로 연결합니다.
	// resolver를 공유하여 우선순위 결정 로직을 일관되게 유지합니다.
	// Registry 등록 전에 생성하여 ChainHandler에 주입합니다.
	jobPublisher := publisher.New(producer, resolver, log)

	kr.Register(registry, core.DefaultConfig(), newsRepo, jobPublisher, log)
	us.Register(registry, core.DefaultConfig(), newsRepo, jobPublisher, log)

	contentRepo := pgstore.NewContentRepository(pool, log)
	contentSvc := service.NewContentService(contentRepo, log)

	// ── 5. Worker Pool ────────────────────────────────────────────────────────
	// 워커 수는 각 토픽의 파티션 수를 초과하지 않도록 설정
	//   High: 파티션 6 → 워커 3 (긴급, 즉시 처리)
	//   Normal: 파티션 8 → 워커 6 (일반 크롤링, 최대 처리량)
	//   Low: 파티션 4 → 워커 2 (백그라운드 수집)
	managerCfg := worker.ManagerConfig{
		High:   worker.PoolConfig{Consumer: highConsumer, WorkerCount: 3},
		Normal: worker.PoolConfig{Consumer: normalConsumer, WorkerCount: 6},
		Low:    worker.PoolConfig{Consumer: lowConsumer, WorkerCount: 2},
	}

	manager := worker.NewPoolManager(managerCfg, producer, registry, contentSvc, resolver, log)
	manager.Start(ctx)

	log.WithFields(map[string]interface{}{
		"high_workers":   managerCfg.High.WorkerCount,
		"normal_workers": managerCfg.Normal.WorkerCount,
		"low_workers":    managerCfg.Low.WorkerCount,
	}).Info("pool manager started")

	// ── 7. Scheduler (시드 Job 발행) ──────────────────────────────────────────
	// 등록된 소스의 시드 Job만 생성합니다. 체이닝은 Publisher가 담당합니다.
	schedulerCfg, err := config.LoadScheduler()
	if err != nil {
		log.WithError(err).Fatal("failed to load scheduler config")
	}

	emitter := scheduler.NewJobEmitter(producer, log)
	entries := scheduler.DefaultEntries(schedulerCfg)
	sched := scheduler.New(entries, emitter, log)
	sched.Start(ctx)

	log.WithField("entry_count", len(entries)).Info("scheduler started")

	// ── 9. Graceful Shutdown ──────────────────────────────────────────────────
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	<-sigChan
	log.Warn("shutdown signal received, draining workers...")
	cancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	// Scheduler goroutine 종료 대기
	sched.Stop()

	if err := manager.Stop(shutdownCtx); err != nil {
		log.WithError(err).Error("error during pool manager shutdown")
	}

	log.Info("crawler shutdown completed")
}
