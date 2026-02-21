package main

import (
  "context"
  "os"
  "os/signal"
  "syscall"
  "time"

  "ecoscrapper/internal/crawler/core"
  "ecoscrapper/internal/crawler/handler"
  "ecoscrapper/internal/crawler/worker"
  "ecoscrapper/pkg/logger"
  "ecoscrapper/pkg/queue"
)

func main() {
  logConfig := logger.DefaultConfig()
  logConfig.Level = logger.LevelInfo
  logConfig.Pretty = false
  log := logger.New(logConfig)

  log.Info("starting EcoScrapper crawler")

  ctx, cancel := context.WithCancel(context.Background())
  defer cancel()

  ctx = log.ToContext(ctx)

  kafkaCfg := queue.DefaultConfig()
  kafkaCfg.GroupID = queue.GroupCrawlerWorkers

  log.WithFields(map[string]interface{}{
    "brokers":  kafkaCfg.Brokers,
    "group_id": kafkaCfg.GroupID,
  }).Info("kafka configuration loaded")

  // ── 1. 발행 (Job → crawl.high / normal / low) ─────────────────────────────
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
  highConsumer := queue.NewConsumer(kafkaCfg, queue.TopicCrawlHigh)
  defer highConsumer.Close()

  normalConsumer := queue.NewConsumer(kafkaCfg, queue.TopicCrawlNormal)
  defer normalConsumer.Close()

  lowConsumer := queue.NewConsumer(kafkaCfg, queue.TopicCrawlLow)
  defer lowConsumer.Close()

  // ── 4. 실행 (Worker → 크롤링 → raw.us / raw.kr) ───────────────────────────
  // TODO: 소스별 크롤러 구현 후 registry.Register("cnn", cnn.NewHandler(...)) 방식으로 추가
  registry := handler.NewRegistry(log)

  // ── 5. 조율 (Pool 생성 및 시작) ───────────────────────────────────────────
  // 워커 수는 각 토픽의 파티션 수를 초과하지 않도록 설정
  //   High: 파티션 6 → 워커 3 (긴급, 즉시 처리)
  //   Normal: 파티션 8 → 워커 6 (일반 크롤링, 최대 처리량)
  //   Low: 파티션 4 → 워커 2 (백그라운드 수집)
  managerCfg := worker.ManagerConfig{
    High:   worker.PoolConfig{Consumer: highConsumer, WorkerCount: 3},
    Normal: worker.PoolConfig{Consumer: normalConsumer, WorkerCount: 6},
    Low:    worker.PoolConfig{Consumer: lowConsumer, WorkerCount: 2},
  }

  manager := worker.NewPoolManager(managerCfg, producer, registry, resolver, log)
  manager.Start(ctx)

  log.WithFields(map[string]interface{}{
    "high_workers":   managerCfg.High.WorkerCount,
    "normal_workers": managerCfg.Normal.WorkerCount,
    "low_workers":    managerCfg.Low.WorkerCount,
  }).Info("pool manager started")

  sigChan := make(chan os.Signal, 1)
  signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

  <-sigChan
  log.Warn("shutdown signal received, draining workers...")
  cancel()

  shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
  defer shutdownCancel()

  if err := manager.Stop(shutdownCtx); err != nil {
    log.WithError(err).Error("error during pool manager shutdown")
  }

  log.Info("crawler shutdown completed")
}
