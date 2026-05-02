package general

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"issuetracker/internal/processor/fetcher/core"
	"issuetracker/internal/processor/fetcher/rule"
	"issuetracker/internal/storage"
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
// host 단위 fetcher 정책 (이슈 #175 단계 1):
//   - Resolver 가 nil 이거나 룰 미등록 host: DefaultChain 사용 (gq → browser fallback)
//   - 룰 = 'chromedp': ChromedpChain 사용 (browser only) — goquery 시도 skip
//   - 룰 = 'goquery':  DefaultChain 사용 (gq → browser fallback) — 현재로선 default 와 동일.
//     단계 3 의 자동 downgrade 정책이 도입되면 GoQueryOnly chain 으로 분기 가능.
//
// 본 분리의 핵심 가치:
//   - parser 가 무거워져도 (chromedp 가 큰 HTML 처리, LLM 호출 등) fetcher worker 슬롯 점유 X
//   - parser worker 인스턴스 수를 fetcher 와 독립으로 스케일 가능
//   - raw HTML 이 DB 에 보존되어 worker crash 시에도 복구 가능
//   - 파싱 실패한 raw 가 잔존 → LLM 으로 새 rule 생성 (이슈 #149) 후 재처리 가능
type ChainHandler struct {
	Crawler       SourceCrawler
	DefaultChain  Handler       // gq → browser (현재 동작 — 룰 미등록 host 의 기본)
	ChromedpChain Handler       // browser only (룰 = chromedp 인 host 전용)
	Resolver      rule.Resolver // optional. nil 이면 항상 DefaultChain 사용
	RawSvc        service.RawContentService
	Producer      queue.Producer
	Log           *logger.Logger
}

// NewChainHandler 는 새 ChainHandler 를 생성합니다.
// DefaultChain / RawSvc / Producer / Log 모두 비-nil 필수.
// ChromedpChain 이 nil 이면 룰 = 'chromedp' 매칭이어도 DefaultChain fallback (warn 로그).
// Resolver 가 nil 이면 룰 조회 없이 항상 DefaultChain — 기존 동작 100% 보존.
func NewChainHandler(
	crawler SourceCrawler,
	defaultChain Handler,
	chromedpChain Handler,
	resolver rule.Resolver,
	rawSvc service.RawContentService,
	producer queue.Producer,
	log *logger.Logger,
) *ChainHandler {
	return &ChainHandler{
		Crawler:       crawler,
		DefaultChain:  defaultChain,
		ChromedpChain: chromedpChain,
		Resolver:      resolver,
		RawSvc:        rawSvc,
		Producer:      producer,
		Log:           log,
	}
}

// MetadataKeyForceFetcher 는 단계 3 의 자동 republish job 이 ChainHandler 에 chromedp 강제
// 사용을 지시할 때 Target.Metadata 에 사용하는 키입니다 (이슈 #175 단계 3, sub-issue #221).
//
// 값 "chromedp" 가 설정되면 Resolver / fetcher_rules 조회를 건너뛰고 즉시 ChromedpChain 사용.
// 외부 source 가 임의로 force 지정하지 못하도록 publisher 측에서 internal-only 로 제한해야 함
// (이슈 #221 본문 안전망).
const MetadataKeyForceFetcher = "force_fetcher"

// selectChain 은 host 단위 fetcher 룰을 조회하여 사용할 chain 을 결정합니다.
//
// 우선순위 (위에서부터):
//  1. Target.Metadata["force_fetcher"] == "chromedp" → ChromedpChain (단계 3 republish 경로)
//  2. Resolver 의 host 룰 조회 결과
//  3. fallback → DefaultChain
//
// Resolver 가 nil 이거나 host 추출 실패 또는 룰 조회 실패 시 DefaultChain 으로 fallback —
// fetcher 정책이 부분적으로 망가져도 fetch 자체는 계속 진행 (graceful degrade).
//
// chain 선택 외에 룰 결과를 로그에 남겨 운영 가시성 확보.
func (h *ChainHandler) selectChain(ctx context.Context, job *core.CrawlJob) Handler {
	// 단계 3 republish 경로 — Resolver 보다 우선. 자동 chromedp 전환 trigger 가 발행한
	// 새 CrawlJob 이 즉시 chromedp 처리되도록 보장.
	if v, ok := job.Target.Metadata[MetadataKeyForceFetcher]; ok {
		if s, ok := v.(string); ok && s == string(storage.FetcherChromedp) {
			if h.ChromedpChain != nil {
				h.Log.WithFields(map[string]interface{}{
					"url":     job.Target.URL,
					"fetcher": s,
				}).Debug("force_fetcher metadata applied (republish path)")
				return h.ChromedpChain
			}
			h.Log.WithFields(map[string]interface{}{
				"url": job.Target.URL,
			}).Warn("force_fetcher=chromedp but no ChromedpChain wired, using default chain")
		}
	}

	if h.Resolver == nil {
		return h.DefaultChain
	}
	host := extractHost(job.Target.URL)
	if host == "" {
		return h.DefaultChain
	}
	res, err := h.Resolver.Resolve(ctx, host)
	if err != nil {
		h.Log.WithFields(map[string]interface{}{
			"url":  job.Target.URL,
			"host": host,
		}).WithError(err).Warn("fetcher rule resolve failed, falling back to default chain")
		return h.DefaultChain
	}
	if !res.Found {
		return h.DefaultChain
	}
	if res.Fetcher == storage.FetcherChromedp {
		if h.ChromedpChain == nil {
			h.Log.WithFields(map[string]interface{}{
				"url":  job.Target.URL,
				"host": host,
			}).Warn("rule selected chromedp but no ChromedpChain wired, using default chain")
			return h.DefaultChain
		}
		h.Log.WithFields(map[string]interface{}{
			"url":     job.Target.URL,
			"host":    host,
			"fetcher": string(res.Fetcher),
		}).Debug("fetcher rule applied")
		return h.ChromedpChain
	}
	// FetcherGoQuery — 현재는 DefaultChain 과 동작이 같음. 단계 3 자동 downgrade 도입 시 분기.
	return h.DefaultChain
}

// extractHost 는 URL 에서 host 만 뽑습니다 (port 제거 포함).
// 파싱 실패 시 빈 문자열 — 호출자가 default chain 으로 fallback 분기.
func extractHost(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Hostname()
}

// Handle 은 chain 통과로 raw HTML 을 fetch 하고 raw_contents 저장 + RawContentRef 발행을 수행합니다.
//
// 항상 nil Content slice 를 반환 — 파싱은 parser worker 가 TopicFetched 를 consume 하여 처리.
func (h *ChainHandler) Handle(ctx context.Context, job *core.CrawlJob) ([]*core.Content, error) {
	if h.DefaultChain == nil || h.Log == nil || h.RawSvc == nil || h.Producer == nil {
		return nil, fmt.Errorf("chain handler is not properly initialized")
	}

	chain := h.selectChain(ctx, job)
	raw, err := chain.Handle(ctx, job)
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
		// 단, dedup 정책상 publisher 가 Ingestion Lock 으로 사전 차단해야 정상 — 여기 도달은 race condition.
		// 정상 흐름이라 가정하고 ref 만 다시 발행 (idempotent — ProcessingLock 이 parser 측 중복 흡수).
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
