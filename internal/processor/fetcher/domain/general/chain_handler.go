package general

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"

	"issuetracker/internal/processor/fetcher/core"
	"issuetracker/internal/processor/fetcher/rule"
	"issuetracker/internal/storage"
	"issuetracker/internal/storage/service"
	"issuetracker/pkg/logger"
	"issuetracker/pkg/queue"
)

// ChainHandler 는 fetcher worker 의 책임을 담당하는 handler.Handler 어댑터입니다.
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
// host 단위 fetcher 정책:
//   - Resolver 가 nil 이거나 룰 미등록 host: DefaultChain 사용 (gq → browser fallback)
//   - 룰 = 'chromedp': ChromedpChains 사용 (browser only) — goquery 시도 skip
//   - 룰 = 'goquery':  DefaultChain 사용 (gq → browser fallback) — 현재로선 default 와 동일.
//     단계 3 의 자동 downgrade 정책이 도입되면 GoQueryOnly chain 으로 분기 가능.
//
// ChromedpChains 를 worker_id 별 slice 로 분리하여 worker:Chrome 1:1 매핑 활성화:
//   - 각 chain 인스턴스는 자기 전용 RemoteURL 의 ChromedpCrawler 를 보유
//   - HandleChromedpOnly 가 ctx 의 worker_id 로 자기 chain 선택 → worker:Chrome 격리
//   - chain 길이는 chromedp pool 의 WorkerCount 와 일치 (main.go wiring 시 보장)
//
// 본 분리의 핵심 가치:
//   - parser 가 무거워져도 (chromedp 가 큰 HTML 처리, LLM 호출 등) fetcher worker 슬롯 점유 X
//   - parser worker 인스턴스 수를 fetcher 와 독립으로 스케일 가능
//   - raw HTML 이 DB 에 보존되어 worker crash 시에도 복구 가능
//   - 파싱 실패한 raw 가 잔존 → LLM 으로 새 rule 생성 후 재처리 가능
type ChainHandler struct {
	Crawler        SourceCrawler
	DefaultChain   Handler       // gq → browser (현재 동작 — 룰 미등록 host 의 기본)
	ChromedpChains []Handler     // worker_id 별 browser only chain
	Resolver       rule.Resolver // optional. nil 이면 항상 DefaultChain 사용
	RawSvc         service.RawContentService
	Producer       queue.Producer
	Log            *logger.Logger
}

// NewChainHandler 는 새 ChainHandler 를 생성합니다.
// DefaultChain / RawSvc / Producer / Log 모두 비-nil 필수.
// ChromedpChains 가 nil/empty 이면 룰 = 'chromedp' 매칭이어도 DefaultChain fallback (warn 로그).
// Resolver 가 nil 이면 룰 조회 없이 항상 DefaultChain — 기존 동작 100% 보존.
//
// chromedpChains 길이는 chromedp pool 의 WorkerCount 와 일치해야 함 (호출자 책임).
// 단일 Chrome 운영 호환 모드 (sub-issue #229 머지 직후 시점) 에서는 길이 1 의 slice 로 호출.
func NewChainHandler(
	crawler SourceCrawler,
	defaultChain Handler,
	chromedpChains []Handler,
	resolver rule.Resolver,
	rawSvc service.RawContentService,
	producer queue.Producer,
	log *logger.Logger,
) *ChainHandler {
	return &ChainHandler{
		Crawler:        crawler,
		DefaultChain:   defaultChain,
		ChromedpChains: chromedpChains,
		Resolver:       resolver,
		RawSvc:         rawSvc,
		Producer:       producer,
		Log:            log,
	}
}

// hasChromedpChain 은 ChromedpChains 가 wiring 되어 있는지 확인합니다.
// nil 또는 empty slice 모두 미wiring 으로 판단.
func (h *ChainHandler) hasChromedpChain() bool {
	return len(h.ChromedpChains) > 0
}

// resolveChromedpChain 은 ctx 의 worker_id 로 자기 전용 chromedp chain 을 반환합니다.
// worker_id 미설정 / 범위 밖이면 0번 chain fallback (방어 + 가시성 WARN).
//
// defense-in-depth (gemini 피드백): ChromedpChains 가 empty 면 nil 반환 — 호출자
// (HandleChromedpOnly) 가 사전에 hasChromedpChain 검사하지만, 헬퍼 자체에서도
// 슬라이스 길이 확인으로 인덱스 panic 방지. 미래 다른 호출자가 사전 검사 누락해도 안전.
func (h *ChainHandler) resolveChromedpChain(ctx context.Context) Handler {
	if len(h.ChromedpChains) == 0 {
		return nil
	}
	id := core.WorkerIDFromContext(ctx)
	if id < 0 || id >= len(h.ChromedpChains) {
		if h.Log != nil {
			h.Log.WithFields(map[string]interface{}{
				"worker_id":   id,
				"chain_count": len(h.ChromedpChains),
				"fallback_id": 0,
			}).Warn("chain handler received out-of-range worker_id, using chromedp chain 0 fallback")
		}
		return h.ChromedpChains[0]
	}
	return h.ChromedpChains[id]
}

// selectChain 은 host 단위 fetcher 룰을 조회하여 사용할 chain 과 chromedp 매칭 여부를 결정합니다.
//
// 우선순위 (위에서부터):
//  1. Target.Metadata["force_fetcher"] == "chromedp" → chromedp 매칭 (단계 3 republish 경로)
//  2. Resolver 의 host 룰 조회 결과
//  3. fallback → DefaultChain
//
// 반환 (chain, isChromedp):
//   - isChromedp=true: 호출자 (Handle) 가 republish 분기로 진입 (chain 자체는 사용 안 함)
//   - isChromedp=false: chain 직접 호출
//
// ChromedpChains 가 worker_id 별 slice 가 되면서 selectChain 단계에서는 어느 슬롯을
// 쓸지 결정하지 않음 (republish 후 ChromedpJobHandler 가 slot 선택). Handle 의 분기 식별을
// chain 포인터 비교 대신 isChromedp 플래그로 안전하게 처리.
//
// Resolver 가 nil 이거나 host 추출 실패 또는 룰 조회 실패 시 DefaultChain 으로 fallback —
// fetcher 정책이 부분적으로 망가져도 fetch 자체는 계속 진행 (graceful degrade).
func (h *ChainHandler) selectChain(ctx context.Context, job *core.CrawlJob) (Handler, bool) {
	// 단계 3 republish 경로 — Resolver 보다 우선. 자동 chromedp 전환 trigger 가 발행한
	// 새 CrawlJob 이 즉시 chromedp 처리되도록 보장.
	//
	// Provenance 검증 (CodeRabbit 피드백): force_fetcher metadata 는 process-local secret token
	// 과 함께 부착되어야만 honor. 외부 publisher (현재는 없으나 미래 확장 시) 가 token 추측 불가.
	// token 부재 / 불일치 시 warn 후 default 분기 — fail-safe.
	if v, ok := job.Target.Metadata[rule.MetadataKeyForceFetcher]; ok {
		if s, ok := v.(string); ok && s == string(storage.FetcherChromedp) {
			tokenVal, _ := job.Target.Metadata[rule.MetadataKeyForceFetcherToken].(string)
			if !rule.ValidateForceFetcherToken(tokenVal) {
				h.Log.WithFields(map[string]interface{}{
					"url": job.Target.URL,
				}).Warn("force_fetcher metadata present but token invalid/absent — falling back to default chain")
			} else if h.hasChromedpChain() {
				h.Log.WithFields(map[string]interface{}{
					"url":     job.Target.URL,
					"fetcher": s,
				}).Debug("force_fetcher metadata applied (republish path)")
				return nil, true
			} else {
				h.Log.WithFields(map[string]interface{}{
					"url": job.Target.URL,
				}).Warn("force_fetcher=chromedp but no ChromedpChains wired, using default chain")
			}
		}
	}

	if h.Resolver == nil {
		return h.DefaultChain, false
	}
	host := extractHost(job.Target.URL)
	if host == "" {
		return h.DefaultChain, false
	}
	res, err := h.Resolver.Resolve(ctx, host)
	if err != nil {
		h.Log.WithFields(map[string]interface{}{
			"url":  job.Target.URL,
			"host": host,
		}).WithError(err).Warn("fetcher rule resolve failed, falling back to default chain")
		return h.DefaultChain, false
	}
	if !res.Found {
		return h.DefaultChain, false
	}
	if res.Fetcher == storage.FetcherChromedp {
		if !h.hasChromedpChain() {
			h.Log.WithFields(map[string]interface{}{
				"url":  job.Target.URL,
				"host": host,
			}).Warn("rule selected chromedp but no ChromedpChains wired, using default chain")
			return h.DefaultChain, false
		}
		h.Log.WithFields(map[string]interface{}{
			"url":     job.Target.URL,
			"host":    host,
			"fetcher": string(res.Fetcher),
		}).Debug("fetcher rule applied")
		return nil, true
	}
	// FetcherGoQuery — 현재는 DefaultChain 과 동작이 같음. 단계 3 자동 downgrade 도입 시 분기.
	return h.DefaultChain, false
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
//
// chromedp 처리는 별도 worker pool 로 격리. selectChain 결과가 chromedp 매칭이거나
// DefaultChain 의 GoQuery 가 lazy detect 시 sentinel 반환하면 직접 호출 대신 TopicCrawlChromedp
// 로 republish — Chrome 자원 동시 호출량을 chromedp pool 의 semaphore 로 제어.
//
// selectChain 이 (chain, isChromedp) tuple 반환. ChromedpChains slice 모델로
// 변경되어 chain 포인터 비교가 부적합 → isChromedp 플래그로 분기.
func (h *ChainHandler) Handle(ctx context.Context, job *core.CrawlJob) ([]*core.Content, error) {
	if h.DefaultChain == nil || h.Log == nil || h.RawSvc == nil || h.Producer == nil {
		return nil, fmt.Errorf("chain handler is not properly initialized")
	}

	chain, isChromedp := h.selectChain(ctx, job)

	// chromedp 매칭 (force_fetcher 또는 Resolver 룰) → 직접 호출 안 하고 republish.
	if isChromedp {
		if err := h.republishToChromedpQueue(ctx, job); err != nil {
			return nil, fmt.Errorf("republish to chromedp queue for %s: %w", job.Target.URL, err)
		}
		return nil, nil
	}

	raw, err := chain.Handle(ctx, job)
	if err != nil {
		// GoQuery 의 lazy detect sentinel — chromedp pool 로 republish 후 정상 종료.
		if errors.Is(err, ErrLazyContentNeedsBrowser) {
			if pubErr := h.republishToChromedpQueue(ctx, job); pubErr != nil {
				return nil, fmt.Errorf("republish to chromedp queue (lazy detect) for %s: %w", job.Target.URL, pubErr)
			}
			return nil, nil
		}
		return nil, err
	}
	if raw == nil {
		return nil, fmt.Errorf("chain returned nil raw content for %s", job.Target.URL)
	}

	return nil, h.processFetchedRaw(ctx, job, raw, "default")
}

// processFetchedRaw 는 fetch 된 raw 를 raw_contents 에 저장하고 RawContentRef 를 TopicFetched 에
// 발행하는 공통 흐름입니다 (gemini 피드백 — Handle / HandleChromedpOnly 중복 추출).
//
// pool 인자는 로그 차원의 식별자 ("default" / "chromedp") — 운영 가시성용.
func (h *ChainHandler) processFetchedRaw(ctx context.Context, job *core.CrawlJob, raw *core.RawContent, pool string) error {
	rawID, dup, err := h.RawSvc.Store(ctx, raw)
	if err != nil {
		return fmt.Errorf("store raw content for %s: %w", job.Target.URL, err)
	}
	if dup {
		// 동일 URL 의 이전 raw 가 이미 존재 — 새 fetch 가 의미 없음 (parser 가 이전 raw 를 처리 중이거나 처리 완료).
		// 단, dedup 정책상 publisher 가 Ingestion Lock 으로 사전 차단해야 정상 — 여기 도달은 race condition.
		// 정상 흐름이라 가정하고 ref 만 다시 발행 (idempotent — ProcessingLock 이 parser 측 중복 흡수).
		h.Log.WithFields(map[string]interface{}{
			"crawler":     job.CrawlerName,
			"url":         raw.URL,
			"existing_id": rawID,
			"pool":        pool,
		}).Debug("raw content already exists for url, republishing ref")
	}

	if err := h.publishFetchedRef(ctx, job, raw, rawID); err != nil {
		return fmt.Errorf("publish fetched ref for %s: %w", job.Target.URL, err)
	}

	h.Log.WithFields(map[string]interface{}{
		"crawler":     job.CrawlerName,
		"url":         raw.URL,
		"raw_id":      rawID,
		"target_type": string(job.Target.Type),
		"pool":        pool,
	}).Debug("raw content stored, fetched ref published")
	return nil
}

// HandleChromedpOnly 는 ChromedpChains 중 worker_id 슬롯을 호출하여 chromedp 단독 fetch 를 수행합니다.
//
// chromedp pool 의 ChromedpJobHandler 가 같은 ChainHandler 인스턴스를 Registry 에서 lookup 하여
// 본 메소드 호출 — Resolver / force_fetcher / republish 분기 skip 하고 ctx 의 worker_id 로
// ChromedpChains[id] 직접 호출. 그 외 처리 흐름 (raw_contents 저장 + RawContentRef publish) 은
// 일반 Handle 과 동일.
//
// ChromedpChains 미wiring 사이트 (예: yonhap) 에 대해서는 정의상 chromedp pool 이 큐 메시지를
// 받지 않아야 하지만, 안전장치로 empty 일 때 graceful error.
func (h *ChainHandler) HandleChromedpOnly(ctx context.Context, job *core.CrawlJob) ([]*core.Content, error) {
	if h.Log == nil || h.RawSvc == nil || h.Producer == nil {
		return nil, fmt.Errorf("chain handler is not properly initialized")
	}
	if !h.hasChromedpChain() {
		return nil, fmt.Errorf("crawler %s has no chromedp chain wired", job.CrawlerName)
	}

	chain := h.resolveChromedpChain(ctx)
	if chain == nil {
		// defense-in-depth (gemini 피드백): hasChromedpChain 통과 후에도 헬퍼가 nil 반환하면
		// race condition 또는 ChromedpChains 가 nil 원소 포함 같은 invariant 위반 — graceful error.
		return nil, fmt.Errorf("crawler %s resolved nil chromedp chain (invariant violation)", job.CrawlerName)
	}
	raw, err := chain.Handle(ctx, job)
	if err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, fmt.Errorf("chromedp chain returned nil raw content for %s", job.Target.URL)
	}

	return nil, h.processFetchedRaw(ctx, job, raw, "chromedp")
}

// republishToChromedpQueue 는 본 job 을 TopicCrawlChromedp 로 다시 발행합니다.
//
// chromedp 처리 책임을 본 worker (goquery pool) 에서 분리 — 별도 chromedp worker pool 이 receive
// 후 semaphore 로 Chrome 자원 보호. force_fetcher metadata + token 은 보존하여 chromedp pool 의
// ChainHandler 가 분기 일관성 유지.
//
// CrawlJob 페이로드는 그대로 재직렬화 — Marshal 실패는 가능한 fatal 이라 caller 에 전파.
func (h *ChainHandler) republishToChromedpQueue(ctx context.Context, job *core.CrawlJob) error {
	data, err := job.Marshal()
	if err != nil {
		return fmt.Errorf("marshal job %s: %w", job.ID, err)
	}
	msg := queue.Message{
		Topic: queue.TopicCrawlChromedp,
		Key:   []byte(job.ID),
		Value: data,
		Headers: map[string]string{
			"crawler":  job.CrawlerName,
			"job_id":   job.ID,
			"original": "goquery_pool",
		},
	}
	if err := h.Producer.Publish(ctx, msg); err != nil {
		return fmt.Errorf("publish chromedp queue: %w", err)
	}
	h.Log.WithFields(map[string]interface{}{
		"crawler": job.CrawlerName,
		"url":     job.Target.URL,
	}).Info("job republished to chromedp queue")
	return nil
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
