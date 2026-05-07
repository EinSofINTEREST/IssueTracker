// Package metrics provides a Prometheus registry and HTTP handler for /metrics export.
//
// metrics 패키지는 Prometheus 기반 운영 metric 수집·노출의 진입점입니다.
// 모든 모듈은 본 패키지의 NewRegistry() 가 반환한 *prometheus.Registry 를 공유하여
// 자신의 metric (counter / histogram / gauge) 을 등록합니다.
//
// 기본 collectors (Go runtime, process) 는 NewRegistry 시점에 자동 등록되며,
// 모듈 custom metric 은 후속 PR 에서 단계적으로 추가됩니다.
package metrics

import (
	"context"
	"errors"
	"fmt"
	"net"
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
// addr 가 빈 문자열이면 endpoint 비활성화 — (noop stop, nil) 반환 후 종료. 운영 환경별 metric
// 노출 토글에 사용.
//
// **fail-fast 정책**: bind/listen 실패는 호출 시점에 동기 검출되어
// error 로 반환됩니다 — 포트 충돌 등으로 metric 이 silent 누락되지 않도록 caller 가 Fatal 처리해야
// 합니다. listen 성공 후 발생하는 Serve 에러만 goroutine 안에서 로깅됩니다.
//
// ctx cancel 시 graceful shutdown (shutdownTimeout 내 in-flight scrape 마무리).
// 반환된 stop 함수는 명시적 종료가 필요할 때 호출 — 일반적으로 ctx cancel 만으로 충분합니다.
func Serve(ctx context.Context, addr string, registry *prometheus.Registry, log *logger.Logger) (stop func() error, err error) {
	if addr == "" {
		log.Info("metrics endpoint disabled (METRICS_ADDR empty)")
		return func() error { return nil }, nil
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

	// 호출 시점에 동기 bind — 실패하면 caller 가 Fatal 가능.
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("listen metrics endpoint %q: %w", addr, err)
	}

	go func() {
		log.WithField("addr", addr).Info("metrics endpoint started")
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.WithError(err).Error("metrics endpoint exited unexpectedly")
		}
	}()

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.WithError(err).Warn("metrics endpoint shutdown error")
			return
		}
		log.Info("metrics endpoint stopped")
	}()

	return func() error {
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}, nil
}
