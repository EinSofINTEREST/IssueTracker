package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	goredis "github.com/redis/go-redis/v9"
	"issuetracker/internal/locks"
	"issuetracker/internal/processor"
	"issuetracker/internal/processor/fetcher"
	"issuetracker/internal/processor/fetcher/core"
	"issuetracker/internal/processor/fetcher/domain/general/sources"
	"issuetracker/internal/processor/fetcher/handler"
	fetcherRule "issuetracker/internal/processor/fetcher/rule"
	crawlerWorker "issuetracker/internal/processor/fetcher/worker"
	"issuetracker/internal/processor/parser/rule"
	"issuetracker/internal/processor/parser/rule/claudegen"
	"issuetracker/internal/processor/parser/rule/llmgen"
	llmgenwiring "issuetracker/internal/processor/parser/rule/llmgen/wiring"
	refinerwiring "issuetracker/internal/processor/parser/rule/refiner/wiring"
	"issuetracker/internal/processor/parser/rule/validator"
	parserStage "issuetracker/internal/processor/parser/stage"
	parserWorker "issuetracker/internal/processor/parser/worker"
	"issuetracker/internal/processor/validate"
	"issuetracker/internal/publisher"
	"issuetracker/internal/scheduler"
	"issuetracker/internal/storage"
	pgstore "issuetracker/internal/storage/postgres"
	"issuetracker/internal/storage/service"
	"issuetracker/pkg/config"
	"issuetracker/pkg/links"
	llmwiring "issuetracker/pkg/llm/wiring"
	"issuetracker/pkg/logger"
	"issuetracker/pkg/metrics"
	"issuetracker/pkg/queue"

	"issuetracker/pkg/redis"
)

const (
	validateWorkerCount = 8
	// parserWorkerCount: TopicFetched consumer group (issuetracker-parsers) 의 worker 수.
	// fetcher worker 와 독립 — chromedp/LLM 등으로 parser 가 무거워질 때 별도 스케일.
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

	shutdownCfg, err := config.LoadShutdown()
	if err != nil {
		log.WithError(err).Fatal("failed to load shutdown config")
	}
	log.WithFields(map[string]interface{}{
		"shutdown_timeout":           shutdownCfg.Timeout.String(),
		"claudegen_shutdown_timeout": shutdownCfg.ClaudegenTimeout.String(),
	}).Info("shutdown timeouts loaded")

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

	// rule.Parser: parsing_rules 테이블 기반 단일 파서 엔진.
	// 사이트별 NaverParser/CNNParser/... 를 대체 — 모든 사이트가 본 단일 인스턴스를 공유.
	parsingRuleRepo := pgstore.NewParsingRuleRepository(pool, log)
	ruleResolver, err := rule.NewResolver(parsingRuleRepo)
	if err != nil {
		log.WithError(err).Fatal("failed to construct rule resolver")
	}
	// parsing_rules mutation → cache invalidate 자동 결합 (decorator 패턴).
	// 호출처가 명시적 Invalidate 를 까먹어도 stale cache 발생 X — single source of truth.
	parsingRuleRepo = storage.WrapWithInvalidator(parsingRuleRepo, ruleResolver)
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
	var blacklistMatcher *rule.BlacklistMatcher
	if blacklistCfg.Enabled {
		blacklistRepo := pgstore.NewBlacklistRepository(pool, log)
		bm, bmErr := rule.NewBlacklistMatcher(blacklistRepo)
		if bmErr != nil {
			log.WithError(bmErr).Fatal("failed to construct blacklist matcher")
		}
		// invalidatingBlacklistRepo decorator 는 현재 wiring 하지 않음.
		// read-only 경로 (Matcher.Filter) 만 사용 — application 측 mutation 경로 부재.
		// 운영 CLI / 자동 색출 후속 이슈에서 decorator 도입 + 변수 재할당으로 invalidate 결합.
		// (decorator 코드 자체는 blacklist_matcher.go 에 보존, unit test 로 검증.)
		blacklistMatcher = bm
		log.Info("page-parse blacklist enabled (parsing_blacklist DB-backed)")
	} else {
		log.Info("page-parse blacklist disabled (BLACKLIST_ENABLED=false)")
	}

	// Readiness check: 사이트 등록 전 parsing_rules 가 seed 됐는지 검증.
	// 부재 시 fail-fast — 실행 중 모든 ParsePage/ParseLinks 가 ErrNoRule 로 죽는 것보다 즉시 종료.
	// migration 007 (또는 동등한 운영자 seed) 가 적용되어야 통과.
	if err := rule.VerifySeeded(ctx, ruleResolver); err != nil {
		log.WithError(err).Fatal("parsing_rules seed missing — apply migration 007 before deploy")
	}

	// raw_contents 서비스 — fetcher 측 Claim Check 저장 + parser 측 로드/삭제.
	rawRepo := pgstore.NewRawContentRepository(pool, log)
	rawSvc := service.NewRawContentService(rawRepo, log)

	contentRepo := pgstore.NewContentRepository(pool, log)
	contentSvc := service.NewContentService(contentRepo, log)

	// host 단위 fetcher 룰 — fetcher_rules 테이블 + Resolver wiring.
	// 룰 부재 host 는 default chain (현재 동작 100% 보존).
	fetcherRuleRepo, err := pgstore.NewFetcherRuleRepository(pool, log)
	if err != nil {
		log.WithError(err).Fatal("failed to construct fetcher rule repository")
	}
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
	if err := sources.RegisterAll(ctx, registry, fetcherRuleRepo, core.DefaultConfig(), rawSvc, crawlerProducer, fetcherResolver, chromedpRemoteURLs, log); err != nil {
		log.WithError(err).Fatal("failed to register crawlers from db")
	}

	// Redis 기반 ProcessingLock: 동일 URL 이 여러 worker/인스턴스에서 단계별 (fetcher/parser/validator)
	// 중복 처리되는 것을 방지합니다. 단일 인스턴스를 fetcher / parser / validator 가 공유 —
	// 단계 구분은 ProcessingKey(stage, url) 의 stage prefix 로 처리.
	// worker/manager 가 nil 을 NoopProcessingLock 로 fallback 처리하는 설계와 일관되게,
	// Redis 초기화 실패 시에도 크롤링이 중단되지 않도록 graceful degrade 합니다.
	var procLock locks.ProcessingLock
	var ingestionLock locks.IngestionLock
	var retryScheduler crawlerWorker.RetryScheduler
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

	// URL dedup — Ingestion Lock → Pipeline Guard 통합:
	// Publisher / Scheduler / ParserWorker 가 동일 guard 를 공유하여 target type 별 정책 적용:
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

	managerCfg := crawlerWorker.ManagerConfig{
		High:           crawlerWorker.PoolConfig{Consumer: highConsumer, WorkerCount: 3},
		Normal:         crawlerWorker.PoolConfig{Consumer: normalConsumer, WorkerCount: 6},
		Low:            crawlerWorker.PoolConfig{Consumer: lowConsumer, WorkerCount: 2},
		ProcessingLock: procLock,
		RetryScheduler: retryScheduler,
	}

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

	manager := crawlerWorker.NewPoolManager(managerCfg, crawlerProducer, registry, contentSvc, resolver, log)

	log.WithFields(map[string]interface{}{
		"high_workers":   managerCfg.High.WorkerCount,
		"normal_workers": managerCfg.Normal.WorkerCount,
		"low_workers":    managerCfg.Low.WorkerCount,
	}).Info("fetcher pool manager constructed")

	// ── LLM rule generator ──────────────────────────────────────────
	// rule.ErrNoRule (host 매칭 활성 규칙 없음) fallback 으로 LLM 이 selector 를 자동 생성합니다.
	// LLM_ENABLED=false 또는 API key 누락 시 nil — parser worker 는 ErrNoRule 시 raw 만 잔존.
	//
	// **현재 정책**: FixedOrder("gemini") 정책으로 Gemini 단일 provider 사용 (1000회/일 무료 한도 내 검증).
	// 후속 PR (이슈 TBD) 에서 chain (gemini → openai → anthropic) 으로 정책 확장.
	//
	// 동일 provider 를 refiner 와 공유 — 환경변수 1세트로 동시 제어.
	llmProvider := llmwiring.BuildProvider(log)
	llmGen, err := llmgenwiring.Build(llmProvider, parsingRuleRepo, ruleResolver, redisClientShared, log)
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
	var failureCounter fetcherRule.FailureCounter = fetcherRule.NewNoopFailureCounter()
	if fetcherUpgradeCfg.Enabled && redisClientShared != nil {
		fc, fcErr := fetcherRule.NewRedisFailureCounter(
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
	var rawIDTracker fetcherRule.RawIDTracker = fetcherRule.NewNoopRawIDTracker()
	if fetcherUpgradeCfg.Enabled && redisClientShared != nil {
		t, tErr := fetcherRule.NewRedisRawIDTracker(
			redisClientShared.Raw(),
			fetcherUpgradeCfg.Window,
			"", // default keyPrefix "fetcher:failed_raws"
			log,
		)
		if tErr != nil {
			log.WithError(tErr).Warn("failed to construct redis raw id tracker, falling back to noop")
		} else {
			rawIDTracker = t
			log.Info("fetcher raw id tracker (redis set) enabled")
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
			crawlerProducer,
			redisRaw, // nil 허용 — in-flight lock 비활성 (단일 인스턴스)
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
	sampleRepo := pgstore.NewSampleURLRepository(pool, log)

	pw := parserWorker.NewParserWorker(
		parserConsumer,
		crawlerProducer, // normalized 토픽 발행 + chained article jobs 발행 시 publisher 가 동일 producer 사용
		rawSvc,
		contentSvc,
		jobPublisher,
		ruleParser,
		ruleResolver, // sample 누적 시 매칭 rule lookup
		sampleRepo,
		procLock, // fetcher / parser / validator 가 동일 ProcessingLock 인스턴스 공유
		llmGen,
		failureCounter,  // host 단위 fetcher 실패 카운터
		rawIDTracker,    // host 별 실패 raw_id 추적기
		fetcherUpgrader, // 임계값 도달 시 chromedp 자동 전환 + republish
		fetcherUpgradeCfg.EmptyBodyTitleMin,
		fetcherUpgradeCfg.EmptyBodyContentMin,
		parserWorkerCount,
		log,
	)

	// ── Pipeline Guard release ─────────────────────────────────────
	// Category cycle 종료 시 marker release — scheduler 다음 주기에 즉시 진입 가능.
	if pipelineGuard != nil {
		pw.SetPipelineGuard(pipelineGuard)
	}

	// ── Page-parse 블랙리스트 ───────────────────────────────────────
	// 카테고리에서 추출된 article URL 중 blacklist 매칭은 publisher.Publish 직전 drop.
	// Matcher 가 nil (BLACKLIST_ENABLED=false) 이면 setter noop — 모든 링크 그대로 발행.
	if blacklistMatcher != nil {
		pw.SetBlacklist(blacklistMatcher)
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
		sc, scErr := llmgen.NewRedisStaleCounter(
			redisClientShared.Raw(),
			staleRelearnCfg.Threshold,
			staleRelearnCfg.Window,
			"", // default keyPrefix "stale:relearn"
			log,
		)
		if scErr != nil {
			log.WithError(scErr).Warn("failed to construct redis stale counter, stale relearn disabled at runtime")
		} else {
			pw.SetStaleCounter(sc, staleRelearnCfg.Threshold)
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
		llmGen.SetValidateFailureHandler(pw.RequeueForLLMRetry)
	}

	// ── Pending URL 큐 ────────────────────────────────────────────
	// in-flight 중 동일 도메인으로 유입된 URL 을 Redis LIST 에 보존.
	// 룰 생성 완료 시 대기 URL 을 issuetracker.fetched 에 재발행 — 새 룰로 재파싱.
	// Redis 미설정 시 graceful degrade (pending URL 보존 없이 기존 skip 동작 유지).
	if llmGen != nil && redisClientShared != nil {
		llmGen.SetPendingQueue(
			llmgen.NewRedisPendingQueue(redisClientShared.Raw()),
			pw.RequeueParsing,
		)
		log.Info("llmgen: Redis 기반 pending URL 큐 활성화")
	}

	// ── 의미 검증 ValidatorPool ──────────────────────────────────
	// DOM 매칭 검증 통과 후 추출 내용이 실제 뉴스 제목/본문인지 LLM 으로 의미 검증.
	// llmProvider 가 nil(LLM 비활성) 이면 의미 검증 건너뜀 — DOM 검증만 수행.
	if llmGen != nil && llmProvider != nil {
		semPool := validator.NewPool(log,
			validator.NewLLMValidator(llmProvider),
		)
		llmGen.SetSelectorValidator(validator.NewLLMGenAdapter(semPool))
		log.Info("llmgen: 의미 검증 ValidatorPool 활성화")
	}

	// ── Claude Code 추출기 ──────────────────────────────────────
	// LLM_EXTRACTOR=claude-code 일 때 활성화 — Claude 구독 환경의 sonnet 으로 셀렉터 추출.
	// 미지정 / gemini (기본) 일 때는 buildLLMGenerator 가 설정한 기본 LLM provider 추출.
	// Start 실패 시 Gemini 경로로 graceful fallback (fatal 아님).
	// 종료 시 stages.Stop 이후 worker.Stop 호출 — llmGen.Stop 으로 in-flight Extract 완료 보장 후 컨테이너 정리.
	var claudegenWorker *claudegen.ClaudeWorker
	llmExtractor := os.Getenv("LLM_EXTRACTOR")
	switch {
	case llmExtractor != "claude-code":
		// 기본 경로 — 분기 미발생 (Gemini 등 buildLLMGenerator 의 provider 사용).
	case llmGen == nil:
		// LLM_ENABLED=false / API key 부재 등으로 llmGen 비활성 — silent skip 회피.
		log.Warn("LLM_EXTRACTOR=claude-code requested but LLM generator is disabled (check LLM_ENABLED / API key); claudegen extractor not registered")
	default:
		worker, werr := claudegen.NewFromEnv(log)
		if werr != nil {
			log.WithError(werr).Warn("claudegen worker construction failed, falling back to default extractor")
		} else if serr := worker.Start(ctx); serr != nil {
			log.WithError(serr).Warn("claudegen worker start failed, falling back to default extractor")
		} else {
			llmGen.SetExtractor(worker)
			claudegenWorker = worker
			log.Info("llmgen: Claude Code 웜 컨테이너 추출기 활성화")
		}
	}

	// ── Refiner ──────────────────────────────────────────
	// catch-all + llm-auto rule 의 누적 sample URL 로부터 path_pattern 정밀화.
	// REFINEMENT_ENABLED=false 또는 config 실패 시 nil — 기존 catch-all rule 그대로 동작.
	// metricsRegistry 는 nil 허용 — Record* 호출이 noop.
	pathRefiner, err := refinerwiring.Build(llmProvider, parsingRuleRepo, sampleRepo, ruleResolver, metricsRegistry, log)
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

	emitter := scheduler.NewJobEmitter(crawlerProducer, log)
	if pipelineGuard != nil {
		emitter.SetGuard(pipelineGuard)
		// publisher 와 동일 normalizer 공유 — marker 키 일관성.
		emitter.SetNormalizer(links.NewNormalizer())
		log.Info("scheduler emitter pipeline guard enabled")
	}
	entries := scheduler.DefaultEntries(schedulerCfg)
	sched := scheduler.New(entries, emitter, log, schedulerCfg.MaxRetries)

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

	log.WithFields(map[string]interface{}{
		"worker_count": validateWorkerCount,
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
	parserStg, err := parserStage.NewStage(pw, cleaner, llmGen, pathRefiner, log)
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
	if claudegenWorker != nil {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownCfg.ClaudegenTimeout)
		cleanupCtx = log.ToContext(cleanupCtx)
		if err := claudegenWorker.Stop(cleanupCtx); err != nil {
			log.WithError(err).Error("claudegen worker stop failed")
		} else {
			log.Info("claudegen worker stopped")
		}
		cleanupCancel()
	}

	log.Info("shutdown completed")
}
