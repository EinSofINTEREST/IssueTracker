package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"issuetracker/internal/bus"
	"issuetracker/internal/locks"
	"issuetracker/internal/processor"
	"issuetracker/internal/processor/fetcher"
	"issuetracker/internal/processor/fetcher/core"
	"issuetracker/internal/processor/fetcher/domain/general/sources"
	"issuetracker/internal/processor/fetcher/domain/search"
	"issuetracker/internal/processor/fetcher/handler"
	fetcherRule "issuetracker/internal/processor/fetcher/rule"
	crawlerWorker "issuetracker/internal/processor/fetcher/worker"
	"issuetracker/internal/processor/parser"
	"issuetracker/internal/processor/parser/rule"
	"issuetracker/internal/processor/parser/rule/claudegen"
	llmgenwiring "issuetracker/internal/processor/parser/rule/llmgen/wiring"
	refinerwiring "issuetracker/internal/processor/parser/rule/refiner/wiring"
	"issuetracker/internal/processor/parser/rule/validator"
	parserWorker "issuetracker/internal/processor/parser/worker"
	"issuetracker/internal/processor/validate"
	validateWorkerPkg "issuetracker/internal/processor/validate/worker"
	"issuetracker/internal/scheduler"
	"issuetracker/internal/storage/decorator"
	pgstore "issuetracker/internal/storage/postgres"
	"issuetracker/internal/storage/primitive"
	redisstore "issuetracker/internal/storage/redis"
	"issuetracker/internal/storage/repository"
	"issuetracker/internal/storage/service"
	"issuetracker/pkg/config"
	"issuetracker/pkg/links"
	"issuetracker/pkg/llm/prompt"
	llmwiring "issuetracker/pkg/llm/wiring"
	"issuetracker/pkg/logger"
	"issuetracker/pkg/metrics"
	"issuetracker/pkg/queue"

	goredis "github.com/redis/go-redis/v9"

	"issuetracker/pkg/redis"
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

	shutdownCfg, err := config.LoadShutdown()
	if err != nil {
		log.WithError(err).Fatal("failed to load shutdown config")
	}
	log.WithFields(map[string]interface{}{
		"shutdown_timeout":           shutdownCfg.Timeout.String(),
		"claudegen_shutdown_timeout": shutdownCfg.ClaudegenTimeout.String(),
	}).Info("shutdown timeouts loaded")

	// 모든 stage 의 worker goroutine 수를 env 로 노출 (이슈 #376).
	// default 는 기존 hardcoded 값과 동일 — env 미설정 시 동작 100% 보존.
	workerCountsCfg, err := config.LoadWorkerCounts()
	if err != nil {
		log.WithError(err).Fatal("failed to load worker counts config")
	}
	log.WithFields(map[string]interface{}{
		"fetcher_high":   workerCountsCfg.FetcherHigh,
		"fetcher_normal": workerCountsCfg.FetcherNormal,
		"fetcher_low":    workerCountsCfg.FetcherLow,
		"parser":         workerCountsCfg.Parser,
		"validate":       workerCountsCfg.Validate,
	}).Info("worker counts loaded")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ctx = log.ToContext(ctx)

	// ── Metrics endpoint ──────────────────────────────────────────
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

	// 이슈 #391 — PriorityResolver chain 이 publisher 측으로 이동 + 모든 PublishX 가
	// resolver 통과 (메타 #385 Sub 6). ExplicitPriorityResolver 를 chain 1순위 로 등록 —
	// 발행자가 job.Priority 를 사전 명시한 경우 (seed entry / retry / upgrade) 그 값이 보존됨.
	resolver := bus.NewCompositeResolver(core.PriorityNormal)
	resolver.Add(&bus.ExplicitPriorityResolver{})
	resolver.Add(bus.NewSourcePriorityResolver(core.PriorityNormal))
	resolver.Add(bus.NewRuleBasedPriorityResolver(core.PriorityNormal))

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

	jobPublisher := bus.New(crawlerProducer, resolver, log)

	// rule.Parser: parser_rules 테이블 기반 단일 파서 엔진.
	// 사이트별 NaverParser/CNNParser/... 를 대체 — 모든 사이트가 본 단일 인스턴스를 공유.
	// 모든 Repository 는 WrapXxxWithTimeout 으로 감싸 query-level timeout 적용 (이슈 #427).
	// pgxpool.Acquire 가 MaxConns 고갈 상황에서 무한 대기하는 시나리오 차단.
	parserRuleRepo := decorator.WrapParserRuleWithTimeout(pgstore.NewParserRuleRepository(pool, log), dbCfg.QueryTimeout)
	ruleResolver, err := rule.NewResolver(parserRuleRepo)
	if err != nil {
		log.WithError(err).Fatal("failed to construct rule resolver")
	}
	// parser_rules mutation → cache invalidate 자동 결합 (decorator 패턴).
	// 호출처가 명시적 Invalidate 를 까먹어도 stale cache 발생 X — single source of truth.
	parserRuleRepo = decorator.WrapWithInvalidator(parserRuleRepo, ruleResolver)
	ruleParser, err := rule.NewParser(ruleResolver)
	if err != nil {
		log.WithError(err).Fatal("failed to construct rule parser")
	}

	// page-parse 블랙리스트 — 카테고리 → article job 발행 단계에서 매칭 URL 차단.
	// Enabled=false 시 Matcher 미주입 → parser_worker 가 모든 링크 그대로 발행 (기능 OFF).
	blacklistCfg, err := config.LoadBlacklist()
	if err != nil {
		log.WithError(err).Fatal("failed to load blacklist config")
	}
	var (
		blacklistMatcher *rule.BlacklistMatcher
		blacklistRepo    repository.BlacklistRepository // llmGen.SetBlacklistRepo 에 전달 (#326).
	)
	if blacklistCfg.Enabled {
		repo := decorator.WrapBlacklistWithTimeout(pgstore.NewBlacklistRepository(pool, log), dbCfg.QueryTimeout)
		bm, bmErr := rule.NewBlacklistMatcher(repo)
		if bmErr != nil {
			log.WithError(bmErr).Fatal("failed to construct blacklist matcher")
		}
		// invalidatingBlacklistRepo decorator: claudegen 자동 INSERT (#326) 시 Matcher cache flush.
		blacklistRepo = decorator.WrapBlacklistWithInvalidator(repo, bm)
		blacklistMatcher = bm
		log.Info("page-parse blacklist enabled (parser_blacklist DB-backed)")
	} else {
		log.Info("page-parse blacklist disabled (BLACKLIST_ENABLED=false)")
	}

	// Readiness check: 사이트 등록 전 parser_rules 가 seed 됐는지 검증.
	// 부재 시 fail-fast — 실행 중 모든 ParsePage/ParseLinks 가 ErrNoRule 로 죽는 것보다 즉시 종료.
	// migration 007 (또는 동등한 운영자 seed) 가 적용되어야 통과.
	if err := rule.VerifySeeded(ctx, ruleResolver); err != nil {
		log.WithError(err).Fatal("parser_rules seed missing — apply migration 007 before deploy")
	}

	// raw_contents 서비스 — fetcher 측 Claim Check 저장 + parser 측 로드/삭제.
	rawRepo := decorator.WrapRawContentWithTimeout(pgstore.NewRawContentRepository(pool, log), dbCfg.QueryTimeout)
	rawSvc := service.NewRawContentService(rawRepo, log)

	contentRepo := decorator.WrapContentWithTimeout(pgstore.NewContentRepository(pool, log), dbCfg.QueryTimeout)
	contentSvc := service.NewContentService(contentRepo, log)

	// host 단위 fetcher 룰 — fetcher_rules 테이블 + Resolver wiring.
	// 룰 부재 host 는 default chain (현재 동작 100% 보존).
	fetcherRuleRepoBase, err := pgstore.NewFetcherRuleRepository(pool, log)
	if err != nil {
		log.WithError(err).Fatal("failed to construct fetcher rule repository")
	}
	fetcherRuleRepo := decorator.WrapFetcherRuleWithTimeout(fetcherRuleRepoBase, dbCfg.QueryTimeout)
	fetcherResolver, err := fetcherRule.NewResolver(fetcherRuleRepo, log, 0)
	if err != nil {
		log.WithError(err).Fatal("failed to construct fetcher rule resolver")
	}

	// process-local secret token — Upgrader 의 force_fetcher 부착과 ChainHandler 의
	// 검증이 같은 token 공유. 외부 source 의 임의 force 차단.
	if err := fetcherRule.InitForceFetcherToken(); err != nil {
		log.WithError(err).Fatal("failed to init force_fetcher token")
	}

	// chromedp pool config 를 사이트 등록 전에 로드 — 사이트별 chromedp chain 이
	// worker_id 별 RemoteURL 로 N 개 build 되어야 ChainHandler 가 worker:Chrome 1:1 매핑 활성화.
	chromedpPoolCfg, err := config.LoadFetcherChromedpPool()
	if err != nil {
		log.WithError(err).Fatal("failed to load fetcher chromedp pool config")
	}
	chromedpRemoteURLs := chromedpPoolCfg.RemoteURLs
	if !chromedpPoolCfg.Enabled {
		// pool 비활성 시 사이트 chromedp chain 도 미wiring — fail-fast 로직은 별도 분기 (아래).
		chromedpRemoteURLs = nil
	}

	// chromedp pool 이 활성화된 경우 remoteURLs 수 == WorkerCount 를 사전 검증합니다 (CodeRabbit Major 반영).
	// RegisterAll 이 URL 당 1 개 chain 을 생성하므로, 불일치 시 worker:chain 매핑이 어긋납니다.
	if chromedpPoolCfg.Enabled && len(chromedpRemoteURLs) != chromedpPoolCfg.WorkerCount {
		log.WithFields(map[string]interface{}{
			"worker_count":     chromedpPoolCfg.WorkerCount,
			"remote_url_count": len(chromedpRemoteURLs),
		}).Fatal("chromedp pool config mismatch: remote_urls count must equal worker_count")
	}

	// fetcher 측 등록: fetcher_rules DB 에서 모든 source 를 읽어 일괄 등록.
	if err := sources.RegisterAll(ctx, registry, fetcherRuleRepo, core.DefaultConfig(), rawSvc, jobPublisher, fetcherResolver, chromedpRemoteURLs, log); err != nil {
		log.WithError(err).Fatal("failed to register crawlers from db")
	}

	// Search handler (Google CSE) — optional. APIKey/CX 미설정이면 wire skip.
	// scheduler_entries 의 category='search' entry 가 fetcher 에 도달했을 때 본 handler 가 호출되어
	// keyword × CSE fanout → host 단위 chained article job 발행.
	googleCSECfg, err := config.LoadGoogleCSE()
	if err != nil {
		log.WithError(err).Warn("failed to load google cse config — search handler disabled")
	} else if !googleCSECfg.IsConfigured() {
		log.Info("google cse not configured (GOOGLE_CSE_API_KEY/CX) — search handler disabled")
	} else {
		searchKeywordRepoBase, kwErr := pgstore.NewSearchKeywordRepository(pool, log)
		if kwErr != nil {
			log.WithError(kwErr).Warn("search keyword repo construction failed — search handler disabled")
		} else {
			searchKeywordRepo := decorator.WrapSearchKeywordWithTimeout(searchKeywordRepoBase, dbCfg.QueryTimeout)
			cseClient, cseErr := search.NewCSEClient(search.CSEClientOptions{
				APIKey:  googleCSECfg.APIKey,
				CX:      googleCSECfg.CX,
				Timeout: googleCSECfg.Timeout,
			}, log)
			if cseErr != nil {
				log.WithError(cseErr).Warn("cse client construction failed — search handler disabled")
			} else {
				searchHandler, hErr := search.NewSearchHandler(search.SearchHandlerOptions{
					Client:      cseClient,
					KeywordRepo: searchKeywordRepo,
					Publisher:   jobPublisher,
				}, log)
				if hErr != nil {
					log.WithError(hErr).Warn("search handler construction failed — search handler disabled")
				} else {
					// scheduler entry 의 url=https://customsearch.googleapis.com/... 가 host=customsearch.googleapis.com
					// 으로 변환되어 CrawlerName 에 들어감 (NewDefaultEntryConverter). 두 키 모두 등록 — source_name
					// 직접 매칭이나 host-based dispatch 양쪽 지원.
					registry.Register("customsearch.googleapis.com", searchHandler)
					registry.Register("google_cse", searchHandler)
					log.Info("search handler registered (google cse)")
				}
			}
		}
	}

	// Redis 기반 ProcessingLock: 동일 URL 이 여러 worker/인스턴스에서 단계별 (fetcher/parser/validator)
	// 중복 처리되는 것을 방지합니다. 단일 인스턴스를 fetcher / parser / validator 가 공유 —
	// 단계 구분은 ProcessingKey(stage, url) 의 stage prefix 로 처리.
	// worker/manager 가 nil 을 NoopProcessingLock 로 fallback 처리하는 설계와 일관되게,
	// Redis 초기화 실패 시에도 크롤링이 중단되지 않도록 graceful degrade 합니다.
	var procLock locks.ProcessingLock
	var ingestionLock locks.IngestionLock
	var retryScheduler bus.RetryScheduler
	var retrySchedulerStop func()
	var redisClientShared *redis.Client // failure counter wiring 에서 재사용
	redisCfg, err := config.LoadRedis()
	if err != nil {
		log.WithError(err).Warn("failed to load redis config, falling back to noop processing lock and ingestion lock")
	} else {
		redisClient, redisErr := redis.New(ctx, redisCfg)
		if redisErr != nil {
			log.WithError(redisErr).Warn("failed to connect to redis, falling back to noop processing lock and ingestion lock")
		} else {
			defer redisClient.Close()
			redisClientShared = redisClient
			log.WithFields(map[string]interface{}{
				"host": redisCfg.Host,
				"port": redisCfg.Port,
			}).Info("redis connected for processing lock and ingestion lock")
			procLock = locks.NewRedisProcessingLock(redisClient, locks.DefaultProcessingLockTTL)
			ingestionLock = locks.NewRedisIngestionLock(redisClient, redisCfg.IngestionLockTTL)

			// Delayed retry queue: retry 를 Redis ZSET 에 보관하고 별도
			// goroutine 이 ScheduledAt 도달 시 Kafka 에 발행 — worker 슬롯 점유 회피.
			// Redis 부재 시 worker 가 lazy 로 KafkaImmediateRetryScheduler 를 사용 (기존 동작).
			retryCfg := bus.DefaultRedisRetrySchedulerConfig()
			// idle heartbeat 압축 (이슈 #370) — pkg/config 로 env 로드 일관성 유지.
			retrySchedCfg, retrySchedErr := config.LoadRetryScheduler()
			if retrySchedErr != nil {
				log.WithError(retrySchedErr).Fatal("RETRY_HEARTBEAT_EVERY_N_IDLE_TICKS 로드 실패")
			}
			retryCfg.HeartbeatEveryNIdleTicks = retrySchedCfg.HeartbeatEveryNIdleTicks
			redisRetry := bus.NewRedisDelayedRetryScheduler(
				redisClient, jobPublisher,
				retryCfg,
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

	// URL dedup — Ingestion Lock → Pipeline Guard 통합:
	// Publisher / Scheduler / Worker 가 동일 guard 를 공유하여 target type 별 정책 적용:
	//   - Article: 24h TTL (기존 IngestionLock 정책 유지)
	//   - Category: 단명 TTL (default 60s) — cycle 종료 시 명시적 release + TTL fallback
	jobPublisher.SetNormalizer(links.NewNormalizer())
	var pipelineGuard *locks.PipelineGuard
	if ingestionLock != nil {
		pipelineGuard = locks.NewPipelineGuard(ingestionLock, redisCfg.PipelineGuardCategoryTTL)
		jobPublisher.SetPipelineGuard(pipelineGuard)
		log.WithFields(map[string]interface{}{
			"article_ttl":  redisCfg.IngestionLockTTL.String(),
			"category_ttl": redisCfg.PipelineGuardCategoryTTL.String(),
		}).Info("publisher pipeline guard enabled (Article 24h / Category 단명)")
	}

	// StageGate 설정 (이슈 #353/#355/#356) — fetcher / parser / validator 의 per-stage Semaphore cap.
	// 환경변수로 명시 cap 제공, 미지정 시 각 stage 의 worker_count/2 자동.
	stageGateCfg, err := config.LoadStageGate()
	if err != nil {
		log.WithError(err).Fatal("failed to load stage gate config")
	}

	managerCfg := crawlerWorker.ManagerConfig{
		High:                  crawlerWorker.PoolConfig{Consumer: highConsumer, WorkerCount: workerCountsCfg.FetcherHigh},
		Normal:                crawlerWorker.PoolConfig{Consumer: normalConsumer, WorkerCount: workerCountsCfg.FetcherNormal},
		Low:                   crawlerWorker.PoolConfig{Consumer: lowConsumer, WorkerCount: workerCountsCfg.FetcherLow},
		ProcessingLock:        procLock,
		RetryScheduler:        retryScheduler,
		MaxConcurrentPerStage: stageGateCfg.FetcherMaxConcurrentPerStage,
	}
	// per-pool 실제 capacity 를 함께 로깅 — env 양수 override 시 min(configured, worker_count/2) 가
	// 적용되므로 단순 "worker_count/2" 문구는 부정확. 운영자가 실제 cap 을 즉답 가능하도록.
	log.WithFields(map[string]interface{}{
		"configured": stageGateCfg.FetcherMaxConcurrentPerStage,
		"high":       managerCfg.High.WorkerCount,
		"normal":     managerCfg.Normal.WorkerCount,
		"low":        managerCfg.Low.WorkerCount,
		"high_cap":   config.CapPerStage(managerCfg.High.WorkerCount, stageGateCfg.FetcherMaxConcurrentPerStage),
		"normal_cap": config.CapPerStage(managerCfg.Normal.WorkerCount, stageGateCfg.FetcherMaxConcurrentPerStage),
		"low_cap":    config.CapPerStage(managerCfg.Low.WorkerCount, stageGateCfg.FetcherMaxConcurrentPerStage),
	}).Info("fetcher stage gate config loaded (per-pool cap = min(configured, worker_count/2))")

	// chromedp 전용 worker pool — semaphore 로 Chrome 동시 호출 제한.
	// chromedpPoolCfg 는 사이트 등록 단계에서 미리 로드됨 (RemoteURLs 가 사이트
	// chromedp chain build 에 필요).
	if chromedpPoolCfg.Enabled {
		chromedpKafkaCfg := queue.DefaultConfig()
		chromedpKafkaCfg.GroupID = queue.GroupChromedpFetchers
		chromedpConsumer := queue.NewConsumer(chromedpKafkaCfg, queue.TopicCrawlChromedp)
		defer chromedpConsumer.Close()

		// per-worker Semaphore 모델 — worker_id 별 1 개씩, 길이 = WorkerCount.
		// 본 Semaphore 는 worker 자원 격리 guard 역할 — KafkaConsumerPool 의 worker 는 메시지를
		// 순차 처리하므로 capacity > 1 은 현 모델에서 추가 동시성 이득 없음 (gemini 피드백).
		// 실질 전체 동시 navigate 수 = WorkerCount. 다음 sub-issue (#230) 에서 worker_id 별
		// RemoteURL 까지 매핑하면 worker:Chrome 1:1 활성화.
		sems := make([]crawlerWorker.Semaphore, chromedpPoolCfg.WorkerCount)
		for i := 0; i < chromedpPoolCfg.WorkerCount; i++ {
			sem, semErr := crawlerWorker.NewSemaphore(chromedpPoolCfg.SemaphoreCapacity)
			if semErr != nil {
				log.WithError(semErr).Fatal("failed to construct chromedp semaphore")
			}
			sems[i] = sem
		}
		chromedpHandler, err := crawlerWorker.NewChromedpJobHandler(registry, sems, log)
		if err != nil {
			log.WithError(err).Fatal("failed to construct chromedp job handler")
		}

		managerCfg.Chromedp = crawlerWorker.PoolConfig{Consumer: chromedpConsumer, WorkerCount: chromedpPoolCfg.WorkerCount}
		managerCfg.ChromedpHandler = chromedpHandler

		log.WithFields(map[string]interface{}{
			"worker_count":                  chromedpPoolCfg.WorkerCount,
			"per_worker_semaphore_capacity": chromedpPoolCfg.SemaphoreCapacity,
			"effective_concurrency":         chromedpPoolCfg.WorkerCount,
		}).Info("chromedp pool wiring enabled (per-worker semaphores; effective concurrency = worker_count)")
	} else {
		// goquery worker 의 ChainHandler 가 lazy detect / chromedp
		// 룰 / force_fetcher 분기에서 항상 TopicCrawlChromedp 로 republish 함. consumer 가 없으면
		// 메시지가 영구 누적되어 운영 장애로 이어짐 — fail-fast 로 운영자가 명시적 의사결정 강제.
		log.Fatal("chromedp pool disabled (FETCHER_CHROMEDP_POOL_ENABLED=false) but goquery republish path is unconditional — enable pool or fork republish behavior in chain_handler")
	}

	manager := crawlerWorker.NewPoolManager(managerCfg, jobPublisher, registry, contentSvc, resolver, log)

	log.WithFields(map[string]interface{}{
		"high_workers":   managerCfg.High.WorkerCount,
		"normal_workers": managerCfg.Normal.WorkerCount,
		"low_workers":    managerCfg.Low.WorkerCount,
	}).Info("fetcher pool manager constructed")

	// ── LLM rule generator ──────────────────────────────────────────
	// rule.ErrNoRule (host 매칭 활성 규칙 없음) fallback 으로 LLM 이 selector 를 자동 생성합니다.
	// LLM_ENABLED=false 또는 API key 누락 시 nil — parser worker 는 ErrNoRule 시 raw 만 잔존.
	//
	// **현재 정책**: LLM_POLICY 환경변수로 선택 (default "chain"):
	//   - chain (default): FixedOrder(gemini → openai → anthropic) — 정적 순서
	//   - cheapest / latency / hybrid: capability + MeasuredProvider 메트릭 기반 dynamic routing (이슈 #170)
	// metricsRegistry 를 전달하여 각 provider 호출의 latency / failure 를 Prometheus 로 노출 +
	// MeasuredProvider Stats 를 hybrid/latency 정책이 dynamic routing 에 활용 (이슈 #170).
	//
	// 동일 provider 를 refiner 와 공유 — 환경변수 1세트로 동시 제어.
	llmProvider := llmwiring.BuildProviderWithOptions(log, llmwiring.Options{
		PrometheusRegistry: metricsRegistry,
	})

	// LLM prompt loader — provider 가 nil (LLM_ENABLED=false 또는 API key 부재) 이면
	// promptLoader 도 미생성. wiring.Build 가 provider==nil 시 short-circuit 하므로
	// LLM 비활성 환경에서 prompt 디렉토리 부재로 인한 boot 실패 회피.
	var promptLoader prompt.Loader
	if llmProvider != nil {
		promptCfg, pcErr := config.LoadPrompt()
		if pcErr != nil {
			log.WithError(pcErr).Fatal("failed to load prompt config")
		}
		loader, warn := prompt.NewDefaultLoader(promptCfg.Dir, promptCfg.DirSet)
		if warn != "" {
			log.Warn(warn)
		}
		promptLoader = loader
		log.WithFields(map[string]interface{}{
			"env_dir":     promptCfg.Dir,
			"env_dir_set": promptCfg.DirSet,
		}).Info("LLM prompt loader enabled (file → embed chain)")
	}

	llmGen, err := llmgenwiring.Build(llmProvider, parserRuleRepo, ruleResolver, promptLoader, redisClientShared, log)
	if err != nil {
		log.WithError(err).Fatal("failed to build llmgen generator")
	}

	// ── Fetcher 실패 카운터 ────────────────────────────────────────
	// host 단위 fetcher 실패를 sliding window 로 누적 — 단계 3 (#221) 의 chromedp 자동 전환
	// 트리거 입력. ENABLED=false 또는 Redis 미연결 시 Noop (성능 저하 0).
	fetcherUpgradeCfg, err := config.LoadFetcherAutoUpgrade()
	if err != nil {
		log.WithError(err).Fatal("failed to load fetcher auto-upgrade config")
	}
	var failureCounter primitive.FailureCounter = primitive.NewNoopFailureCounter()
	if fetcherUpgradeCfg.Enabled && redisClientShared != nil {
		fc, fcErr := redisstore.NewFailureCounter(
			redisClientShared.Raw(),
			fetcherUpgradeCfg.Threshold,
			fetcherUpgradeCfg.Window,
			"", // default keyPrefix "fetcher:fail"
			log,
		)
		if fcErr != nil {
			log.WithError(fcErr).Warn("failed to construct redis failure counter, falling back to noop")
		} else {
			failureCounter = fc
			log.WithFields(map[string]interface{}{
				"threshold":              fetcherUpgradeCfg.Threshold,
				"window":                 fetcherUpgradeCfg.Window.String(),
				"empty_body_title_min":   fetcherUpgradeCfg.EmptyBodyTitleMin,
				"empty_body_content_min": fetcherUpgradeCfg.EmptyBodyContentMin,
			}).Info("fetcher failure counter (redis sliding window) enabled")
		}
	} else if !fetcherUpgradeCfg.Enabled {
		log.Info("fetcher auto-upgrade disabled (FETCHER_AUTO_UPGRADE_ENABLED=false), failure counter is noop")
	} else {
		log.Warn("redis unavailable, fetcher failure counter falls back to noop")
	}

	// host 단위 실패 raw_id 추적기 — 단계 3 의 chromedp 자동 전환 trigger 가 republish 대상 수집에 사용.
	// 카운터와 같은 lifecycle (window TTL 동기화) — Redis 미연결 시 Noop.
	//
	// freshness = parserWorker.DefaultStaleRawTTL (1h) — raw_contents cleanup 의 StaleTTL 과 동기화.
	// ZSET key-level EXPIRE 는 Track 마다 refresh 되어 stale entry 가 살아남는 race 차단 (이슈 #299).
	// raw_contents row 가 cleanup 으로 삭제된 직후 Upgrader 가 그 ID 로 GetByID 호출하여 ErrNotFound
	// (STORAGE_001) 노이즈 발생하던 케이스 봉쇄.
	var rawIDTracker primitive.RawIDTracker = primitive.NewNoopRawIDTracker()
	if fetcherUpgradeCfg.Enabled && redisClientShared != nil {
		t, tErr := redisstore.NewRawIDTrackerWithFreshness(
			redisClientShared.Raw(),
			fetcherUpgradeCfg.Window,
			parserWorker.DefaultStaleRawTTL,
			"", // default keyPrefix "fetcher:failed_raws"
			log,
		)
		if tErr != nil {
			log.WithError(tErr).Warn("failed to construct redis raw id tracker, falling back to noop")
		} else {
			rawIDTracker = t
			log.WithField("freshness", parserWorker.DefaultStaleRawTTL.String()).
				Info("fetcher raw id tracker (redis zset) enabled with freshness filter")
		}
	}

	// 임계값 도달 시 chromedp 자동 전환 + 실패 raw republish trigger.
	// ENABLED=false 또는 의존성 부재 시 nil — parser_worker 가 thresholdReached 신호만 받고 실제 전환 발생 안 함.
	var fetcherUpgrader *fetcherRule.Upgrader
	if fetcherUpgradeCfg.Enabled {
		var redisRaw *goredis.Client
		if redisClientShared != nil {
			redisRaw = redisClientShared.Raw()
		}
		up, upErr := fetcherRule.NewUpgrader(
			fetcherRuleRepo,
			fetcherResolver,
			rawIDTracker,
			rawSvc,
			jobPublisher, // 이슈 #388 — bus.UpgradePublisher (단일 facade)
			redisRaw,     // nil 허용 — in-flight lock 비활성 (단일 인스턴스)
			log,
		)
		if upErr != nil {
			log.WithError(upErr).Warn("failed to construct fetcher upgrader, auto-upgrade disabled at runtime")
		} else {
			fetcherUpgrader = up
			log.Info("fetcher auto-upgrade trigger (chromedp + republish) enabled")
		}
	}

	// ── Parser worker ──────────────────────────────────────────────
	// fetcher 와 분리된 별도 consumer group (issuetracker-parsers) 으로 동작 — 인스턴스 수 독립 스케일.
	// TopicFetched 의 RawContentRef 를 consume 하여 raw 로드 + 파싱 + content 저장 + raw 삭제.
	// 파싱 실패 (rule.Error) 시 raw 잔존 → LLM 재처리 윈도우.
	parserKafkaCfg := queue.DefaultConfig()
	parserKafkaCfg.GroupID = queue.GroupParsers
	parserConsumer := queue.NewConsumer(parserKafkaCfg, queue.TopicFetched)
	// sample URL 누적 — parser_worker 가 정상 파싱 후 누적, 단계 4-2 의 정밀화 트리거 입력.
	sampleRepo := decorator.WrapSampleURLWithTimeout(pgstore.NewSampleURLRepository(pool, log), dbCfg.QueryTimeout)

	// Parser StageGate (이슈 #355) — ProcessingLock + per-stage Semaphore 합성.
	// Semaphore capacity 는 PARSER_MAX_CONCURRENT_PER_STAGE 와 worker_count/2 의 min.
	// stageGateCfg 는 fetcher managerCfg 직전 (위쪽) 에서 이미 로드.
	parserCap := config.CapPerStage(workerCountsCfg.Parser, stageGateCfg.ParserMaxConcurrentPerStage)
	parserGate := locks.BuildStageGate(locks.StageParser, parserCap, procLock, log)
	if procLock != nil {
		log.WithFields(map[string]interface{}{
			"worker_count": workerCountsCfg.Parser,
			"capacity":     parserCap,
			"configured":   stageGateCfg.ParserMaxConcurrentPerStage,
		}).Info("parser stage gate enabled (ProcessingLock + Semaphore)")
	} else {
		log.Warn("processing lock unavailable, parser stage gate falls back to noop")
	}

	w := parserWorker.NewWorker(
		parserConsumer,
		jobPublisher, // 이슈 #392 — 구 producer + JobPublisher 두 인자 통합 (bus.Publisher 가 Forward + PublishChained 모두 제공)
		rawSvc,
		contentSvc,
		ruleParser,
		ruleResolver, // sample 누적 시 매칭 rule lookup
		sampleRepo,
		parserGate, // fetcher / parser / validator 가 동일 ProcessingLock 공유 + parser stage Semaphore
		llmGen,
		failureCounter,  // host 단위 fetcher 실패 카운터
		rawIDTracker,    // host 별 실패 raw_id 추적기
		fetcherUpgrader, // 임계값 도달 시 chromedp 자동 전환 + republish
		fetcherUpgradeCfg.EmptyBodyTitleMin,
		fetcherUpgradeCfg.EmptyBodyContentMin,
		workerCountsCfg.Parser,
		log,
	)

	// ── Pipeline Guard release ─────────────────────────────────────
	// Category cycle 종료 시 marker release — scheduler 다음 주기에 즉시 진입 가능.
	if pipelineGuard != nil {
		w.SetPipelineGuard(pipelineGuard)
	}

	// ── Page-parse 블랙리스트 ───────────────────────────────────────
	// 카테고리에서 추출된 article URL 중 blacklist 매칭은 bus.Publish 직전 drop.
	// Matcher 가 nil (BLACKLIST_ENABLED=false) 이면 setter noop — 모든 링크 그대로 발행.
	if blacklistMatcher != nil {
		w.SetBlacklist(blacklistMatcher)
	}

	// ── Stale rule 재학습 카운터 ───────────────────────────────────
	// host 단위 stale parse failure 누적 — 임계 도달 시 Generator.EnqueueStale 트리거.
	// FetcherAutoUpgrade 와 별개 keyspace + 더 긴 윈도우 / 더 높은 임계값 — chromedp 전환이
	// 먼저 시도되고, 그래도 실패 지속 시 LLM 재학습 (InsertNextVersion 으로 v+1 추가).
	staleRelearnCfg, err := config.LoadStaleRelearn()
	if err != nil {
		log.WithError(err).Fatal("failed to load stale relearn config")
	}
	if staleRelearnCfg.Enabled && redisClientShared != nil && llmGen != nil {
		sc, scErr := redisstore.NewStaleCounter(
			redisClientShared.Raw(),
			staleRelearnCfg.Threshold,
			staleRelearnCfg.Window,
			"", // default keyPrefix "stale:relearn"
			log,
		)
		if scErr != nil {
			log.WithError(scErr).Warn("failed to construct redis stale counter, stale relearn disabled at runtime")
		} else {
			w.SetStaleCounter(sc, staleRelearnCfg.Threshold)
			log.WithFields(map[string]interface{}{
				"threshold": staleRelearnCfg.Threshold,
				"window":    staleRelearnCfg.Window.String(),
			}).Info("stale rule relearn (redis sliding window) enabled")
		}
	} else if !staleRelearnCfg.Enabled {
		log.Info("stale rule relearn disabled (STALE_RELEARN_ENABLED=false)")
	} else if redisClientShared == nil {
		log.Warn("redis unavailable, stale rule relearn falls back to noop")
	} else if llmGen == nil {
		log.Info("llmgen disabled — stale rule relearn skipped (no enqueue target)")
	}

	// ── LLM validate 실패 재큐 ───────────────────────────────────
	// selector 검증 실패 시 raw 를 issuetracker.fetched 에 재발행 — 룰 생성 성공 후 재파싱 기회 부여.
	// llmGen 이 nil(LLM 비활성) 이면 wiring 불필요.
	if llmGen != nil {
		llmGen.SetValidateFailureHandler(w.RequeueForLLMRetry)
	}

	// ── Pending URL 큐 ────────────────────────────────────────────
	// in-flight 중 동일 도메인으로 유입된 URL 을 Redis LIST 에 보존.
	// 룰 생성 완료 시 대기 URL 을 issuetracker.fetched 에 재발행 — 새 룰로 재파싱.
	// Redis 미설정 시 graceful degrade (pending URL 보존 없이 기존 skip 동작 유지).
	if llmGen != nil && redisClientShared != nil {
		pq, pqErr := redisstore.NewPendingQueue(redisClientShared.Raw())
		if pqErr != nil {
			log.WithError(pqErr).Fatal("failed to construct redis pending queue")
		}
		llmGen.SetPendingQueue(pq, w.RequeueParsing)
		log.Info("llmgen: Redis 기반 pending URL 큐 활성화")
	}

	// ── 의미 검증 ValidatorPool ──────────────────────────────────
	// DOM 매칭 검증 통과 후 추출 내용이 실제 뉴스 제목/본문인지 LLM 으로 의미 검증.
	// llmProvider 가 nil(LLM 비활성) 이면 의미 검증 건너뜀 — DOM 검증만 수행.
	if llmGen != nil && llmProvider != nil {
		llmValidator, vErr := validator.NewLLMValidator(llmProvider, promptLoader)
		if vErr != nil {
			log.WithError(vErr).Fatal("failed to construct llm validator")
		}
		semPool := validator.NewPool(log, llmValidator)
		llmGen.SetSelectorValidator(validator.NewLLMGenAdapter(semPool))
		log.Info("llmgen: 의미 검증 ValidatorPool 활성화")
	}

	// ── Claude Code 추출기 ──────────────────────────────────────
	// LLM_EXTRACTOR=claude-code 일 때 활성화 — Claude 구독 환경의 sonnet 으로 셀렉터 추출.
	// 미지정 / gemini (기본) 일 때는 buildLLMGenerator 가 설정한 기본 LLM provider 추출.
	// Start 실패 시 Gemini 경로로 graceful fallback (fatal 아님).
	// 종료 시 stages.Stop 이후 worker.Stop 호출 — llmGen.Stop 으로 in-flight Extract 완료 보장 후 컨테이너 정리.
	// 이슈 #352 — 단일 worker → ClaudeWorkerPool (N replica, default 2) 로 throughput 향상.
	// CLAUDE_CODE_WORKER_COUNT 로 운영자 조정 가능. 기존 단일 worker 동작은 N=1 로 동등 재현 가능.
	var claudegenPool *claudegen.ClaudeWorkerPool
	llmExtractor := os.Getenv("LLM_EXTRACTOR")
	switch {
	case llmExtractor != "claude-code":
		// 기본 경로 — 분기 미발생 (Gemini 등 buildLLMGenerator 의 provider 사용).
	case llmGen == nil:
		// LLM_ENABLED=false / API key 부재 등으로 llmGen 비활성 — silent skip 회피.
		log.Warn("LLM_EXTRACTOR=claude-code requested but LLM generator is disabled (check LLM_ENABLED / API key); claudegen extractor not registered")
	case promptLoader == nil:
		log.Warn("LLM_EXTRACTOR=claude-code requested but prompt loader is disabled; claudegen extractor not registered")
	default:
		pool, perr := claudegen.NewPoolFromEnv(promptLoader, log)
		if perr != nil {
			log.WithError(perr).Warn("claudegen pool construction failed, falling back to default extractor")
		} else if serr := pool.Start(ctx); serr != nil {
			log.WithError(serr).Warn("claudegen pool start failed, falling back to default extractor")
		} else {
			llmGen.SetExtractor(pool)
			claudegenPool = pool
			log.WithFields(map[string]interface{}{
				"worker_count": pool.WorkerCount(),
			}).Info("llmgen: Claude Code 웜 컨테이너 pool 활성화")
		}
	}

	// claudegen 의 EnrichedExtractor 분기에서 페이지를 blacklist 로 판정 시 자동 등록할
	// repository 주입 (#326). blacklistRepo 가 nil (BLACKLIST_ENABLED=false) 이면
	// Generator 내부 분기가 셀렉터 INSERT skip 만 보장.
	if blacklistRepo != nil && llmGen != nil {
		llmGen.SetBlacklistRepo(blacklistRepo)
		log.Info("llmgen: 자동 blacklist 등록 활성화 (claudegen multi-step extraction)")
	}

	// ── Refiner ──────────────────────────────────────────
	// catch-all + llm-auto rule 의 누적 sample URL 로부터 path_pattern 정밀화.
	// REFINEMENT_ENABLED=false 또는 config 실패 시 nil — 기존 catch-all rule 그대로 동작.
	// metricsRegistry 는 nil 허용 — Record* 호출이 noop.
	pathRefiner, err := refinerwiring.Build(llmProvider, promptLoader, parserRuleRepo, sampleRepo, ruleResolver, metricsRegistry, log)
	if err != nil {
		log.WithError(err).Fatal("failed to build refiner")
	}

	// Cleanup cron — parser worker 가 처리하지 못한 채 잔존한 raw_contents row 정리.
	// 정상 흐름에서는 거의 동작 안 함. crash / rule.Error 잔존 / LLM 재처리 윈도우 만료된 row 만 대상.
	cleaner := parserWorker.NewRawContentCleaner(rawSvc, parserWorker.CleanupConfig{}, log)

	// ── Scheduler (시드 Job 발행) ─────────────────────────────────────────────
	schedulerCfg, err := config.LoadScheduler()
	if err != nil {
		log.WithError(err).Fatal("failed to load scheduler config")
	}

	// 이슈 #387 — scheduler.JobEmitter 제거. scheduler 가 bus.Publisher 직접 의존.
	// guard / normalizer 는 jobPublisher 에 이미 wiring 되어 있어 시드 / chained 둘 다 동일
	// 정책 적용 (메타 #385 — 단일 facade).
	// Scheduler entries 는 두 source-of-truth 중 하나에서 결정 (이슈 #328):
	//   1. DB (scheduler_entries 테이블) — 운영자 UPDATE 가 30s refresh 주기로 반영
	//   2. fallback (DefaultEntries) — DB 부재 / 초기화 실패 시 기존 hardcoded map
	// DB 우선 — Resolver wiring 성공 시 Scheduler 가 부팅 시 ListEnabled 결과로 spawn,
	// StartRefreshLoop 가 주기적 diff.
	entries := scheduler.DefaultEntries(schedulerCfg)
	sched := scheduler.New(entries, jobPublisher, log, schedulerCfg.MaxRetries)

	schedulerEntryRepoBase, schedRepoErr := pgstore.NewSchedulerEntryRepository(pool, log)
	if schedRepoErr != nil {
		log.WithError(schedRepoErr).Warn("scheduler entry repo construction failed — using static DefaultEntries fallback")
	} else {
		schedulerEntryRepo := decorator.WrapSchedulerEntryWithTimeout(schedulerEntryRepoBase, dbCfg.QueryTimeout)
		entryConverter := scheduler.NewDefaultEntryConverter(schedulerCfg.JobTimeout)
		entryResolver, resErr := scheduler.NewEntryResolver(schedulerEntryRepo, entryConverter, log, 0)
		if resErr != nil {
			log.WithError(resErr).Warn("scheduler entry resolver construction failed — using static DefaultEntries fallback")
		} else {
			sched.SetEntryResolver(entryResolver)
			log.Info("scheduler entries resolver enabled (DB-backed scheduler_entries, 30s refresh)")
		}
	}

	// Backlog throttle: SCHEDULER_MAX_BACKLOG > 0 일 때만 활성.
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
	// Resolver 가 wiring 되어 있으면 30s 주기 refresh — DB 변경 자동 반영.
	// resolver 자체 5min TTL cache 가 DB 부하 흡수, 짧은 refresh 가 stall 빈도 줄임.
	sched.StartRefreshLoop(ctx, 30*time.Second)

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

	// 이슈 #393 — validate worker 가 publisher facade 의존. validate 전용 producer 를
	// thin publisher 로 wrap (resolver/guard 불필요 — validate 는 Forward 만 사용).
	validatePublisher := bus.New(validateProducer, nil, log)

	// Validate StageGate (이슈 #356) — ProcessingLock + per-stage Semaphore 합성.
	validateCap := config.CapPerStage(workerCountsCfg.Validate, stageGateCfg.ValidateMaxConcurrentPerStage)
	validateGate := locks.BuildStageGate(locks.StageValidator, validateCap, procLock, log)
	if procLock != nil {
		log.WithFields(map[string]interface{}{
			"worker_count": workerCountsCfg.Validate,
			"capacity":     validateCap,
			"configured":   stageGateCfg.ValidateMaxConcurrentPerStage,
		}).Info("validate stage gate enabled (ProcessingLock + Semaphore)")
	} else {
		log.Warn("processing lock unavailable, validate stage gate falls back to noop")
	}

	validateWorker := validateWorkerPkg.NewWorker(validateConsumer, validatePublisher, contentSvc, validateGate, workerCountsCfg.Validate, validateCfg)

	log.WithFields(map[string]interface{}{
		"worker_count": workerCountsCfg.Validate,
		"input_topic":  queue.TopicNormalized,
		"output_topic": queue.TopicValidated,
	}).Info("validate worker constructed")

	// ══════════════════════════════════════════════════════════════════════════
	// Stage 통합 — processor.Stage 인터페이스로 모든 단계 균일 관리
	// ══════════════════════════════════════════════════════════════════════════

	fetcherStage, err := fetcher.NewStage(manager)
	if err != nil {
		log.WithError(err).Fatal("failed to construct fetcher stage")
	}
	parserStg, err := parser.NewStage(w, cleaner, llmGen, pathRefiner, log)
	if err != nil {
		log.WithError(err).Fatal("failed to construct parser stage")
	}
	validateStage, err := validate.NewStage(validateWorker)
	if err != nil {
		log.WithError(err).Fatal("failed to construct validate stage")
	}

	// chromedp 자동 upgrade 의 자가 회복 안전장치 — interval 마다 reason='auto_upgrade_validation'
	// row 를 goquery 로 reset. ENABLED=false 시 stage 미등록 (단계 3 의 upgrade-only 동작 유지).
	stages := []processor.Stage{fetcherStage, parserStg, validateStage}
	downgradeCfg, err := config.LoadFetcherAutoDowngrade()
	if err != nil {
		log.WithError(err).Fatal("failed to load fetcher auto-downgrade config")
	}
	if downgradeCfg.Enabled {
		downgrader, err := fetcherRule.NewDowngrader(fetcherRuleRepo, fetcherResolver, downgradeCfg.Interval, log)
		if err != nil {
			log.WithError(err).Fatal("failed to construct fetcher auto-downgrade stage")
		}
		stages = append(stages, downgrader)
		log.WithField("interval", downgradeCfg.Interval.String()).Info("fetcher auto-downgrade enabled")
	} else {
		log.Info("fetcher auto-downgrade disabled (FETCHER_AUTO_DOWNGRADE_ENABLED=false)")
	}
	for _, s := range stages {
		s.Start(ctx)
		log.WithField("stage", s.Name()).Info("pipeline stage started")
	}

	// ══════════════════════════════════════════════════════════════════════════
	// 종료 시그널 대기
	// ══════════════════════════════════════════════════════════════════════════

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	<-sigChan
	// 셧다운 시작 시점부터 logger 에 shutting_down=true 를 부여합니다.
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
	// context.WithoutCancel(ctx) 사용 — parent ctx 의 cancellation (방금 호출한 cancel())
	// 은 분리하되 ctx values (logger / 향후 trace ID 등) 는 상속 보존.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownCfg.Timeout)
	defer shutdownCancel()
	shutdownCtx = log.ToContext(shutdownCtx)

	sched.Stop()

	// Stage 들을 정의 순서대로 stop — fetcher → parser → validate 순서로 정리.
	// 데이터 파이프라인 source-first 원칙: fetcher 가 먼저 Kafka 발행을 멈추면 downstream
	// stage 가 in-flight 메시지를 graceful drain 가능. parser.Stage 내부에서 lifecycle
	// 의존성 (worker → llmGen → refiner → cleaner) 이 처리됨.
	for _, s := range stages {
		if err := s.Stop(shutdownCtx); err != nil {
			log.WithFields(map[string]interface{}{"stage": s.Name()}).WithError(err).Error("error during stage shutdown")
		} else {
			log.WithField("stage", s.Name()).Info("pipeline stage stopped")
		}
	}

	// Claude Code 컨테이너 종료 — stages.Stop 으로 llmGen 의 모든 in-flight Extract 가
	// 완료된 이후 호출. Worker.Stop 이 실행 중인 docker exec 세션 완료 대기 + 컨테이너 정리.
	//
	// shutdownCtx 가 stages.Stop 에서 timeout 으로 cancel 되더라도 docker rm -f 자체는 반드시 시도되어야
	// 컨테이너 누수가 발생하지 않으므로, 별도의 cleanupCtx 를 사용.
	// WithoutCancel(ctx) 로 ctx values 보존.
	if claudegenPool != nil {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownCfg.ClaudegenTimeout)
		cleanupCtx = log.ToContext(cleanupCtx)
		if err := claudegenPool.Stop(cleanupCtx); err != nil {
			log.WithError(err).Error("claudegen pool stop failed")
		} else {
			log.Info("claudegen pool stopped")
		}
		cleanupCancel()
	}

	log.Info("shutdown completed")
}
