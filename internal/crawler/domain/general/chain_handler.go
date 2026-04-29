package general

import (
	"context"
	"errors"
	"fmt"

	"issuetracker/internal/crawler/core"
	"issuetracker/internal/crawler/parser"
	"issuetracker/internal/crawler/parser/rule"
	"issuetracker/internal/storage"
	"issuetracker/pkg/logger"
)

const (
	// maxChainedURLs: 단일 카테고리 페이지에서 발행할 수 있는 최대 기사 URL 수.
	// 광고/외부 링크 혼재 시 Kafka 토픽 폭증 방지. parsing_rules.LinkDiscovery.MaxLinksPerPage
	// 이 추가 cap 으로 동작하지만 본 상수는 ItemContainer 모드도 보호.
	maxChainedURLs = 200
)

// ChainHandler 는 fetch chain + DB 기반 파싱 (rule.Parser) + DB 저장을 결합한
// handler.Handler 어댑터입니다 (이슈 #100 / #139 통합).
//
// ChainHandler combines fetch chain + DB-driven parsing + DB persistence.
// 사이트별 NaverParser/CNNParser/... 가 사라진 자리 — 모든 사이트가 단일 rule.Parser 를 공유하며,
// parsing_rules 테이블의 row 가 사이트 동작을 결정합니다.
//
// 동작 분기:
//  1. RSS (fetch_strategy=rss): ConvertRSSPages 로 다건 변환 반환
//  2. 카테고리/목록 (TargetTypeCategory): rule.Parser.ParseLinks → publisher.Publish → nil 반환
//  3. 기사 페이지 (TargetTypeArticle): rule.Parser.ParsePage → ConvertPage → 단건 반환
type ChainHandler struct {
	Crawler   SourceCrawler
	Chain     Handler
	Parser    *rule.Parser                  // nil 허용 — nil 이면 ParsePage / ParseLinks 건너뜀
	Publisher JobPublisher                  // nil 허용 — nil 이면 카테고리 체이닝 건너뜀
	Repo      storage.NewsArticleRepository // nil 허용 — nil 이면 DB 저장 건너뜀
	Log       *logger.Logger
}

// NewChainHandler 는 새 ChainHandler 를 생성합니다. chain 과 log 는 비-nil 필수.
func NewChainHandler(
	crawler SourceCrawler,
	chain Handler,
	parser *rule.Parser,
	publisher JobPublisher,
	repo storage.NewsArticleRepository,
	log *logger.Logger,
) *ChainHandler {
	return &ChainHandler{
		Crawler:   crawler,
		Chain:     chain,
		Parser:    parser,
		Publisher: publisher,
		Repo:      repo,
		Log:       log,
	}
}

// Handle 은 CrawlJob 을 chain 통과시키고 파싱된 Content(s) 반환.
func (h *ChainHandler) Handle(ctx context.Context, job *core.CrawlJob) ([]*core.Content, error) {
	if h.Chain == nil || h.Log == nil {
		return nil, fmt.Errorf("chain handler is not properly initialized: chain or log is nil")
	}

	raw, err := h.Chain.Handle(ctx, job)
	if err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, fmt.Errorf("chain returned nil raw content for %s", job.Target.URL)
	}

	// 1. RSS 결과
	if strategy, ok := raw.Metadata["fetch_strategy"].(string); ok && strategy == "rss" {
		return ConvertRSSPages(raw), nil
	}

	// 2. 카테고리/목록 페이지
	if job.Target.Type == core.TargetTypeCategory {
		if h.Parser == nil || h.Publisher == nil {
			h.Log.WithFields(map[string]interface{}{
				"crawler":    job.CrawlerName,
				"url":        job.Target.URL,
				"has_parser": h.Parser != nil,
				"has_pub":    h.Publisher != nil,
			}).Debug("category job skipped: parser or publisher not configured")
			return nil, nil
		}
		return nil, h.publishArticleJobs(ctx, job, raw)
	}

	// 3. 기사 페이지
	isArticle := job.Target.Type == core.TargetTypeArticle
	canParse := raw.HTML != "" && h.Parser != nil
	if !isArticle || !canParse {
		h.Log.WithFields(map[string]interface{}{
			"is_article": isArticle,
			"has_html":   raw.HTML != "",
			"has_parser": h.Parser != nil,
		}).Debug("skipping parse: conditions not met")
		return nil, nil
	}

	page, err := h.Parser.ParsePage(ctx, raw)
	if err != nil {
		h.Log.WithError(err).Warn("page parse failed, skipping")
		return nil, nil
	}

	if h.Repo != nil {
		if err := h.Repo.Insert(ctx, PageToRecord(page, raw)); err != nil {
			h.Log.WithError(err).Warn("failed to save parsed page to news_articles")
		}
	}

	return []*core.Content{ConvertPage(page, raw)}, nil
}

// publishArticleJobs 는 카테고리/목록 페이지에서 기사 URL 추출 후 CrawlJob 발행.
// rule.Parser.ParseLinks 가 LinkDiscovery (full-page) 또는 ItemContainer 경로 자동 선택.
//
// 에러 처리 정책 (Coderabbit 피드백):
//   - rule 진단 에러 (ErrParseFailure / ErrEmptySelector / ErrNoRule) 는 terminal —
//     warn + nil 반환. 카테고리 페이지가 비거나 rule 이 stale 한 경우 재시도해도
//     같은 결과 → DLQ/재시도 churn 회피. article ParsePage 의 동일 정책 (105-108) 과 정렬.
//   - 그 외 에러 (예: ctx 취소) 만 재시도/DLQ 진입을 위해 전파.
func (h *ChainHandler) publishArticleJobs(ctx context.Context, job *core.CrawlJob, raw *core.RawContent) error {
	items, err := h.Parser.ParseLinks(ctx, raw)
	if err != nil {
		var rerr *rule.Error
		if errors.As(err, &rerr) {
			h.Log.WithFields(map[string]interface{}{
				"crawler":    job.CrawlerName,
				"url":        job.Target.URL,
				"error_code": string(rerr.Code),
			}).WithError(err).Warn("link parse failed (rule stale or no matches), skipping category")
			return nil
		}
		return fmt.Errorf("parse links for %s: %w", job.Target.URL, err)
	}
	if len(items) == 0 {
		h.Log.WithFields(map[string]interface{}{
			"crawler": job.CrawlerName,
			"url":     job.Target.URL,
		}).Debug("no article links found in category page")
		return nil
	}

	urls := uniqueURLs(items, maxChainedURLs)

	if err := h.Publisher.Publish(ctx, job.CrawlerName, urls, core.TargetTypeArticle, job.Timeout); err != nil {
		return fmt.Errorf("publish chained jobs for %s: %w", job.Target.URL, err)
	}

	h.Log.WithFields(map[string]interface{}{
		"crawler":     job.CrawlerName,
		"url":         job.Target.URL,
		"url_count":   len(urls),
		"target_type": string(core.TargetTypeArticle),
	}).Info("chained article jobs published from category page")
	return nil
}

// uniqueURLs 는 LinkItem 슬라이스에서 빈 URL 을 제거하고 limit 까지의 unique URL 반환.
func uniqueURLs(items []parser.LinkItem, limit int) []string {
	seen := make(map[string]struct{}, len(items))
	urls := make([]string, 0, min(len(items), limit))

	for _, item := range items {
		if item.URL == "" {
			continue
		}
		if _, dup := seen[item.URL]; dup {
			continue
		}
		seen[item.URL] = struct{}{}
		urls = append(urls, item.URL)
		if len(urls) >= limit {
			break
		}
	}
	return urls
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
