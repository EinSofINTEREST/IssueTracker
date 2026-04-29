package general

import (
	"context"
	"encoding/json"
	"fmt"

	"issuetracker/internal/crawler/core"
	"issuetracker/internal/storage/service"
	"issuetracker/pkg/logger"
	"issuetracker/pkg/queue"
)

// ChainHandler 는 fetcher worker 의 책임을 담당하는 handler.Handler 어댑터입니다 (이슈 #134).
//
// 분리 후 책임 (Claim Check 패턴):
//  1. Chain (GoQuery / Browser) 으로 raw HTML fetch
//  2. RawContentService.Store 로 raw_contents 테이블에 저장 → raw_id
//  3. RawContentRef (raw_id + url + source_info) 를 queue.TopicFetched 로 publish
//
// 파싱 / DB 저장 / 다음 단계 chained job 발행은 별도 parser worker (issuetracker-parsers
// consumer group) 의 책임으로 이전됨. 본 핸들러는 nil Content slice 를 반환하여 worker pool 의
// publishNormalized 단계를 스킵하도록 함.
//
// 본 분리의 핵심 가치:
//   - parser 가 무거워져도 (chromedp 가 큰 HTML 처리, LLM 호출 등) fetcher worker 슬롯 점유 X
//   - parser worker 인스턴스 수를 fetcher 와 독립으로 스케일 가능
//   - raw HTML 이 DB 에 보존되어 worker crash 시에도 복구 가능
//   - 파싱 실패한 raw 가 잔존 → LLM 으로 새 rule 생성 (이슈 #149) 후 재처리 가능
type ChainHandler struct {
	Crawler  SourceCrawler
	Chain    Handler
	RawSvc   service.RawContentService
	Producer queue.Producer
	Log      *logger.Logger
}

// NewChainHandler 는 새 ChainHandler 를 생성합니다.
// Chain / RawSvc / Producer / Log 모두 비-nil 필수.
func NewChainHandler(
	crawler SourceCrawler,
	chain Handler,
	rawSvc service.RawContentService,
	producer queue.Producer,
	log *logger.Logger,
) *ChainHandler {
	return &ChainHandler{
		Crawler:  crawler,
		Chain:    chain,
		RawSvc:   rawSvc,
		Producer: producer,
		Log:      log,
	}
}

// Handle 은 chain 통과로 raw HTML 을 fetch 하고 raw_contents 저장 + RawContentRef 발행을 수행합니다.
//
// 항상 nil Content slice 를 반환 — 파싱은 parser worker 가 TopicFetched 를 consume 하여 처리.
func (h *ChainHandler) Handle(ctx context.Context, job *core.CrawlJob) ([]*core.Content, error) {
	if h.Chain == nil || h.Log == nil || h.RawSvc == nil || h.Producer == nil {
		return nil, fmt.Errorf("chain handler is not properly initialized")
	}

	raw, err := h.Chain.Handle(ctx, job)
	if err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, fmt.Errorf("chain returned nil raw content for %s", job.Target.URL)
	}

	// raw_contents 저장 (중복 시 기존 ID 재사용 — RawContentService 책임)
	rawID, dup, err := h.RawSvc.Store(ctx, raw)
	if err != nil {
		return nil, fmt.Errorf("store raw content for %s: %w", job.Target.URL, err)
	}
	if dup {
		// 동일 URL 의 이전 raw 가 이미 존재 — 새 fetch 가 의미 없음 (parser 가 이전 raw 를 처리 중이거나 처리 완료).
		// 단, dedup 정책상 publisher 가 URL cache 로 사전 차단해야 정상 — 여기 도달은 race condition.
		// 정상 흐름이라 가정하고 ref 만 다시 발행 (idempotent — JobLocker 가 parser 측 중복 흡수).
		h.Log.WithFields(map[string]interface{}{
			"crawler":     job.CrawlerName,
			"url":         raw.URL,
			"existing_id": rawID,
		}).Debug("raw content already exists for url, republishing ref")
	}

	if err := h.publishFetchedRef(ctx, job, raw, rawID); err != nil {
		return nil, fmt.Errorf("publish fetched ref for %s: %w", job.Target.URL, err)
	}

	h.Log.WithFields(map[string]interface{}{
		"crawler":     job.CrawlerName,
		"url":         raw.URL,
		"raw_id":      rawID,
		"target_type": string(job.Target.Type),
	}).Debug("raw content stored, fetched ref published")

	return nil, nil
}

// publishFetchedRef 는 RawContentRef 를 TopicFetched 에 발행합니다.
// 헤더에 target_type 을 포함하여 parser worker 가 Article/Category 분기에 사용.
func (h *ChainHandler) publishFetchedRef(ctx context.Context, job *core.CrawlJob, raw *core.RawContent, rawID string) error {
	ref := core.RawContentRef{
		ID:         rawID,
		URL:        raw.URL,
		FetchedAt:  raw.FetchedAt,
		SourceInfo: raw.SourceInfo,
	}
	payload, err := json.Marshal(ref)
	if err != nil {
		return fmt.Errorf("marshal raw content ref: %w", err)
	}

	msg := queue.Message{
		Topic: queue.TopicFetched,
		Key:   []byte(rawID),
		Value: payload,
		Headers: map[string]string{
			"source":      raw.SourceInfo.Name,
			"country":     raw.SourceInfo.Country,
			"crawler":     job.CrawlerName,
			"job_id":      job.ID,
			"target_type": string(job.Target.Type),
			"timeout_ms":  fmt.Sprintf("%d", job.Timeout.Milliseconds()),
		},
	}
	return h.Producer.Publish(ctx, msg)
}
