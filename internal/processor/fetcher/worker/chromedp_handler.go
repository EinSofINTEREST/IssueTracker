package worker

import (
	"context"
	"errors"
	"fmt"

	"issuetracker/internal/processor/fetcher/core"
	"issuetracker/internal/processor/fetcher/domain/general"
	"issuetracker/internal/processor/fetcher/handler"
	"issuetracker/pkg/logger"
)

// ChromedpJobHandler 는 TopicCrawlChromedp 의 CrawlJob 을 처리하는 JobHandler 입니다 (이슈 #218).
//
// 책임:
//
//  1. Semaphore.Acquire — Chrome 인스턴스의 동시 navigation 수 제한
//  2. handler.Registry 에서 crawler_name 으로 ChainHandler lookup
//  3. ChainHandler.HandleChromedpOnly 호출 — ChromedpChain 으로 raw fetch + 저장 + RawContentRef publish
//  4. Semaphore.Release (defer)
//
// goquery worker pool 의 ChainHandler.republishToChromedpQueue 가 발행한 메시지가 본 핸들러로
// 흐름. ChromedpChain 이 같은 인스턴스라 chromedp 호출 자체는 동일 — 격리된 worker pool 에서
// semaphore 로 동시성만 제한.
type ChromedpJobHandler struct {
	registry *handler.Registry
	sem      Semaphore
	log      *logger.Logger
}

// NewChromedpJobHandler 는 새 ChromedpJobHandler 를 생성합니다.
//
// registry / sem 은 nil 허용 안 함 (이슈 #208 정책).
func NewChromedpJobHandler(registry *handler.Registry, sem Semaphore, log *logger.Logger) (*ChromedpJobHandler, error) {
	if registry == nil {
		return nil, errors.New("worker: NewChromedpJobHandler requires non-nil handler Registry")
	}
	if sem == nil {
		return nil, errors.New("worker: NewChromedpJobHandler requires non-nil Semaphore")
	}
	return &ChromedpJobHandler{
		registry: registry,
		sem:      sem,
		log:      log,
	}, nil
}

// Handle 은 semaphore 보호 하에 chromedp fetch 를 실행합니다.
//
// Registry 에서 crawler_name 으로 등록된 Handler 를 lookup. 등록된 Handler 가 *general.ChainHandler
// 가 아니면 (테스트/noop fallback 등) error — chromedp pool 은 정의상 ChainHandler 만 처리.
//
// Semaphore Acquire 가 ctx 만료로 실패하면 ctx.Err 반환 — KafkaConsumerPool 이 재시도 정책 적용.
func (h *ChromedpJobHandler) Handle(ctx context.Context, job *core.CrawlJob) ([]*core.Content, error) {
	if err := h.sem.Acquire(ctx); err != nil {
		return nil, fmt.Errorf("chromedp semaphore acquire: %w", err)
	}
	defer h.sem.Release()

	regHandler, ok := h.registry.Lookup(job.CrawlerName)
	if !ok {
		return nil, fmt.Errorf("no chain handler registered for crawler %s", job.CrawlerName)
	}
	chain, ok := regHandler.(*general.ChainHandler)
	if !ok {
		return nil, fmt.Errorf("registered handler for crawler %s is not *ChainHandler", job.CrawlerName)
	}

	if h.log != nil {
		h.log.WithFields(map[string]interface{}{
			"crawler":  job.CrawlerName,
			"url":      job.Target.URL,
			"capacity": h.sem.Capacity(),
		}).Debug("chromedp pool handling job")
	}

	return chain.HandleChromedpOnly(ctx, job)
}
