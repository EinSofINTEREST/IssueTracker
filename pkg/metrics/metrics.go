// Package metrics provides a Prometheus registry and HTTP handler for /metrics export.
//
// metrics 패키지는 Prometheus 기반 운영 metric 수집·노출의 진입점입니다 (이슈 #165).
// 모든 모듈은 본 패키지의 NewRegistry() 가 반환한 *prometheus.Registry 를 공유하여
// 자신의 metric (counter / histogram / gauge) 을 등록합니다.
//
// 기본 collectors (Go runtime, process) 는 NewRegistry 시점에 자동 등록되며,
// 모듈 custom metric 은 후속 PR 에서 단계적으로 추가됩니다.
package metrics

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"issuetracker/pkg/logger"
)

// shutdownTimeout: ctx cancel 후 in-flight /metrics scrape 응답을 마무리할 grace 윈도우.
const shutdownTimeout = 5 * time.Second

// NewRegistry 는 Go runtime + process 기본 collector 가 등록된 prometheus.Registry 를 반환합니다.
//
// NewRegistry returns a prometheus.Registry pre-registered with Go runtime and process collectors.
// 모듈 custom metric 은 호출자가 반환된 registry 에 직접 MustRegister 로 등록합니다.
func NewRegistry() *prometheus.Registry {
	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector())
	reg.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	return reg
}

// Serve 는 별도 goroutine 으로 /metrics endpoint 를 노출합니다 (non-blocking).
//
// addr 가 빈 문자열이면 endpoint 비활성화 — nil 반환 후 종료. 운영 환경별 metric 노출 토글에 사용.
// ctx cancel 시 graceful shutdown (shutdownTimeout 내 in-flight scrape 마무리).
//
// 반환된 함수는 명시적 stop 이 필요할 때 호출 — 일반적으로 ctx cancel 만으로 충분합니다.
func Serve(ctx context.Context, addr string, registry *prometheus.Registry, log *logger.Logger) (stop func() error) {
	if addr == "" {
		log.Info("metrics endpoint disabled (METRICS_ADDR empty)")
		return func() error { return nil }
	}

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{
		Registry: registry,
	}))

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.WithField("addr", addr).Info("metrics endpoint started")
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.WithError(err).Error("metrics endpoint exited unexpectedly")
		}
	}()

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.WithError(err).Warn("metrics endpoint shutdown error")
			return
		}
		log.Info("metrics endpoint stopped")
	}()

	return srv.Close
}
