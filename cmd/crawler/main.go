package main

import (
  "context"
  "os"
  "os/signal"
  "syscall"

  "ecoscrapper/internal/crawler/core"
  "ecoscrapper/pkg/logger"
)

func main() {
  // Logger 초기화
  logConfig := logger.DefaultConfig()
  logConfig.Level = logger.LevelInfo
  logConfig.Pretty = false
  log := logger.New(logConfig)

  log.Info("starting EcoScrapper crawler")

  // Context 설정 (graceful shutdown 지원)
  ctx, cancel := context.WithCancel(context.Background())
  defer cancel()

  // Logger를 context에 추가
  ctx = log.ToContext(ctx)

  // Crawler 설정
  config := core.DefaultConfig()
  config.RequestsPerHour = 100
  config.BurstSize = 10

  log.WithFields(map[string]interface{}{
    "requests_per_hour": config.RequestsPerHour,
    "burst_size":        config.BurstSize,
    "timeout":           config.Timeout,
  }).Info("crawler configuration loaded")

  // Signal handling for graceful shutdown
  sigChan := make(chan os.Signal, 1)
  signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

  go func() {
    <-sigChan
    log.Warn("shutdown signal received")
    cancel()
  }()

  // TODO: Crawler orchestration logic
  log.Info("crawler started, waiting for jobs...")

  // Wait for context cancellation
  <-ctx.Done()

  log.Info("crawler shutdown completed")
}
