package search

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"time"

	"issuetracker/internal/processor/fetcher/core"
	"issuetracker/internal/storage"
	"issuetracker/pkg/logger"
)

// JobPublisher 는 SearchHandler 가 fanout chained article job 을 발행할 때 사용하는 인터페이스입니다.
//
// 실제 구현은 internal/publisher.Publisher — host 별 batch 발행을 위해 host group 마다 호출.
// 이슈 #386 — Publisher.PublishChained 메소드명 일치 (구 Publish 에서 rename).
type JobPublisher interface {
	PublishChained(
		ctx context.Context,
		crawlerName string,
		urls []string,
		targetType core.TargetType,
		timeout time.Duration,
	) error
}

// SearchHandler 는 scheduler 가 발행한 search_results 카테고리 job 을 처리하는 Handler 입니다.
//
// 동작:
//  1. job.Target.Metadata 에서 engine / per_query_max_results / date_range_days 추출
//  2. SearchKeywordRepository.ListEnabled 로 enabled keyword 전체 조회
//  3. 각 keyword 별 CSEClient.Search 호출 → URL 누적 (keyword-level 실패는 skip + warn)
//  4. URL 들을 host 단위로 group → 각 host 별 publisher.Publish(crawlerName=host, TargetTypeArticle)
//     fanout — 다운스트림 fetcher 가 host-specific handler 로 라우팅
//  5. 성공 keyword 의 last_searched_at 갱신
//
// nil content 반환 — 실제 article 의 raw_content 는 chained article job 이 fetch 후 발행.
type SearchHandler struct {
	client      *CSEClient
	keywordRepo storage.SearchKeywordRepository
	publisher   JobPublisher
	log         *logger.Logger

	// articleTimeout 은 fanout 된 chained article job 의 fetch timeout.
	articleTimeout time.Duration
}

// SearchHandlerOptions 는 SearchHandler 생성 옵션입니다.
type SearchHandlerOptions struct {
	Client         *CSEClient
	KeywordRepo    storage.SearchKeywordRepository
	Publisher      JobPublisher
	ArticleTimeout time.Duration // 0 이하면 30s
}

// NewSearchHandler 는 SearchHandler 를 생성합니다. 모든 의존성 비-nil 필수.
func NewSearchHandler(opts SearchHandlerOptions, log *logger.Logger) (*SearchHandler, error) {
	if opts.Client == nil {
		return nil, errors.New("search: SearchHandler requires CSEClient")
	}
	if opts.KeywordRepo == nil {
		return nil, errors.New("search: SearchHandler requires KeywordRepo")
	}
	if opts.Publisher == nil {
		return nil, errors.New("search: SearchHandler requires Publisher")
	}
	if log == nil {
		return nil, errors.New("search: SearchHandler requires logger")
	}
	timeout := opts.ArticleTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &SearchHandler{
		client:         opts.Client,
		keywordRepo:    opts.KeywordRepo,
		publisher:      opts.Publisher,
		log:            log,
		articleTimeout: timeout,
	}, nil
}

// entryMetadata 는 scheduler_entries.metadata JSONB 에서 추출하는 search-specific 옵션입니다.
//
// 모든 필드는 optional — 기본값은 search_results target 에 일반적으로 적합한 보수 값.
type entryMetadata struct {
	Engine             string `json:"engine"`
	PerQueryMaxResults int    `json:"per_query_max_results"`
	DateRangeDays      int    `json:"date_range_days"`
}

// Handle 은 search_results target 에 대해 keyword fanout 을 수행합니다.
func (h *SearchHandler) Handle(ctx context.Context, job *core.CrawlJob) ([]*core.Content, error) {
	if job == nil {
		return nil, errors.New("search: nil job")
	}

	meta := h.parseMetadata(job)
	if meta.Engine != "" && meta.Engine != "google_cse" {
		// 본 handler 는 google_cse 만 처리. 다른 engine 은 future-proof skip.
		h.log.WithField("engine", meta.Engine).Warn("search handler skip: unsupported engine")
		return nil, nil
	}

	keywords, err := h.keywordRepo.ListEnabled(ctx, "", "")
	if err != nil {
		return nil, fmt.Errorf("list enabled keywords: %w", err)
	}
	if len(keywords) == 0 {
		h.log.Info("search handler: no enabled keywords — skip")
		return nil, nil
	}

	opts := SearchOptions{
		MaxResults:    meta.PerQueryMaxResults,
		DateRangeDays: meta.DateRangeDays,
	}

	// keyword-level dedup — 같은 article 이 여러 keyword 에서 hit 되면 1번만 fanout.
	seen := make(map[string]struct{}, 256)
	var allURLs []string
	successKeywordIDs := make([]int64, 0, len(keywords))

	for _, kw := range keywords {
		// ctx cancel 시 즉시 cycle 종료 — select+break 는 select 만 빠져나오므로 명시 return.
		if err := ctx.Err(); err != nil {
			h.log.WithError(err).Warn("search handler ctx done — interrupting keyword loop")
			h.markSearched(ctx, successKeywordIDs)
			return nil, err
		}

		kwOpts := opts
		kwOpts.Language = kw.Language
		kwOpts.Region = kw.Region

		urls, searchErr := h.client.Search(ctx, kw.Keyword, kwOpts)
		if searchErr != nil {
			// ctx cancel/deadline 으로 인한 실패는 wrap 여부에 무관하게 ctx.Err() 가 유일 진실
			// — 다른 에러 분류 분기보다 우선 검사 (gemini Medium 반영).
			if ctxErr := ctx.Err(); ctxErr != nil {
				h.log.WithFields(map[string]interface{}{
					"keyword": kw.Keyword,
				}).WithError(searchErr).Warn("cse search interrupted by ctx — aborting cycle")
				h.markSearched(ctx, successKeywordIDs)
				return nil, searchErr
			}
			// non-retryable (auth / quota) → 전체 cycle 중단 (다음 cycle 까지 기다림).
			var cseErr *CSEError
			if errors.As(searchErr, &cseErr) && !cseErr.Retryable {
				h.log.WithFields(map[string]interface{}{
					"keyword":     kw.Keyword,
					"status_code": cseErr.StatusCode,
				}).WithError(searchErr).Error("cse non-retryable error — aborting cycle")
				return nil, nil
			}
			// keyword-level retryable 실패 → skip + warn, 다른 keyword 계속.
			h.log.WithFields(map[string]interface{}{
				"keyword": kw.Keyword,
			}).WithError(searchErr).Warn("cse search failed — skipping keyword")
			continue
		}

		for _, u := range urls {
			if _, dup := seen[u]; dup {
				continue
			}
			seen[u] = struct{}{}
			allURLs = append(allURLs, u)
		}
		successKeywordIDs = append(successKeywordIDs, kw.ID)
	}

	if len(allURLs) == 0 {
		h.log.WithField("keyword_count", len(keywords)).Info("search handler: no urls collected")
		// last_searched_at 은 갱신 — 이번 cycle 에서 호출은 했으므로.
		h.markSearched(ctx, successKeywordIDs)
		return nil, nil
	}

	// host 단위 group 후 host-specific handler 로 fanout.
	byHost := groupByHost(allURLs)

	var publishedTotal int
	for host, urls := range byHost {
		if err := h.publisher.PublishChained(ctx, host, urls, core.TargetTypeArticle, h.articleTimeout); err != nil {
			h.log.WithFields(map[string]interface{}{
				"host":      host,
				"url_count": len(urls),
			}).WithError(err).Error("publish chained article jobs failed")
			continue
		}
		publishedTotal += len(urls)
	}

	h.log.WithFields(map[string]interface{}{
		"keyword_total":   len(keywords),
		"keyword_success": len(successKeywordIDs),
		"urls_unique":     len(allURLs),
		"hosts":           len(byHost),
		"published":       publishedTotal,
	}).Info("search handler cycle completed")

	h.markSearched(ctx, successKeywordIDs)
	return nil, nil
}

// parseMetadata 는 job.Target.Metadata JSONB raw 또는 map 에서 entryMetadata 를 추출합니다.
//
// scheduler 의 EntryConverter 가 현재 metadata 를 ScheduleEntry 로 carry-over 하지 않으므로
// 본 handler 는 best-effort — Metadata map 에 raw bytes 가 있으면 decode, 없으면 빈 struct 반환.
// 빈 struct 는 client 의 default (1 page / no date filter) 로 동작.
func (h *SearchHandler) parseMetadata(job *core.CrawlJob) entryMetadata {
	var meta entryMetadata
	if job.Target.Metadata == nil {
		return meta
	}
	if raw, ok := job.Target.Metadata["raw_metadata"]; ok {
		if b, ok := raw.([]byte); ok && len(b) > 0 {
			if err := json.Unmarshal(b, &meta); err != nil {
				h.log.WithError(err).Warn("search handler: failed to decode raw_metadata")
			}
		}
	}
	if v, ok := job.Target.Metadata["engine"].(string); ok && meta.Engine == "" {
		meta.Engine = v
	}
	if v, ok := job.Target.Metadata["per_query_max_results"].(int); ok && meta.PerQueryMaxResults == 0 {
		meta.PerQueryMaxResults = v
	}
	if v, ok := job.Target.Metadata["date_range_days"].(int); ok && meta.DateRangeDays == 0 {
		meta.DateRangeDays = v
	}
	return meta
}

// markSearched 는 성공한 keyword 들의 last_searched_at 을 일괄 갱신합니다.
// 실패는 warn 로그만 남기고 cycle 자체는 정상 종료 — last_searched_at 은 부가 정보.
func (h *SearchHandler) markSearched(ctx context.Context, ids []int64) {
	if len(ids) == 0 {
		return
	}
	now := time.Now().UTC()
	for _, id := range ids {
		if err := h.keywordRepo.MarkSearched(ctx, id, now); err != nil {
			h.log.WithFields(map[string]interface{}{
				"keyword_id": id,
			}).WithError(err).Warn("mark searched failed — skipping")
		}
	}
}

// groupByHost 는 URL 리스트를 hostname 별로 group 합니다. 파싱 실패 URL 은 "_unknown_" group.
//
// 반환되는 map 은 deterministic 순서가 아님 — host 단위 batch publish 효율을 위한 group 화에만 사용.
func groupByHost(urls []string) map[string][]string {
	byHost := make(map[string][]string, 16)
	for _, u := range urls {
		host := hostOf(u)
		byHost[host] = append(byHost[host], u)
	}
	return byHost
}

// hostOf 는 URL 의 hostname 을 반환합니다. 파싱 실패 시 "_unknown_".
func hostOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Hostname() == "" {
		return "_unknown_"
	}
	return u.Hostname()
}
