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
//  1. worker_id 별 Semaphore.Acquire — worker 자원 격리 guard
//  2. handler.Registry 에서 crawler_name 으로 ChainHandler lookup
//  3. ChainHandler.HandleChromedpOnly 호출 — ChromedpChain 으로 raw fetch + 저장 + RawContentRef publish
//  4. Semaphore.Release (defer)
//
// 이슈 #229 — per-worker Semaphore 모델 + 실효 동시성 정정 (gemini 피드백):
// 글로벌 Semaphore 1 개를 모든 worker 가 공유하던 PR #227 의 모델을, worker_id 별 Semaphore
// 1 개로 분리. **단, KafkaConsumerPool 의 worker goroutine 은 메시지를 순차 처리** 하므로 같은
// worker 가 동시 2 개 이상의 Handle 을 호출할 수 없음 → per-worker Semaphore 의 capacity > 1 은
// 현 모델에서 추가 동시성 이득 없음 (default 1 권장).
//
// 본 핸들러의 Semaphore 는 처리량 throttle 이 아닌 worker 자원 격리 guard 역할 — 다음 sub-issue
// #230 에서 worker_id 별 Chrome RemoteURL 까지 매핑하면 worker:Chrome 1:1 활성화. 처리량 조정은
// WorkerCount + RemoteURLs 수로 수행.
//
// goquery worker pool 의 ChainHandler.republishToChromedpQueue 가 발행한 메시지가 본 핸들러로
// 흐름.
type ChromedpJobHandler struct {
	registry *handler.Registry
	// sems 는 worker_id 별 Semaphore. 길이 N — KafkaConsumerPool 의 chromedp pool worker 수와 동일.
	// Handle 진입 시 ctx 의 worker_id 로 슬롯 선택. ID 가 범위 밖이면 0번 슬롯 fallback (방어).
	sems []Semaphore
	log  *logger.Logger
}

// NewChromedpJobHandler 는 새 ChromedpJobHandler 를 생성합니다 (이슈 #229).
//
// sems 는 worker_id 인덱스의 Semaphore slice — 길이는 chromedp pool 의 worker 수와 동일해야
// 합니다. 호출자(main.go) 가 cfg.WorkerCount 만큼 NewSemaphore 를 만들어 전달합니다.
//
// registry / sems 는 nil/empty 허용 안 함 (이슈 #208 정책).
func NewChromedpJobHandler(registry *handler.Registry, sems []Semaphore, log *logger.Logger) (*ChromedpJobHandler, error) {
	if registry == nil {
		return nil, errors.New("worker: NewChromedpJobHandler requires non-nil handler Registry")
	}
	if len(sems) == 0 {
		return nil, errors.New("worker: NewChromedpJobHandler requires at least one Semaphore")
	}
	for i, s := range sems {
		if s == nil {
			return nil, fmt.Errorf("worker: NewChromedpJobHandler sems[%d] is nil", i)
		}
	}
	return &ChromedpJobHandler{
		registry: registry,
		sems:     sems,
		log:      log,
	}, nil
}

// resolveSemaphore 는 ctx 의 worker_id 로 자기 전용 Semaphore 를 반환합니다.
// worker_id 미설정 또는 범위 밖이면 0번 슬롯 fallback + WARN 로그 (방어 + 가시성).
func (h *ChromedpJobHandler) resolveSemaphore(ctx context.Context) (Semaphore, int) {
	id := core.WorkerIDFromContext(ctx)
	if id < 0 || id >= len(h.sems) {
		if h.log != nil {
			h.log.WithFields(map[string]interface{}{
				"worker_id":   id,
				"slot_count":  len(h.sems),
				"fallback_id": 0,
			}).Warn("chromedp handler received out-of-range worker_id, using slot 0 fallback")
		}
		return h.sems[0], 0
	}
	return h.sems[id], id
}

// Handle 은 per-worker semaphore 보호 하에 chromedp fetch 를 실행합니다.
//
// Registry 에서 crawler_name 으로 등록된 Handler 를 lookup. 등록된 Handler 가 *general.ChainHandler
// 가 아니면 (테스트/noop fallback 등) error — chromedp pool 은 정의상 ChainHandler 만 처리.
//
// Semaphore Acquire 가 ctx 만료로 실패하면 ctx.Err 반환 — KafkaConsumerPool 이 재시도 정책 적용.
func (h *ChromedpJobHandler) Handle(ctx context.Context, job *core.CrawlJob) ([]*core.Content, error) {
	if job == nil {
		return nil, errors.New("chromedp handler received nil job")
	}

	sem, workerID := h.resolveSemaphore(ctx)
	if err := sem.Acquire(ctx); err != nil {
		return nil, fmt.Errorf("chromedp semaphore acquire (worker_id=%d): %w", workerID, err)
	}
	defer func() {
		if relErr := sem.Release(); relErr != nil && h.log != nil {
			h.log.WithFields(map[string]interface{}{
				"worker_id": workerID,
			}).WithError(relErr).Warn("chromedp semaphore release contract violation (non-fatal)")
		}
	}()

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
			"crawler":   job.CrawlerName,
			"url":       job.Target.URL,
			"worker_id": workerID,
			"capacity":  sem.Capacity(),
		}).Debug("chromedp pool handling job")
	}

	return chain.HandleChromedpOnly(ctx, job)
}
