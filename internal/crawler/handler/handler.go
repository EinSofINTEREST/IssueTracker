// Package handler provides crawler handler registry for mapping CrawlJob
// crawler names to their concrete handler implementations.
//
// handler 패키지는 CrawlJob의 crawler_name을 실제 Handler 구현체로 매핑하는
// 레지스트리를 제공합니다.
//
// 새로운 크롤러를 추가할 때는 Handler 인터페이스를 구현한 후
// Registry.Register(name, handler)로 등록합니다.
package handler

import (
  "context"

  "issuetracker/internal/crawler/core"
  "issuetracker/pkg/logger"
)

// Handler는 단일 CrawlJob을 처리하는 인터페이스입니다.
//
// Handler processes a single CrawlJob and returns fetched raw content.
// Implementations must be safe for concurrent use by multiple goroutines.
type Handler interface {
  Handle(ctx context.Context, job *core.CrawlJob) (*core.RawContent, error)
}

// Registry는 crawler name → Handler 매핑을 관리합니다.
// 등록되지 않은 crawler name은 noop fallback으로 처리됩니다.
//
// Registry maps crawler names to Handler implementations.
// Unregistered crawler names fall back to a noop handler.
type Registry struct {
  handlers map[string]Handler
  fallback Handler
  log      *logger.Logger
}

// NewRegistry는 새로운 Registry를 생성합니다.
// fallback으로 noopHandler가 사용됩니다.
//
// NewRegistry creates a Registry with a noop fallback for unregistered crawlers.
func NewRegistry(log *logger.Logger) *Registry {
  return &Registry{
    handlers: make(map[string]Handler),
    fallback: &noopHandler{log: log},
    log:      log,
  }
}

// Register는 crawler name에 Handler를 등록합니다.
//
// Register associates a Handler with the given crawler name.
func (r *Registry) Register(name string, h Handler) {
  r.handlers[name] = h
}

// Handle은 job.CrawlerName에 등록된 Handler를 찾아 실행합니다.
// 등록된 Handler가 없으면 noop fallback을 실행합니다.
//
// Handle dispatches the job to the registered handler for job.CrawlerName.
// Falls back to noop if no handler is registered.
func (r *Registry) Handle(ctx context.Context, job *core.CrawlJob) (*core.RawContent, error) {
  h, ok := r.handlers[job.CrawlerName]
  if !ok {
    r.log.WithField("crawler", job.CrawlerName).Warn("no handler registered, using noop")
    return r.fallback.Handle(ctx, job)
  }

  return h.Handle(ctx, job)
}
