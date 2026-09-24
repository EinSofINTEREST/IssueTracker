// cmd/api 는 수집·처리된 콘텐츠를 외부에 제공하는 REST API 서버입니다 (이슈 #21).
//
// 본 바이너리는 읽기 전용입니다 — 파이프라인 stage 나 Kafka consumer 를 기동하지 않고
// PostgreSQL 만 의존합니다.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"issuetracker/internal/api"
	"issuetracker/internal/storage/decorator"
	pgstore "issuetracker/internal/storage/postgres"
	appcfg "issuetracker/pkg/config/app"
	storagecfg "issuetracker/pkg/config/storage"
	"issuetracker/pkg/logger"
	"issuetracker/pkg/metrics"
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

	log.Info("starting IssueTracker api server")

	shutdownCfg, err := appcfg.LoadShutdown()
	if err != nil {
		log.WithError(err).Fatal("failed to load shutdown config")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx = log.ToContext(ctx)

	// ── Metrics endpoint ──────────────────────────────────────────────────────
	metricsCfg, err := appcfg.LoadMetrics()
	if err != nil {
		log.WithError(err).Fatal("failed to load metrics config")
	}
	if _, err := metrics.Serve(ctx, metricsCfg.Addr, metrics.NewRegistry(), log); err != nil {
		log.WithError(err).Fatal("failed to start metrics endpoint")
	}

	// ── DB 연결 ───────────────────────────────────────────────────────────────
	dbCfg, err := storagecfg.Load()
	if err != nil {
		log.WithError(err).Fatal("failed to load db config")
	}

	pool, err := pgstore.NewPool(ctx, dbCfg, log)
	if err != nil {
		log.WithError(err).Fatal("failed to connect to db")
	}
	defer pool.Close()

	// ── API 서버 ──────────────────────────────────────────────────────────────
	apiCfg, err := appcfg.LoadAPI()
	if err != nil {
		log.WithError(err).Fatal("failed to load api config")
	}

	// query-level timeout 적용 (이슈 #427) — 다른 바이너리와 동일하게 decorator 경유.
	contentRepo := decorator.WrapContentWithTimeout(pgstore.NewContentRepository(pool, log), dbCfg.QueryTimeout)

	router := api.NewRouter(api.Deps{DB: pool, Contents: contentRepo, Log: log})

	// bind 실패는 동기 error — 포트 충돌로 API 가 조용히 뜨지 않는 상황을 만들지 않는다.
	// shutdownCfg.Timeout 이 in-flight 요청 드레인 윈도우로 실제 적용된다.
	stopAPI, err := api.Serve(ctx, apiCfg.Addr, router, shutdownCfg.Timeout, log)
	if err != nil {
		log.WithError(err).Fatal("failed to start api server")
	}

	// ── 종료 시그널 대기 ──────────────────────────────────────────────────────
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	<-sigChan
	log = log.WithField("shutting_down", true)
	log.Warn("shutdown signal received, draining in-flight requests...")

	// cancel 보다 먼저 stopAPI 를 부른다 — cancel 이 먼저면 ctx goroutine 의 shutdown 과
	// 경합해 어느 쪽이 in-flight 를 기다렸는지 불분명해진다.
	if err := stopAPI(); err != nil {
		log.WithError(err).Error("error during api server shutdown")
	}
	cancel()

	log.WithField("shutdown_timeout", shutdownCfg.Timeout.String()).Info("api server shutdown completed")
}
