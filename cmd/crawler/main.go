package main

import (
  "context"
  "os"
  "os/signal"
  "syscall"
  "time"

  "ecoscrapper/internal/crawler/core"
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

  producer := queue.NewProducer(kafkaCfg)
  defer producer.Close()

  // crawl.normal 토픽부터 연결 (high/low는 소스별 크롤러 구현 후 추가)
  consumer := queue.NewConsumer(kafkaCfg, queue.TopicCrawlNormal)
  defer consumer.Close()

  const workerCount = 5

  pool := worker.NewKafkaConsumerPool(
    consumer,
    producer,
    &noopJobHandler{log: log},
    workerCount,
    queue.TopicRawUS, // TODO: 국가별 동적 라우팅으로 교체
  )

  pool.Start(ctx)

  log.WithFields(map[string]interface{}{
    "topic":        queue.TopicCrawlNormal,
    "worker_count": workerCount,
    "raw_topic":    queue.TopicRawUS,
  }).Info("kafka consumer pool started")

  sigChan := make(chan os.Signal, 1)
  signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

  <-sigChan
  log.Warn("shutdown signal received, draining workers...")
  cancel()

  // worker들이 진행 중인 작업을 완료할 시간을 줌
  shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
  defer shutdownCancel()

  if err := pool.Stop(shutdownCtx); err != nil {
    log.WithError(err).Error("error during pool shutdown")
  }

  log.Info("crawler shutdown completed")
}

// noopJobHandler는 실제 소스별 크롤러 구현 전 임시로 사용되는 핸들러입니다.
// TODO: 소스별 크롤러(CNN, Naver 등) 구현 후 registry 패턴으로 교체
type noopJobHandler struct {
  log *logger.Logger
}

func (h *noopJobHandler) Handle(ctx context.Context, job *core.CrawlJob) (*core.RawContent, error) {
  h.log.WithFields(map[string]interface{}{
    "job_id":  job.ID,
    "crawler": job.CrawlerName,
    "url":     job.Target.URL,
  }).Info("job received (no crawler registered yet)")

  return nil, nil
}
