package main

import (
	"context"
	appcfg "issuetracker/pkg/config/app"
	fetchercfg "issuetracker/pkg/config/fetcher"
	llmcfg "issuetracker/pkg/config/llm"
	processorcfg "issuetracker/pkg/config/processor"
	runtimecfg "issuetracker/pkg/config/runtime"
	storagecfg "issuetracker/pkg/config/storage"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"issuetracker/internal/bus"
	"issuetracker/internal/locks"
	"issuetracker/internal/processor"
	"issuetracker/internal/processor/enrich"
	enrichcore "issuetracker/internal/processor/enrich/core"
	enrichWorkerPkg "issuetracker/internal/processor/enrich/worker"
	"issuetracker/internal/processor/fetcher"
	"issuetracker/internal/processor/fetcher/core"
	"issuetracker/internal/processor/fetcher/domain/general/sources"
	"issuetracker/internal/processor/fetcher/domain/search"
	"issuetracker/internal/processor/fetcher/handler"
	fetcherRule "issuetracker/internal/processor/fetcher/rule"
	crawlerWorker "issuetracker/internal/processor/fetcher/worker"
	"issuetracker/internal/processor/parser"
	"issuetracker/internal/processor/parser/rule"
	llmgenwiring "issuetracker/internal/processor/parser/rule/llmgen/wiring"
	refinerwiring "issuetracker/internal/processor/parser/rule/refiner/wiring"
	"issuetracker/internal/processor/parser/rule/validator"
	parserWorker "issuetracker/internal/processor/parser/worker"
	"issuetracker/internal/processor/precheck"
	"issuetracker/internal/processor/validate"
	validateWorkerPkg "issuetracker/internal/processor/validate/worker"
	"issuetracker/internal/scheduler"
	"issuetracker/internal/storage/decorator"
	"issuetracker/internal/storage/model"
	pgstore "issuetracker/internal/storage/postgres"
	"issuetracker/internal/storage/primitive"
	redisstore "issuetracker/internal/storage/redis"
	"issuetracker/internal/storage/service"
	"issuetracker/pkg/agent/claude"
	agentdb "issuetracker/pkg/agent/dependency/db"
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

	logCfg, err := appcfg.LoadLog()
	if err != nil {
		log.WithError(err).Fatal("failed to load log config")
	}
	loggerCfg := logger.DefaultConfig()
	loggerCfg.Level = logger.Level(logCfg.Level)
	loggerCfg.Pretty = logCfg.Pretty
	log = logger.New(loggerCfg)

	log.Info("starting IssueTracker")

	shutdownCfg, err := appcfg.LoadShutdown()
	if err != nil {
		log.WithError(err).Fatal("failed to load shutdown config")
	}
	log.WithFields(map[string]interface{}{
		"shutdown_timeout":           shutdownCfg.Timeout.String(),
		"claudegen_shutdown_timeout": shutdownCfg.ClaudegenTimeout.String(),
	}).Info("shutdown timeouts loaded")

	// 모든 stage 의 worker goroutine 수를 env 로 노출 (이슈 #376).
	// default 는 기존 hardcoded 값과 동일 — env 미설정 시 동작 100% 보존.
	workerCountsCfg, err := runtimecfg.LoadWorkerCounts()
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

	// Stage toggle (이슈 #443) — env 로 stage 별 활성/비활성 제어. 모두 true 가 default
	// 이므로 env 미설정 시 동작 100% 보존. fetcher-only / parser-only / validate-only
	// 노드를 같은 바이너리로 띄울 수 있도록 stage Start 호출만 gating.
	stagesCfg, err := runtimecfg.LoadStages()
	if err != nil {
		log.WithError(err).Fatal("failed to load stages config")
	}
	log.WithFields(map[string]interface{}{
		"fetcher":   stagesCfg.FetcherEnabled,
		"parser":    stagesCfg.ParserEnabled,
		"validate":  stagesCfg.ValidateEnabled,
		"enrich":    stagesCfg.EnrichEnabled,
		"scheduler": stagesCfg.SchedulerEnabled,
	}).Info("stage toggles loaded")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ctx = log.ToContext(ctx)

	// ── Metrics endpoint ──────────────────────────────────────────
	// METRICS_ADDR 빈 값이면 endpoint 비활성화. default ":9090".
	metricsCfg, err := appcfg.LoadMetrics()
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

	// Redis client 조기 초기화 — JobBuffer / ProcessingLock / IngestionLock / RetryScheduler 가
	// 모두 공유 (이슈 #510). 실패 시 graceful degrade — 모든 Redis 기반 기능은 noop fallback.
	var redisClientShared *redis.Client
	redisCfg, err := storagecfg.LoadRedis()
	if err != nil {
		log.WithError(err).Warn("failed to load redis config, falling back to noop processing/ingestion lock and disabled job buffer")
	} else {
		redisClient, redisErr := redis.New(ctx, redisCfg)
		if redisErr != nil {
			log.WithError(redisErr).Warn("failed to connect to redis, falling back to noop processing/ingestion lock and disabled job buffer")
		} else {
			defer redisClient.Close()
			redisClientShared = redisClient
			log.WithFields(map[string]interface{}{
				"host": redisCfg.Host,
				"port": redisCfg.Port,
			}).Info("redis connected (shared by lock / ingestion lock / job buffer / retry scheduler)")
		}
	}

	// 이슈 #510 — normal/low priority crawl 토픽 Redis 버퍼링 (opt-in).
	// Enabled + Redis 연결 시 BufferingProducer 데코레이터 + BufferDrainer goroutine 활성화.
	// 비활성 시 raw KafkaProducer 사용 (기존 동작 보존).
	jobBufferCfg, err := processorcfg.LoadJobBuffer()
	if err != nil {
		log.WithError(err).Fatal("failed to load job buffer config")
	}

	rawCrawlerProducer := queue.NewProducer(crawlerKafkaCfg)
	defer rawCrawlerProducer.Close()

	var crawlerProducer queue.Producer = rawCrawlerProducer
	var bufferDrainer *scheduler.BufferDrainer // nil 이면 기능 비활성 — Stop() skip

	if jobBufferCfg.Enabled && redisClientShared != nil {
		bufferingProducer := queue.NewBufferingProducer(rawCrawlerProducer, redisClientShared, jobBufferCfg.MaxLen, log)
		crawlerProducer = bufferingProducer

		// 이슈 #512 — leader locker: 다중 인스턴스 환경에서 한 인스턴스만 drain.
		// TTL 은 drain interval × 1.1 — 한 cycle 안전 커버 + leader crash 후 빠른 회수.
		leaderTTL := jobBufferCfg.DrainInterval + jobBufferCfg.DrainInterval/10
		leaderLocker, llErr := redis.NewLeaderLocker(redisClientShared.Raw(), "buffer_drainer", leaderTTL)
		if llErr != nil {
			log.WithError(llErr).Fatal("failed to construct buffer drainer leader locker")
		}

		drainer, derr := scheduler.NewBufferDrainer(
			redisClientShared,
			rawCrawlerProducer, // drainer 는 underlying 으로 직접 publish — 무한 루프 회피
			queue.NewBacklogChecker(crawlerKafkaCfg.Brokers, jobBufferCfg.CheckTimeout),
			scheduler.BufferDrainerConfig{
				Interval:      jobBufferCfg.DrainInterval,
				TargetBacklog: jobBufferCfg.TargetBacklog,
				DrainBatch:    jobBufferCfg.DrainBatch,
				MaxLen:        jobBufferCfg.MaxLen,
				CheckTimeout:  jobBufferCfg.CheckTimeout,
				GroupID:       queue.GroupCrawlerWorkers,
				Leader:        leaderLocker, // 이슈 #512 — election
				// RetryScheduler 는 jobPublisher 생성 후 SetRetryScheduler 로 late binding
			},
			log,
		)
		if derr != nil {
			log.WithError(derr).Fatal("failed to construct buffer drainer")
		}
		bufferDrainer = drainer
		log.WithFields(map[string]interface{}{
			"drain_interval": jobBufferCfg.DrainInterval.String(),
			"target_backlog": jobBufferCfg.TargetBacklog,
			"drain_batch":    jobBufferCfg.DrainBatch,
			"max_len":        jobBufferCfg.MaxLen,
			"leader_ttl":     leaderTTL.String(),
		}).Info("publisher redis buffer enabled (normal/low priority, multi-instance leader election)")
	} else if jobBufferCfg.Enabled && redisClientShared == nil {
		log.Warn("publisher redis buffer requested but redis unavailable — falling back to direct kafka publish")
	}

	// 이슈 #391 — PriorityResolver chain 이 publisher 측으로 이동 + 모든 PublishX 가
	// resolver 통과 (메타 #385 Sub 6). ExplicitPriorityResolver 를 chain 1순위 로 등록 —
	// 발행자가 job.Priority 를 사전 명시한 경우 (seed entry / retry / upgrade) 그 값이 보존됨.
	//
	// 이슈 #521 — RuleBasedPriorityResolver 가 parser_rules.crawl_priority 컬럼을 hydrate
	// 하여 host/path 기반 priority 분기. ruleBased 인스턴스를 변수로 보유해 후속 단계에서
	// PriorityRulesRefresher 가 hydrate 가능 (pool 생성 이후).
	resolver := bus.NewCompositeResolver(core.PriorityNormal)
	resolver.Add(&bus.ExplicitPriorityResolver{})
	resolver.Add(bus.NewSourcePriorityResolver(core.PriorityNormal))
	ruleBasedResolver := bus.NewRuleBasedPriorityResolver(core.PriorityNormal)
	resolver.Add(ruleBasedResolver)

	highConsumer := queue.NewConsumer(crawlerKafkaCfg, queue.TopicCrawlHigh)
	defer highConsumer.Close()
	normalConsumer := queue.NewConsumer(crawlerKafkaCfg, queue.TopicCrawlNormal)
	defer normalConsumer.Close()
	lowConsumer := queue.NewConsumer(crawlerKafkaCfg, queue.TopicCrawlLow)
	defer lowConsumer.Close()

	registry := handler.NewRegistry(log)

	dbCfg, err := storagecfg.Load()
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
	//
	// ParserRuleService (이슈 #431) 가 decorator chain (timeout + invalidating) 을 자동 합성 —
	// resolver / invalidator wiring 은 service 내부에서 처리.
	parserRuleRepoRaw := pgstore.NewParserRuleRepository(pool, log)
	// resolver 는 timeout decorator 만 적용된 repo 로 lookup (cache invalidate 는 service 가 처리).
	ruleResolver, err := rule.NewResolver(decorator.WrapParserRuleWithTimeout(parserRuleRepoRaw, dbCfg.QueryTimeout))
	if err != nil {
		log.WithError(err).Fatal("failed to construct rule resolver")
	}
	parserRuleSvc := service.NewParserRuleService(
		parserRuleRepoRaw,
		log,
		service.WithParserRuleQueryTimeout(dbCfg.QueryTimeout),
		service.WithParserRuleInvalidator(ruleResolver),
	)

	// 이슈 #521 — RuleBasedPriorityResolver hydrate. parser_rules 의 enabled 룰 (target_type=page)
	// 에서 (host_pattern, path_pattern, crawl_priority) 추출 후 atomic 으로 resolver 에 주입.
	//
	// target_type=page 만 로드 — Copilot #3274137388 피드백: 같은 host 의 page/list 룰이 priority
	// 가 다를 경우 모호. URL crawl priority 분기는 page (article body) 단위가 자연스러우므로
	// 본 단계에서는 page 만 hydrate.
	//
	// Limit (default 10000, env PRIORITY_RULES_LOAD_LIMIT) 도달 시 WARN — silent truncation 회피
	// (gemini #3274133287 / Copilot #3274137421 피드백).
	//
	// 부팅 직후 1회 + 주기 refresh (default 5분, env PRIORITY_RULES_REFRESH_INTERVAL).
	priorityRefreshInterval := envDurationOrDefault("PRIORITY_RULES_REFRESH_INTERVAL", 5*time.Minute)
	priorityLoadLimit := envIntOrDefault("PRIORITY_RULES_LOAD_LIMIT", 10000)
	priorityLoader := func(loadCtx context.Context) ([]bus.HostPathPriorityRule, error) {
		records, err := parserRuleRepoRaw.List(loadCtx, model.ParserRuleFilter{
			OnlyEnabled: true,
			TargetType:  model.TargetTypePage,
			Limit:       priorityLoadLimit,
		})
		if err != nil {
			return nil, err
		}
		if len(records) == priorityLoadLimit {
			log.WithField("limit", priorityLoadLimit).Warn("priority rules load reached limit, possible truncation — raise PRIORITY_RULES_LOAD_LIMIT")
		}
		rules := make([]bus.HostPathPriorityRule, 0, len(records))
		for _, rec := range records {
			rules = append(rules, bus.HostPathPriorityRule{
				HostPattern: rec.HostPattern,
				PathPattern: rec.PathPattern,
				Priority:    priorityFromInt16(rec.CrawlPriority),
			})
		}
		return rules, nil
	}
	priorityRefresher := bus.NewPriorityRulesRefresher(ruleBasedResolver, priorityLoader, priorityRefreshInterval, log)
	priorityRefresher.Start(ctx)
	log.WithFields(map[string]interface{}{
		"interval_ms": priorityRefreshInterval.Milliseconds(),
		"load_limit":  priorityLoadLimit,
	}).Info("priority rules refresher started")
	// page-parse 블랙리스트 — 카테고리 → article job 발행 단계에서 매칭 URL 차단.
	// Enabled=false 시 Matcher 미주입 → parser_worker 가 모든 링크 그대로 발행 (기능 OFF).
	//
	// BlacklistService (이슈 #431) 가 decorator chain (timeout + invalidating cache) 을 내부 합성 +
	// HandleLLMDecision 비즈니스 로직 캡슐화. Matcher 는 별도 구성 — read-only cache + match.
	//
	// 이슈 #477 — ruleParser 의 index-only 자동 강등 옵션에 blacklistSvc 를 주입하기 위해
	// blacklist 구성 후 ruleParser 를 생성하도록 순서 정렬.
	blacklistCfg, err := processorcfg.LoadBlacklist()
	if err != nil {
		log.WithError(err).Fatal("failed to load blacklist config")
	}
	var (
		blacklistMatcher *rule.BlacklistMatcher
		blacklistSvc     service.BlacklistService // llmGen.SetBlacklistService 에 전달 (#326, #431).
	)
	if blacklistCfg.Enabled {
		blacklistRepoRaw := pgstore.NewBlacklistRepository(pool, log)
		// Matcher 는 timeout decorator 만 적용된 repo 로 lookup (cache invalidate 는 service 가 처리).
		bm, bmErr := rule.NewBlacklistMatcher(decorator.WrapBlacklistWithTimeout(blacklistRepoRaw, dbCfg.QueryTimeout))
		if bmErr != nil {
			log.WithError(bmErr).Fatal("failed to construct blacklist matcher")
		}
		blacklistMatcher = bm
		blacklistSvc = service.NewBlacklistService(
			blacklistRepoRaw,
			log,
			service.WithBlacklistQueryTimeout(dbCfg.QueryTimeout),
			service.WithBlacklistInvalidator(bm),
		)
		log.Info("page-parse blacklist enabled (parser_blacklist DB-backed)")
	} else {
		log.Info("page-parse blacklist disabled (BLACKLIST_ENABLED=false)")
	}

	// ruleParser 생성 — blacklistSvc 가 있으면 index-only 자동 강등 옵션 활성 (이슈 #477).
	// blacklistCfg.Enabled=false 환경에서는 blacklistSvc 가 nil → 옵션이 noop → 기존 동작 유지.
	autoDemoteMetrics := rule.NewAutoDemoteMetrics(metricsRegistry)
	parserOpts := []rule.ParserOption{}
	if blacklistSvc != nil {
		parserOpts = append(parserOpts, rule.WithBlacklistAutoDemote(blacklistSvc, autoDemoteMetrics, log))
	}
	ruleParser, err := rule.NewParser(ruleResolver, parserOpts...)
	if err != nil {
		log.WithError(err).Fatal("failed to construct rule parser")
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
	chromedpPoolCfg, err := fetchercfg.LoadFetcherChromedpPool()
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
	googleCSECfg, err := llmcfg.LoadGoogleCSE()
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

	// Redis 기반 ProcessingLock / IngestionLock / DelayedRetryScheduler — redisClientShared 가
	// 위쪽에서 이미 초기화됨 (이슈 #510 — JobBuffer 와 client 공유). 본 블록은 lock/retry 만 wire.
	// 단일 인스턴스를 fetcher / parser / validator 가 공유 — 단계 구분은 ProcessingKey(stage, url)
	// 의 stage prefix 로 처리. worker/manager 가 nil 을 NoopProcessingLock 로 fallback 처리.
	var procLock locks.ProcessingLock
	var ingestionLock locks.IngestionLock
	var retryScheduler bus.RetryScheduler
	var retrySchedulerStop func()
	if redisClientShared != nil {
		procLock = locks.NewRedisProcessingLock(redisClientShared, locks.DefaultProcessingLockTTL)
		ingestionLock = locks.NewRedisIngestionLock(redisClientShared, redisCfg.IngestionLockTTL)

		// Delayed retry queue: retry 를 Redis ZSET 에 보관하고 별도
		// goroutine 이 ScheduledAt 도달 시 Kafka 에 발행 — worker 슬롯 점유 회피.
		// Redis 부재 시 worker 가 lazy 로 KafkaImmediateRetryScheduler 를 사용 (기존 동작).
		retryCfg := bus.DefaultRedisRetrySchedulerConfig()
		// idle heartbeat 압축 (이슈 #370) — pkg/config 로 env 로드 일관성 유지.
		retrySchedCfg, retrySchedErr := runtimecfg.LoadRetryScheduler()
		if retrySchedErr != nil {
			log.WithError(retrySchedErr).Fatal("RETRY_HEARTBEAT_EVERY_N_IDLE_TICKS 로드 실패")
		}
		retryCfg.HeartbeatEveryNIdleTicks = retrySchedCfg.HeartbeatEveryNIdleTicks
		redisRetry := bus.NewRedisDelayedRetryScheduler(
			redisClientShared, jobPublisher,
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

		// 이슈 #512 — BufferDrainer 가 publish 실패 시 Kafka 재시도 경로 사용. late binding —
		// drainer 가 redis client 의존으로 위쪽에서 먼저 생성됐기에 본 시점에 setter 로 주입.
		if bufferDrainer != nil {
			bufferDrainer.SetRetryScheduler(retryScheduler)
			log.Info("buffer drainer retry scheduler wired (publish-fail → kafka retry path)")
		}
		log.Info("redis delayed retry queue enabled (worker slot occupancy on retry resolved)")
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
	stageGateCfg, err := runtimecfg.LoadStageGate()
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
		"high_cap":   runtimecfg.CapPerStage(managerCfg.High.WorkerCount, stageGateCfg.FetcherMaxConcurrentPerStage),
		"normal_cap": runtimecfg.CapPerStage(managerCfg.Normal.WorkerCount, stageGateCfg.FetcherMaxConcurrentPerStage),
		"low_cap":    runtimecfg.CapPerStage(managerCfg.Low.WorkerCount, stageGateCfg.FetcherMaxConcurrentPerStage),
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
		promptCfg, pcErr := llmcfg.LoadPrompt()
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

	llmGen, err := llmgenwiring.Build(llmProvider, parserRuleSvc, ruleResolver, promptLoader, redisClientShared, redisCfg.InflightLockTTL, log)
	if err != nil {
		log.WithError(err).Fatal("failed to build llmgen generator")
	}

	// ── Fetcher 실패 카운터 ────────────────────────────────────────
	// host 단위 fetcher 실패를 sliding window 로 누적 — 단계 3 (#221) 의 chromedp 자동 전환
	// 트리거 입력. ENABLED=false 또는 Redis 미연결 시 Noop (성능 저하 0).
	fetcherUpgradeCfg, err := fetchercfg.LoadFetcherAutoUpgrade()
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
			resolver,     // 이슈 #521 — host/path 기반 priority 결정
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
	parserCap := runtimecfg.CapPerStage(workerCountsCfg.Parser, stageGateCfg.ParserMaxConcurrentPerStage)
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

	// ── Precheck 게이트 (이슈 #425) ───────────────────────────────────
	// fetcher / parser 진입점에서 URL 처리 가부를 일괄 판정. 현재 단일 Source (blacklist) 등록 —
	// 향후 rate_limit / robots / domain_throttle 등 추가 시 본 wiring 만 확장.
	//
	// blacklistMatcher 가 nil (BLACKLIST_ENABLED=false) 이면 NewBlacklistSource 가 nil 반환,
	// precheck.New 가 nil source 를 자동 필터링 → 모든 URL Allow (게이트 비활성).
	precheckDecider := precheck.New(precheck.NewBlacklistSource(blacklistMatcher))
	// parser worker — 진입점 + outgoing chained URL filter.
	w.SetPrecheck(precheckDecider)
	// fetcher worker pool manager — 모든 priority pool + chromedp pool 에 일괄 주입.
	manager.SetPrecheck(precheckDecider)

	// ── Stale rule 재학습 카운터 ───────────────────────────────────
	// host 단위 stale parse failure 누적 — 임계 도달 시 Generator.EnqueueStale 트리거.
	// FetcherAutoUpgrade 와 별개 keyspace + 더 긴 윈도우 / 더 높은 임계값 — chromedp 전환이
	// 먼저 시도되고, 그래도 실패 지속 시 LLM 재학습 (InsertNextVersion 으로 v+1 추가).
	staleRelearnCfg, err := processorcfg.LoadStaleRelearn()
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
	// 이슈 #352 — 단일 worker → Pool (N replica, default 2) 로 throughput 향상.
	// CLAUDE_CODE_WORKER_COUNT 로 운영자 조정 가능. 기존 단일 worker 동작은 N=1 로 동등 재현 가능.
	var claudegenPool *claude.Pool
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
		pool, perr := claude.NewPoolFromEnv(promptLoader, log)
		if perr != nil {
			log.WithError(perr).Warn("claudegen pool construction failed, falling back to default extractor")
		} else {
			// 이슈 #472 — enricher_ro DSN 이 설정되어 있으면 MCP postgres 도구를 풀에 mount.
			// DSN 미설정 시 nil 유지 → 기존 동작 (MCP 없이 RunSession) 그대로.
			if mcp, mErr := buildEnricherROMCPConfig(log); mErr != nil {
				log.WithError(mErr).Warn("enricher_ro MCP config build failed; claudegen pool will run without DB tool")
			} else if mcp != nil {
				pool.WithMCPConfig(mcp)
				log.Info("claudegen pool: MCP postgres read-only tool mounted (issue #472)")
			}
			if serr := pool.Start(ctx); serr != nil {
				log.WithError(serr).Warn("claudegen pool start failed, falling back to default extractor")
			} else {
				llmGen.SetExtractor(pool)
				claudegenPool = pool
				log.WithFields(map[string]interface{}{
					"worker_count": pool.WorkerCount(),
				}).Info("llmgen: Claude Code 웜 컨테이너 pool 활성화")
			}
		}
	}

	// claudegen 의 EnrichedExtractor 분기에서 페이지를 blacklist 로 판정 시 자동 등록할
	// service 주입 (이슈 #326, #431). blacklistSvc 가 nil (BLACKLIST_ENABLED=false) 이면
	// Generator 내부 분기가 셀렉터 INSERT skip 만 보장.
	if blacklistSvc != nil && llmGen != nil {
		llmGen.SetBlacklistService(blacklistSvc)
		log.Info("llmgen: 자동 blacklist 등록 활성화 (claudegen multi-step extraction)")
	}

	// ── Refiner ──────────────────────────────────────────
	// catch-all + llm-auto rule 의 누적 sample URL 로부터 path_pattern 정밀화.
	// REFINEMENT_ENABLED=false 또는 config 실패 시 nil — 기존 catch-all rule 그대로 동작.
	// metricsRegistry 는 nil 허용 — Record* 호출이 noop.
	pathRefiner, err := refinerwiring.Build(llmProvider, promptLoader, parserRuleSvc, sampleRepo, ruleResolver, metricsRegistry, log)
	if err != nil {
		log.WithError(err).Fatal("failed to build refiner")
	}

	// Cleanup cron — parser worker 가 처리하지 못한 채 잔존한 raw_contents row 정리.
	// 정상 흐름에서는 거의 동작 안 함. crash / rule.Error 잔존 / LLM 재처리 윈도우 만료된 row 만 대상.
	cleaner := parserWorker.NewRawContentCleaner(rawSvc, parserWorker.CleanupConfig{}, log)

	// ── Scheduler (시드 Job 발행) ─────────────────────────────────────────────
	schedulerCfg, err := processorcfg.LoadScheduler()
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
	//
	// 이슈 #510 — Redis 버퍼 활성 시 throttle wire skip:
	//   - BacklogThrottler 는 scheduler.runEntry 에서 publish 직전 normal/low job 을 silent drop
	//   - 본 PR 의 BufferingProducer 는 publish 단계에서 normal/low 를 Redis 로 routing
	//   - 둘 다 활성화하면 throttle drop 이 BufferingProducer 진입 전 발생 → buffer 가 손실 경로 우회 못함 (Copilot PR #511 피드백)
	// 따라서 buffer 활성 시 throttler 자체를 wiring 하지 않음 — buffer 가 elastic queueing 책임 인수.
	switch {
	case bufferDrainer != nil:
		log.WithField("max_backlog_ignored", schedulerCfg.MaxBacklog).
			Info("scheduler backlog throttle skipped — publisher redis buffer manages elastic queueing")
	case schedulerCfg.MaxBacklog > 0:
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

	// 이슈 #443 — scheduler stage 비활성 시 Start 호출만 skip.
	// 구성요소 (entries / resolver / publisher) 는 그대로 둠 — DB 부하·메모리 미미.
	if stagesCfg.SchedulerEnabled {
		sched.Start(ctx)
		// Resolver 가 wiring 되어 있으면 30s 주기 refresh — DB 변경 자동 반영.
		// resolver 자체 5min TTL cache 가 DB 부하 흡수, 짧은 refresh 가 stall 빈도 줄임.
		sched.StartRefreshLoop(ctx, 30*time.Second)

		log.WithField("entry_count", len(entries)).Info("scheduler started")
	} else {
		log.Info("scheduler disabled by STAGES_SCHEDULER_ENABLED=false")
	}

	// 이슈 #510 — BufferDrainer 시작 (config Enabled + Redis 연결 시에만 wiring 됨).
	// drainer 가 scheduler stage 와 독립 — fetcher 단계가 활성화된 어떤 인스턴스에서도 drain 가능.
	if bufferDrainer != nil {
		bufferDrainer.Start(ctx)
	}

	// ══════════════════════════════════════════════════════════════════════════
	// Processor (Validate)
	// ══════════════════════════════════════════════════════════════════════════

	validateCfg, err := processorcfg.LoadValidate()
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
	validateCap := runtimecfg.CapPerStage(workerCountsCfg.Validate, stageGateCfg.ValidateMaxConcurrentPerStage)
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
	// Processor (Enrich) — sub-issue #446 skeleton (passthrough only)
	// ══════════════════════════════════════════════════════════════════════════

	enrichKafkaCfg := queue.DefaultConfig()
	enrichKafkaCfg.GroupID = queue.GroupEnrichers

	enrichConsumer := queue.NewConsumer(enrichKafkaCfg, queue.TopicValidated)
	defer enrichConsumer.Close()

	enrichProducer := queue.NewProducer(enrichKafkaCfg)
	defer enrichProducer.Close()

	// publisher facade — enrich 는 Forward (validated → enriched) 만 사용.
	enrichPublisher := bus.New(enrichProducer, nil, log)

	enrichCap := runtimecfg.CapPerStage(workerCountsCfg.Enrich, stageGateCfg.EnrichMaxConcurrentPerStage)
	enrichGate := locks.BuildStageGate(locks.StageEnricher, enrichCap, procLock, log)
	if procLock != nil {
		log.WithFields(map[string]interface{}{
			"worker_count": workerCountsCfg.Enrich,
			"capacity":     enrichCap,
			"configured":   stageGateCfg.EnrichMaxConcurrentPerStage,
		}).Info("enrich stage gate enabled (ProcessingLock + Semaphore)")
	} else {
		log.Warn("processing lock unavailable, enrich stage gate falls back to noop")
	}

	// 이슈 #447 — claudegen 기반 enricher extractor wiring. claudegen pool 이 미활성이거나
	// prompt loader 가 없으면 NoopExtractor 로 fallback (worker 는 항상 forward 보장 — extract
	// 실패가 파이프라인을 막지 않음).
	var enrichExtractor enrichcore.Extractor = enrichcore.NewNoopExtractor()
	if claudegenPool != nil && promptLoader != nil {
		ce, ceErr := enrichcore.NewClaudegenExtractor(claudegenPool, promptLoader)
		if ceErr != nil {
			log.WithError(ceErr).Warn("claudegen enrich extractor construction failed, using noop")
		} else {
			enrichExtractor = ce
			log.Info("enrich extractor: claudegen-backed")
		}
	} else {
		log.Info("enrich extractor: noop (claudegen pool or prompt loader unavailable)")
	}

	// 이슈 #448 — claudegen 기반 enricher verifier (cross-verification) wiring. extractor 와
	// 동일 fallback 정책.
	var enrichVerifier enrichcore.Verifier = enrichcore.NewNoopVerifier()
	if claudegenPool != nil && promptLoader != nil {
		cv, cvErr := enrichcore.NewClaudegenVerifier(claudegenPool, promptLoader)
		if cvErr != nil {
			log.WithError(cvErr).Warn("claudegen enrich verifier construction failed, using noop")
		} else {
			enrichVerifier = cv
			log.Info("enrich verifier: claudegen-backed")
		}
	} else {
		log.Info("enrich verifier: noop (claudegen pool or prompt loader unavailable)")
	}

	// 이슈 #449 — claudegen 기반 enricher contextualizer (외부 맥락 수집) wiring.
	// extractor / verifier 와 동일 fallback 정책.
	var enrichContextualizer enrichcore.Contextualizer = enrichcore.NewNoopContextualizer()
	if claudegenPool != nil && promptLoader != nil {
		cc, ccErr := enrichcore.NewClaudegenContextualizer(claudegenPool, promptLoader)
		if ccErr != nil {
			log.WithError(ccErr).Warn("claudegen enrich contextualizer construction failed, using noop")
		} else {
			enrichContextualizer = cc
			log.Info("enrich contextualizer: claudegen-backed")
		}
	} else {
		log.Info("enrich contextualizer: noop (claudegen pool or prompt loader unavailable)")
	}

	// 이슈 #450 — claudegen 기반 enricher scorer (trust_score) + enriched_contents 영속화 wiring.
	var enrichScorer enrichcore.Scorer = enrichcore.NewNoopScorer()
	if claudegenPool != nil && promptLoader != nil {
		cs, csErr := enrichcore.NewClaudegenScorer(claudegenPool, promptLoader)
		if csErr != nil {
			log.WithError(csErr).Warn("claudegen enrich scorer construction failed, using noop")
		} else {
			enrichScorer = cs
			log.Info("enrich scorer: claudegen-backed")
		}
	} else {
		log.Info("enrich scorer: noop (claudegen pool or prompt loader unavailable)")
	}

	// enriched_contents repository — pgxpool 사용. nil 허용 (Worker 가 nil 시 DB write skip).
	enrichedRepo := decorator.WrapEnrichedContentWithTimeout(
		pgstore.NewEnrichedContentRepository(pool, log), dbCfg.QueryTimeout,
	)

	enrichW := enrichWorkerPkg.NewWorker(enrichConsumer, enrichPublisher, contentSvc, enrichExtractor, enrichVerifier, enrichContextualizer, enrichScorer, enrichedRepo, enrichGate, workerCountsCfg.Enrich)

	log.WithFields(map[string]interface{}{
		"worker_count": workerCountsCfg.Enrich,
		"input_topic":  queue.TopicValidated,
		"output_topic": queue.TopicEnriched,
	}).Info("enrich worker constructed (#447 extractor + #448 verifier + #449 contextualizer + #450 scorer/persistence)")

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
	enrichStage, err := enrich.NewStage(enrichW)
	if err != nil {
		log.WithError(err).Fatal("failed to construct enrich stage")
	}

	// 이슈 #443 — stage 별 활성 플래그에 따라 selective Start.
	// 구성은 위에서 항상 진행 (Kafka Reader 는 FetchMessage 호출 전까지 group 미참여 —
	// 비활성 stage 의 phantom consumer 우려 없음). 비활성 시 Start 만 skip.
	stages := []processor.Stage{}
	if stagesCfg.FetcherEnabled {
		stages = append(stages, fetcherStage)
	} else {
		log.Info("fetcher stage disabled by STAGES_FETCHER_ENABLED=false")
	}
	if stagesCfg.ParserEnabled {
		stages = append(stages, parserStg)
	} else {
		log.Info("parser stage disabled by STAGES_PARSER_ENABLED=false")
	}
	if stagesCfg.ValidateEnabled {
		stages = append(stages, validateStage)
	} else {
		log.Info("validate stage disabled by STAGES_VALIDATE_ENABLED=false")
	}
	if stagesCfg.EnrichEnabled {
		stages = append(stages, enrichStage)
	} else {
		log.Info("enrich stage disabled by STAGES_ENRICH_ENABLED=false")
	}

	// chromedp 자동 upgrade 의 자가 회복 안전장치 — interval 마다 reason='auto_upgrade_validation'
	// row 를 goquery 로 reset. ENABLED=false 시 stage 미등록 (단계 3 의 upgrade-only 동작 유지).
	// fetcher stage 비활성 시 downgrader 도 의미 없으므로 함께 skip.
	downgradeCfg, err := fetchercfg.LoadFetcherAutoDowngrade()
	if err != nil {
		log.WithError(err).Fatal("failed to load fetcher auto-downgrade config")
	}
	if stagesCfg.FetcherEnabled && downgradeCfg.Enabled {
		downgrader, err := fetcherRule.NewDowngrader(fetcherRuleRepo, fetcherResolver, downgradeCfg.Interval, log)
		if err != nil {
			log.WithError(err).Fatal("failed to construct fetcher auto-downgrade stage")
		}
		stages = append(stages, downgrader)
		log.WithField("interval", downgradeCfg.Interval.String()).Info("fetcher auto-downgrade enabled")
	} else if !stagesCfg.FetcherEnabled {
		log.Info("fetcher auto-downgrade skipped (fetcher stage disabled)")
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

	// 이슈 #510 — BufferDrainer 정지. scheduler.Stop 이후 호출 — scheduler 가 BufferingProducer 로
	// 보내는 publish 가 끝난 뒤 drainer 가 잔존 buffer 를 마지막으로 비울 수 있도록.
	// 단, drainer 는 자기 tick 주기로 동작 — Stop 은 다음 tick 직전에 cancel + 진행 중 cycle 완료 대기.
	// 잔존 buffer 는 Redis 에 보존되어 다음 부팅 시 자동 회복 (옵션 A — 본 이슈 본문 참조).
	if bufferDrainer != nil {
		bufferDrainer.Stop()
	}

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

// envDurationOrDefault 는 환경변수에서 time.Duration 을 파싱하여 반환합니다 (이슈 #521).
// 미설정 / 파싱 실패 시 def 반환. 운영자가 PRIORITY_RULES_REFRESH_INTERVAL 같은 변수 조정 시 사용.
func envDurationOrDefault(key string, def time.Duration) time.Duration {
	raw := os.Getenv(key)
	if raw == "" {
		return def
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return def
	}
	return d
}

// envIntOrDefault 는 환경변수에서 양의 int 를 파싱하여 반환합니다 (이슈 #521).
// 미설정 / 파싱 실패 / 0 이하 값은 def 반환.
func envIntOrDefault(key string, def int) int {
	raw := os.Getenv(key)
	if raw == "" {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v <= 0 {
		return def
	}
	return v
}

// priorityFromInt16 은 DB 의 crawl_priority (SMALLINT, 1~3) 를 core.Priority 로 변환합니다 (이슈 #521).
// 매핑: 1=high / 2=normal / 3=low. 잘못된 값은 normal 로 보정 — DB CHECK 제약으로 정상은 미도달.
func priorityFromInt16(v int16) core.Priority {
	switch v {
	case 1:
		return core.PriorityHigh
	case 3:
		return core.PriorityLow
	default:
		return core.PriorityNormal
	}
}

// buildEnricherROMCPConfig 는 ENRICHER_DB_RO_* 환경변수에서 read-only DSN 을 구성하고
// MCP postgres config 를 반환합니다 (이슈 #472).
//
// 반환값:
//   - (*agentdb.MCPConfig, nil): 환경변수 설정 정상 + MCP 활성화 대상
//   - (nil, nil): 환경변수 미설정 → MCP 비활성 (기존 동작 유지)
//   - (nil, error): 부분 설정 / 잘못된 값 → caller 가 warn 로깅
//
// 본 함수는 옵션이라 fatal 아님 — MCP 미설정 환경에서 enrich 가 여전히 동작하도록 보장.
func buildEnricherROMCPConfig(log *logger.Logger) (*agentdb.MCPConfig, error) {
	dsn, ok, err := agentdb.DSNFromEnv("ENRICHER_DB_RO_")
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	cfg, err := agentdb.PostgresMCPConfig("issuetracker_ro", dsn)
	if err != nil {
		return nil, err
	}
	log.WithFields(map[string]interface{}{
		"dsn": dsn.String(),
	}).Debug("enricher_ro MCP config constructed")
	return &cfg, nil
}
