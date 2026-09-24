package api

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"issuetracker/pkg/logger"
)

const (
	// readHeaderTimeout: slowloris 방어 — 헤더 수신 상한.
	readHeaderTimeout = 10 * time.Second
	// defaultShutdownTimeout: shutdownTimeout 이 0 이하일 때 쓰는 grace 윈도우.
	//
	// 0 을 그대로 쓰면 context.WithTimeout 이 즉시 만료되어 in-flight 요청이 전부 끊깁니다 —
	// 설정 누락이 조용히 "graceful 아님" 으로 바뀌지 않도록 기본값으로 대체합니다.
	defaultShutdownTimeout = 10 * time.Second
)

// Serve 는 별도 goroutine 으로 API 서버를 기동합니다 (non-blocking).
//
// addr 가 빈 문자열이면 서버 비활성화 — (noop stop, nil) 반환.
//
// **fail-fast 정책**: bind/listen 실패는 호출 시점에 동기 검출되어 error 로 반환됩니다 —
// 포트 충돌로 API 가 조용히 뜨지 않는 상황을 만들지 않기 위함이며, pkg/metrics.Serve 와
// 같은 정책입니다. listen 성공 후의 Serve 에러만 goroutine 안에서 로깅됩니다.
//
// ctx cancel 시 graceful shutdown — shutdownTimeout 안에서 in-flight 요청을 마무리합니다.
// shutdownTimeout 이 0 이하이면 defaultShutdownTimeout 을 씁니다.
func Serve(
	ctx context.Context,
	addr string,
	handler http.Handler,
	shutdownTimeout time.Duration,
	log *logger.Logger,
) (stop func() error, err error) {
	if addr == "" {
		log.Info("api server disabled (API_ADDR empty)")
		return func() error { return nil }, nil
	}

	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: readHeaderTimeout,
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("listen api endpoint %q: %w", addr, err)
	}

	go func() {
		log.WithField("addr", addr).Info("api server started")
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.WithError(err).Error("api server exited unexpectedly")
		}
	}()

	go func() {
		<-ctx.Done()
		if err := shutdown(ctx, srv, shutdownTimeout); err != nil {
			log.WithError(err).Warn("api server shutdown error")
			return
		}
		log.Info("api server stopped")
	}()

	return func() error { return shutdown(ctx, srv, shutdownTimeout) }, nil
}

// shutdown 은 caller 의 ctx 취소와 무관한 grace 윈도우로 graceful shutdown 을 수행합니다.
//
// WithoutCancel 이 없으면 ctx cancel 경로에서 윈도우가 즉시 만료되어 in-flight 요청이 끊깁니다.
func shutdown(ctx context.Context, srv *http.Server, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = defaultShutdownTimeout
	}

	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()

	return srv.Shutdown(shutdownCtx)
}
