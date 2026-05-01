package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"issuetracker/internal/crawler/core"
	"issuetracker/internal/crawler/domain/general/sources/kr"
	"issuetracker/internal/crawler/domain/general/sources/us"
	"issuetracker/internal/crawler/handler"
	"issuetracker/internal/crawler/parser/rule"
	"issuetracker/internal/crawler/parser/rule/llmgen"
	"issuetracker/internal/crawler/parser/rule/refiner"
	crawlerWorker "issuetracker/internal/crawler/worker"
	parserWorker "issuetracker/internal/parser/worker"
	"issuetracker/internal/processor/validate"
	"issuetracker/internal/publisher"
	"issuetracker/internal/scheduler"
	"issuetracker/internal/storage"
	pgstore "issuetracker/internal/storage/postgres"
	"issuetracker/internal/storage/service"
	"issuetracker/pkg/config"
	"issuetracker/pkg/links"
	"issuetracker/pkg/llm"
	"issuetracker/pkg/llm/chain"
	"issuetracker/pkg/llm/policy"
	_ "issuetracker/pkg/llm/providers"
	"issuetracker/pkg/logger"
	"issuetracker/pkg/metrics"
	"issuetracker/pkg/queue"
	"issuetracker/pkg/redis"
)

const (
	validateWorkerCount = 8
	// parserWorkerCount: TopicFetched consumer group (issuetracker-parsers) 의 worker 수.
	// fetcher worker 와 독립 — chromedp/LLM 등으로 parser 가 무거워질 때 별도 스케일 (이슈 #134).
	parserWorkerCount = 6
)

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

	// ── Metrics endpoint (이슈 #165) ──────────────────────────────────────────
	// METRICS_ADDR 빈 값이면 endpoint 비활성화. default ":9090".
	metricsCfg, err := config.LoadMetrics()
	if err != nil {
		log.WithError(err).Fatal("failed to load metrics config")
	}
	metricsRegistry := metrics.NewRegistry()
	if _, err := metrics.Serve(ctx, metricsCfg.Addr, metricsRegistry, log); err != nil {
		log.WithError(err).Fatal("failed to start metrics endpoint")
	}

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

	jobPublisher := publisher.New(crawlerProducer, resolver, log)

	// rule.Parser: parsing_rules 테이블 기반 단일 파서 엔진 (이슈 #100 / #139).
	// 사이트별 NaverParser/CNNParser/... 를 대체 — 모든 사이트가 본 단일 인스턴스를 공유.
	parsingRuleRepo := pgstore.NewParsingRuleRepository(pool, log)
	ruleResolver := rule.NewResolver(parsingRuleRepo)
	ruleParser := rule.NewParser(ruleResolver)

	// Readiness check: 사이트 등록 전 parsing_rules 가 seed 됐는지 검증.
	// 부재 시 fail-fast — 실행 중 모든 ParsePage/ParseLinks 가 ErrNoRule 로 죽는 것보다 즉시 종료.
	// migration 007 (또는 동등한 운영자 seed) 가 적용되어야 통과.
	if err := verifyParsingRulesSeeded(ctx, ruleResolver); err != nil {
		log.WithError(err).Fatal("parsing_rules seed missing — apply migration 007 before deploy")
	}

	// raw_contents 서비스 — fetcher 측 Claim Check 저장 + parser 측 로드/삭제 (이슈 #134).
	rawRepo := pgstore.NewRawContentRepository(pool, log)
	rawSvc := service.NewRawContentService(rawRepo, log)

	contentRepo := pgstore.NewContentRepository(pool, log)
	contentSvc := service.NewContentService(contentRepo, log)

	// fetcher 측 등록 (이슈 #134 분리 후): chain handler 가 raw_contents 저장 + RawContentRef 발행만 수행.
	if err := kr.Register(registry, core.DefaultConfig(), rawSvc, crawlerProducer, log); err != nil {
		log.WithError(err).Fatal("failed to register kr crawlers")
	}
	if err := us.Register(registry, core.DefaultConfig(), rawSvc, crawlerProducer, log); err != nil {
		log.WithError(err).Fatal("failed to register us crawlers")
	}

	// Redis 기반 ProcessingLock: 동일 URL 이 여러 worker/인스턴스에서 단계별 (fetcher/parser/validator)
	// 중복 처리되는 것을 방지합니다 (이슈 #178). 단일 인스턴스를 fetcher / parser / validator 가 공유 —
	// 단계 구분은 ProcessingKey(stage, url) 의 stage prefix 로 처리.
	// worker/manager 가 nil 을 NoopProcessingLock 로 fallback 처리하는 설계와 일관되게,
	// Redis 초기화 실패 시에도 크롤링이 중단되지 않도록 graceful degrade 합니다.
	var procLock crawlerWorker.ProcessingLock
	var ingestionLock crawlerWorker.IngestionLock
	var retryScheduler crawlerWorker.RetryScheduler
	var retrySchedulerStop func()
	redisCfg, err := config.LoadRedis()
	if err != nil {
		log.WithError(err).Warn("failed to load redis config, falling back to noop processing lock and ingestion lock")
	} else {
		redisClient, redisErr := redis.New(ctx, redisCfg)
		if redisErr != nil {
			log.WithError(redisErr).Warn("failed to connect to redis, falling back to noop processing lock and ingestion lock")
		} else {
			defer redisClient.Close()
			log.WithFields(map[string]interface{}{
				"host": redisCfg.Host,
				"port": redisCfg.Port,
			}).Info("redis connected for processing lock and ingestion lock")
			procLock = crawlerWorker.NewRedisProcessingLock(redisClient, crawlerWorker.DefaultProcessingLockTTL)
			ingestionLock = crawlerWorker.NewRedisIngestionLock(redisClient, redisCfg.IngestionLockTTL)

			// Delayed retry queue (이슈 #82): retry 를 Redis ZSET 에 보관하고 별도
			// goroutine 이 ScheduledAt 도달 시 Kafka 에 발행 — worker 슬롯 점유 회피.
			// Redis 부재 시 worker 가 lazy 로 KafkaImmediateRetryScheduler 를 사용 (기존 동작).
			redisRetry := crawlerWorker.NewRedisDelayedRetryScheduler(
				redisClient, crawlerProducer,
				crawlerWorker.DefaultRedisRetrySchedulerConfig(),
				log,
			)
			runCtx, cancelRun := context.WithCancel(ctx)
			redisRetry.Start(runCtx) // 내부에서 wg.Add 후 go Run — 패닉 안전
			retryScheduler = redisRetry
			retrySchedulerStop = func() {
				cancelRun()
				redisRetry.Stop()
			}
			log.Info("redis delayed retry queue enabled (worker slot occupancy on retry resolved)")
		}
	}
	if retrySchedulerStop != nil {
		defer retrySchedulerStop()
	}

	// URL dedup — Ingestion Lock (이슈 #178, 이슈 #126 의 단일 책임화):
	// Publisher 가 Kafka enqueue 직전에 정규화 + atomic SETNX 로 진입 marker 잡기.
	// 진입 후에는 다운스트림 어느 worker 에서도 동일 URL 의 추가 fetch 발생 X (TTL 만료 시까지).
	jobPublisher.SetNormalizer(links.NewNormalizer())
	if ingestionLock != nil {
		jobPublisher.SetIngestionLock(ingestionLock)
		log.WithField("ttl", redisCfg.IngestionLockTTL.String()).Info("publisher ingestion lock enabled")
	}

	managerCfg := crawlerWorker.ManagerConfig{
		High:           crawlerWorker.PoolConfig{Consumer: highConsumer, WorkerCount: 3},
		Normal:         crawlerWorker.PoolConfig{Consumer: normalConsumer, WorkerCount: 6},
		Low:            crawlerWorker.PoolConfig{Consumer: lowConsumer, WorkerCount: 2},
		ProcessingLock: procLock,
		RetryScheduler: retryScheduler,
	}

	manager := crawlerWorker.NewPoolManager(managerCfg, crawlerProducer, registry, contentSvc, resolver, log)
	manager.Start(ctx)

	log.WithFields(map[string]interface{}{
		"high_workers":   managerCfg.High.WorkerCount,
		"normal_workers": managerCfg.Normal.WorkerCount,
		"low_workers":    managerCfg.Low.WorkerCount,
	}).Info("crawler pool manager started")

	// ── LLM rule generator (이슈 #149) ──────────────────────────────────────────
	// rule.ErrNoRule (host 매칭 활성 규칙 없음) fallback 으로 LLM 이 selector 를 자동 생성합니다.
	// LLM_ENABLED=false 또는 API key 누락 시 nil — parser worker 는 ErrNoRule 시 raw 만 잔존.
	//
	// **본 PR scope**: FixedOrder("gemini") 정책으로 Gemini 단일 provider 사용 (1000회/일 무료 한도 내 검증).
	// 후속 PR (이슈 TBD) 에서 chain (gemini → openai → anthropic) 으로 정책 확장.
	//
	// 이슈 #173 단계 4-2: 동일 provider 를 refiner 와 공유 — 환경변수 1세트로 동시 제어.
	llmProvider := buildLLMProvider(log)
	llmGen := buildLLMGenerator(llmProvider, parsingRuleRepo, ruleResolver, log)

	// ── Parser worker (이슈 #134) ──────────────────────────────────────────────
	// fetcher 와 분리된 별도 consumer group (issuetracker-parsers) 으로 동작 — 인스턴스 수 독립 스케일.
	// TopicFetched 의 RawContentRef 를 consume 하여 raw 로드 + 파싱 + content 저장 + raw 삭제.
	// 파싱 실패 (rule.Error) 시 raw 잔존 → LLM 재처리 윈도우 (이슈 #149).
	parserKafkaCfg := queue.DefaultConfig()
	parserKafkaCfg.GroupID = queue.GroupParsers
	parserConsumer := queue.NewConsumer(parserKafkaCfg, queue.TopicFetched)
	// 이슈 #173 단계 4-1: sample URL 누적 — parser_worker 가 정상 파싱 후 누적, 단계 4-2 의 정밀화 트리거 입력.
	sampleRepo := pgstore.NewSampleURLRepository(pool, log)

	pw := parserWorker.NewParserWorker(
		parserConsumer,
		crawlerProducer, // normalized 토픽 발행 + chained article jobs 발행 시 publisher 가 동일 producer 사용
		rawSvc,
		contentSvc,
		jobPublisher,
		ruleParser,
		ruleResolver, // 이슈 #173 단계 4-1: sample 누적 시 매칭 rule lookup
		sampleRepo,   // 이슈 #173 단계 4-1
		procLock,     // 이슈 #178: fetcher / parser / validator 가 동일 ProcessingLock 인스턴스 공유
		llmGen,
		parserWorkerCount,
		log,
	)
	pw.Start(ctx)

	// ── Refiner (이슈 #173 단계 4-2) ──────────────────────────────────────────
	// catch-all + llm-auto rule 의 누적 sample URL 로부터 path_pattern 정밀화.
	// REFINEMENT_ENABLED=false 또는 config 실패 시 nil — 기존 catch-all rule 그대로 동작.
	pathRefiner := buildRefiner(llmProvider, parsingRuleRepo, sampleRepo, ruleResolver, log)
	if pathRefiner != nil {
		pathRefiner.Start(ctx)
	}

	// Cleanup cron — parser worker 가 처리하지 못한 채 잔존한 raw_contents row 정리.
	// 정상 흐름에서는 거의 동작 안 함. crash / rule.Error 잔존 / LLM 재처리 윈도우 만료된 row 만 대상.
	cleaner := parserWorker.NewRawContentCleaner(rawSvc, parserWorker.CleanupConfig{}, log)
	cleaner.Start(ctx)

	// ── Scheduler (시드 Job 발행) ─────────────────────────────────────────────
	schedulerCfg, err := config.LoadScheduler()
	if err != nil {
		log.WithError(err).Fatal("failed to load scheduler config")
	}

	emitter := scheduler.NewJobEmitter(crawlerProducer, log)
	entries := scheduler.DefaultEntries(schedulerCfg)
	sched := scheduler.New(entries, emitter, log, schedulerCfg.MaxRetries)

	// Backlog throttle (이슈 #124): SCHEDULER_MAX_BACKLOG > 0 일 때만 활성.
	// crawl 토픽의 consumer-group lag 가 임계값 초과 시 publish 차단.
	if schedulerCfg.MaxBacklog > 0 {
		backlogChecker := queue.NewBacklogChecker(crawlerKafkaCfg.Brokers, schedulerCfg.BacklogCheckTimeout)
		throttler := scheduler.NewBacklogThrottler(
			backlogChecker,
			queue.GroupCrawlerWorkers,
			schedulerCfg.MaxBacklog,
			schedulerCfg.BacklogCheckTimeout,
			log,
		)
		sched.SetThrottler(throttler)
		log.WithFields(map[string]interface{}{
			"max_backlog":   schedulerCfg.MaxBacklog,
			"check_timeout": schedulerCfg.BacklogCheckTimeout.String(),
			"group":         queue.GroupCrawlerWorkers,
		}).Info("scheduler backlog throttle enabled")
	}

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

	validateWorker := validate.NewWorker(validateConsumer, validateProducer, contentSvc, procLock, validateWorkerCount, validateCfg)
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
	// 셧다운 시작 시점부터 logger 에 shutting_down=true 를 부여합니다 (이슈 #72 TODO #4).
	//
	// 적용 범위 (중요):
	//   - 본 변수 'log' 와 shutdownCtx 를 통해 전달되는 모든 로그에만 부착됩니다.
	//   - 즉 Stop() 호출 경로 (shutdownCtx 를 받아 logger.FromContext 로 꺼내는 코드)
	//     에서만 자동 상속됩니다.
	//   - Start(ctx) 로 이미 워커 goroutine 에 캡쳐된 ctx 의 logger 는 별개 *Logger
	//     포인터이므로, in-flight 작업이 남기는 로그에는 본 필드가 부착되지 않습니다.
	//   - in-flight 로그까지 일관 필터링하려면 별도 atomic shutdownFlag 를 logger
	//     hook 으로 주입하는 후속 PR 이 필요합니다 (현재 범위 외).
	log = log.WithField("shutting_down", true)
	log.Warn("shutdown signal received, draining workers...")
	cancel()

	// shutdownCtx 에 logger 를 주입하여 Stop 내부에서 logger.FromContext 가
	// shutting_down 필드를 자동으로 상속받도록 합니다.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()
	shutdownCtx = log.ToContext(shutdownCtx)

	sched.Stop()

	if err := manager.Stop(shutdownCtx); err != nil {
		log.WithError(err).Error("error during crawler shutdown")
	}
	if err := pw.Stop(shutdownCtx); err != nil {
		log.WithError(err).Error("error during parser worker shutdown")
	}
	// llmGen.Stop 은 parser worker 정지 후 호출 — 새 Enqueue source 가 차단된 시점에
	// in-flight LLM 호출 완료 대기 (graceful shutdown, 이슈 #149 gemini 피드백).
	if llmGen != nil {
		llmGen.Stop(shutdownCtx)
	}
	// refiner.Stop 은 in-flight polling cycle 의 완료를 대기 (PR #191 피드백).
	// rootCtx (위 cancel()) 가 이미 cancel 된 상태이므로 cycle 안의 RunOnce 가 즉시 종료됨 —
	// 본 호출은 cycle 종료 + goroutine drain 보장.
	if pathRefiner != nil {
		pathRefiner.Stop(shutdownCtx)
	}
	cleaner.Stop()
	if err := validateWorker.Stop(shutdownCtx); err != nil {
		log.WithError(err).Error("error during validate worker shutdown")
	}

	log.Info("shutdown completed")
}

// buildLLMProvider 는 LLMConfig 에 따라 chain provider 를 구성합니다 (이슈 #173 단계 4-2 — 공유용).
//
// 반환값 nil 은 LLM 비활성을 의미 — 호출자가 nil 허용 분기:
//   - LLM_ENABLED=false / API key 부재 / provider 생성 실패 → nil + warn 로그
//
// llmgen 과 refiner 가 동일 provider 를 공유 — 환경변수 1세트 (LLM_*) 로 두 컴포넌트 동시 제어.
//
// **본 PR scope**: FixedOrder(cfg.Provider) 정책으로 단일 provider 사용. 후속 PR 에서 chain 확장.
func buildLLMProvider(log *logger.Logger) llm.Provider {
	cfg, err := config.LoadLLM()
	if err != nil {
		log.WithError(err).Warn("failed to load LLM config, llm provider disabled")
		return nil
	}
	if !cfg.Enabled {
		log.Info("LLM provider disabled (LLM_ENABLED=false)")
		return nil
	}
	if cfg.APIKey == "" {
		log.WithField("provider", cfg.Provider).Warn("LLM API key missing, llm provider disabled (set GEMINI_API_KEY / OPENAI_API_KEY / ANTHROPIC_API_KEY)")
		return nil
	}

	provider, err := llm.New(llm.Config{
		Provider: cfg.Provider,
		APIKey:   cfg.APIKey,
		Model:    cfg.Model,
		Timeout:  cfg.Timeout,
	})
	if err != nil {
		log.WithError(err).WithField("provider", cfg.Provider).Warn("failed to construct LLM provider")
		return nil
	}

	pol := policy.NewFixedOrder(cfg.Provider)
	composed := chain.NewWithPolicy(pol, []llm.Provider{provider}, chain.WithPolicyLogger(log))

	log.WithFields(map[string]interface{}{
		"provider": cfg.Provider,
		"model":    cfg.Model,
		"timeout":  cfg.Timeout.String(),
	}).Info("LLM provider enabled (FixedOrder policy — see issue #149 follow-up for chain expansion)")

	return composed
}

// buildLLMGenerator 는 buildLLMProvider 결과로 llmgen.Generator 를 구성합니다 (이슈 #149).
//
// provider 가 nil (LLM 비활성) 이면 nil 반환 — parser worker 는 ErrNoRule 시 raw 만 잔존.
func buildLLMGenerator(provider llm.Provider, repo storage.ParsingRuleRepository, resolver *rule.Resolver, log *logger.Logger) *llmgen.Generator {
	if provider == nil {
		return nil
	}
	return llmgen.New(provider, repo, resolver, log)
}

// buildRefiner 는 RefinementConfig + LLM provider 로 refiner.Refiner 를 구성합니다 (이슈 #173 단계 4-2).
//
// 반환값 nil 은 정밀화 비활성을 의미 — REFINEMENT_ENABLED=false 또는 config load 실패 시.
// LLM provider 는 nil 허용 — algorithm-only 모드로 동작.
func buildRefiner(
	provider llm.Provider,
	rules storage.ParsingRuleRepository,
	samples storage.SampleURLRepository,
	resolver *rule.Resolver,
	log *logger.Logger,
) *refiner.Refiner {
	cfg, err := config.LoadRefinement()
	if err != nil {
		log.WithError(err).Warn("failed to load refinement config, refiner disabled")
		return nil
	}
	if !cfg.Enabled {
		log.Info("refiner disabled (REFINEMENT_ENABLED=false)")
		return nil
	}

	opts := []refiner.Option{
		refiner.WithInterval(cfg.Interval),
		refiner.WithMinSamples(cfg.MinSamples),
	}
	if provider != nil {
		opts = append(opts, refiner.WithLLMClient(refiner.NewLLMAdapter(provider)))
	}
	return refiner.New(rules, samples, resolver, log, opts...)
}

// verifyParsingRulesSeeded 는 본 PR 이 등록할 모든 사이트의 (host, target_type) 페어가
// parsing_rules 테이블에 활성 row 로 존재하는지 확인합니다.
//
// 부재 시 ErrNoRule 등 진단 에러를 그대로 반환 — 호출자가 Fatal 로 부팅 차단.
// migration 007 이 적용되어야 통과 (또는 운영자가 동등한 row 를 직접 입력).
func verifyParsingRulesSeeded(ctx context.Context, resolver *rule.Resolver) error {
	required := []struct {
		host string
		typ  storage.TargetType
	}{
		{"n.news.naver.com", storage.TargetTypePage},
		{"news.naver.com", storage.TargetTypeList},
		{"v.daum.net", storage.TargetTypePage},
		{"news.daum.net", storage.TargetTypeList},
		{"www.yna.co.kr", storage.TargetTypePage},
		{"www.yna.co.kr", storage.TargetTypeList},
		{"edition.cnn.com", storage.TargetTypePage},
		{"edition.cnn.com", storage.TargetTypeList},
	}
	for _, r := range required {
		// "/" path 로 catch-all (path_pattern='') 매칭 검증 — seed 된 host-only rule 확인 (이슈 #173).
		if _, err := resolver.Resolve(ctx, r.host, "/", r.typ); err != nil {
			return fmt.Errorf("missing rule for (%s, %s): %w", r.host, r.typ, err)
		}
	}
	return nil
}
