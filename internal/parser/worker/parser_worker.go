// Package worker 는 fetcher 와 분리된 parser worker 를 제공합니다 (이슈 #134).
//
// Package worker provides a parser worker decoupled from fetcher workers.
//
// 흐름 (Claim Check 패턴):
//  1. queue.TopicFetched 에서 RawContentRef consume
//  2. RawContentService.GetByID 로 raw HTML 로드
//  3. target_type 분기:
//     - Category (TargetTypeCategory): rule.Parser.ParseLinks → publisher.Publish (chained jobs)
//     - Article (TargetTypeArticle): rule.Parser.ParsePage → ConvertPage → content store + publish normalized
//  4. 정상 처리 후 RawContentService.Delete (raw_contents 정리)
//
// 실패 정책:
//   - rule.Error (parse_failure / empty_selector / no_rule): raw 잔존 + Kafka commit (재시도 X)
//     → LLM 으로 새 rule 생성 (이슈 #149) 후 cleanup cron 이전에 재처리 가능
//   - 기타 transient 에러: commit 안 함 → Kafka 재배달 → 재시도
package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"issuetracker/internal/crawler/core"
	"issuetracker/internal/crawler/domain/general"
	"issuetracker/internal/crawler/parser"
	"issuetracker/internal/crawler/parser/rule"
	"issuetracker/internal/storage"
	"issuetracker/internal/storage/service"
	"issuetracker/pkg/logger"
	"issuetracker/pkg/queue"
)

const (
	// maxChainedURLs: 카테고리 페이지에서 발행할 chained article CrawlJob 의 상한.
	// LinkDiscovery 의 MaxLinksPerPage 외 2중 안전장치.
	maxChainedURLs = 200

	// defaultJobTimeout: 헤더에 timeout_ms 가 없을 때 chained job 이 사용할 기본 timeout.
	defaultJobTimeout = 30 * time.Second
)

// ParserWorker 는 TopicFetched consumer group 의 worker pool 입니다.
type ParserWorker struct {
	consumer    *queue.KafkaConsumer
	producer    queue.Producer
	rawSvc      service.RawContentService
	contentSvc  service.ContentService
	publisher   general.JobPublisher
	parser      *rule.Parser
	workerCount int
	log         *logger.Logger

	wg sync.WaitGroup
}

// NewParserWorker 는 ParserWorker 를 생성합니다.
//
//   - publisher 는 nil 허용 — nil 이면 카테고리 chained jobs 발행 건너뜀 (이런 모드는 보통 운영 금지)
//
// 이슈 #161 (도메인 중립화) 이후 news_articles 도메인 특화 보존은 제거됐습니다 — 모든 article
// 결과는 contentSvc.Store 로 contents 단일 테이블에 저장됩니다.
func NewParserWorker(
	consumer *queue.KafkaConsumer,
	producer queue.Producer,
	rawSvc service.RawContentService,
	contentSvc service.ContentService,
	publisher general.JobPublisher,
	parser *rule.Parser,
	workerCount int,
	log *logger.Logger,
) *ParserWorker {
	if workerCount <= 0 {
		workerCount = 1
	}
	return &ParserWorker{
		consumer:    consumer,
		producer:    producer,
		rawSvc:      rawSvc,
		contentSvc:  contentSvc,
		publisher:   publisher,
		parser:      parser,
		workerCount: workerCount,
		log:         log,
	}
}

// Start 는 worker goroutines 를 기동합니다 (non-blocking).
// 호출자는 ctx cancel + Stop 으로 graceful shutdown 수행.
func (w *ParserWorker) Start(ctx context.Context) {
	w.log.WithFields(map[string]interface{}{
		"worker_count": w.workerCount,
		"input_topic":  queue.TopicFetched,
		"output_topic": queue.TopicNormalized,
	}).Info("parser worker started")

	for i := 0; i < w.workerCount; i++ {
		w.wg.Add(1)
		go w.runWorker(ctx, i)
	}
}

// Stop 은 모든 worker goroutine 의 종료를 대기합니다.
// 호출 전 ctx 가 cancel 되어야 함 (외부 책임).
func (w *ParserWorker) Stop(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		w.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		w.log.Info("parser worker stopped")
	case <-ctx.Done():
		w.log.Warn("parser worker stop timeout")
	}
	return w.consumer.Close()
}

func (w *ParserWorker) runWorker(ctx context.Context, idx int) {
	defer w.wg.Done()
	wlog := w.log.WithField("parser_worker_id", idx)

	for {
		if ctx.Err() != nil {
			return
		}

		msg, err := w.consumer.FetchMessage(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			wlog.WithError(err).Warn("fetch message failed")
			continue
		}

		if err := w.processMessage(ctx, msg); err != nil {
			// processMessage 가 commit 안 한 경우 — 재시도 위해 commit skip (Kafka 가 redeliver).
			wlog.WithError(err).WithField("offset", msg.Offset).Warn("process message failed, will be redelivered")
			continue
		}

		if commitErr := w.consumer.CommitMessages(ctx, msg); commitErr != nil {
			if ctx.Err() == nil {
				wlog.WithError(commitErr).Warn("commit failed after success")
			}
		}
	}
}

// processMessage 는 단일 메시지 처리 흐름. 성공 시 nil, 재시도 필요 시 error 반환.
//
// Commit 정책:
//   - 정상 처리 → 호출자가 commit
//   - rule.Error (parse 실패) → commit (raw 잔존, 재시도 X)
//   - payload 손상 → commit (DLQ 발행 후, 재시도 무의미)
//   - 기타 transient → commit 안 함 (재시도)
func (w *ParserWorker) processMessage(ctx context.Context, msg *queue.Message) error {
	var ref core.RawContentRef
	if err := json.Unmarshal(msg.Value, &ref); err != nil {
		w.log.WithError(err).Error("malformed RawContentRef payload, dropping")
		// commit 을 호출자가 하도록 nil 반환 — payload 손상은 재시도 의미 없음
		return nil
	}

	mlog := w.log.WithFields(map[string]interface{}{
		"raw_id": ref.ID,
		"url":    ref.URL,
	})

	raw, err := w.rawSvc.GetByID(ctx, ref.ID)
	if err != nil {
		// raw 가 이미 삭제 (다른 worker 가 먼저 처리했거나 cleanup 발생) — 정상 종료
		if isNotFound(err) {
			mlog.Debug("raw content not found, skipping (already processed or cleaned up)")
			return nil
		}
		// transient — 재시도
		return fmt.Errorf("get raw by id: %w", err)
	}

	crawlerName := msg.Headers["crawler"]
	if crawlerName == "" {
		crawlerName = raw.SourceInfo.Name
	}
	targetType := core.TargetType(msg.Headers["target_type"])
	jobTimeout := parseTimeoutHeader(msg.Headers["timeout_ms"])

	// 카테고리 페이지 — ParseLinks 후 chained article jobs 발행
	if targetType == core.TargetTypeCategory {
		return w.processCategoryPage(ctx, raw, ref.ID, crawlerName, jobTimeout, mlog)
	}

	// article 페이지 — ParsePage → ConvertPage → publish normalized
	return w.processArticlePage(ctx, raw, ref.ID, crawlerName, mlog)
}

func (w *ParserWorker) processCategoryPage(ctx context.Context, raw *core.RawContent, rawID, crawlerName string, jobTimeout time.Duration, mlog *logger.Logger) error {
	if w.parser == nil || w.publisher == nil {
		mlog.Debug("parser or publisher not configured, skipping category job")
		w.deleteRaw(ctx, rawID, mlog)
		return nil
	}

	items, err := w.parser.ParseLinks(ctx, raw)
	if err != nil {
		return w.handleRuleError(ctx, rawID, "parse_links", err, mlog)
	}
	if len(items) == 0 {
		mlog.Debug("no article links found in category page")
		w.deleteRaw(ctx, rawID, mlog)
		return nil
	}

	urls := uniqueURLs(items, maxChainedURLs)
	if err := w.publisher.Publish(ctx, crawlerName, urls, core.TargetTypeArticle, jobTimeout); err != nil {
		return fmt.Errorf("publish chained article jobs: %w", err)
	}

	mlog.WithFields(map[string]interface{}{
		"crawler":   crawlerName,
		"url_count": len(urls),
	}).Info("chained article jobs published from category page")

	// 카테고리 페이지는 contents/news_articles 에 저장하지 않음 — raw 즉시 정리
	w.deleteRaw(ctx, rawID, mlog)
	return nil
}

func (w *ParserWorker) processArticlePage(ctx context.Context, raw *core.RawContent, rawID, crawlerName string, mlog *logger.Logger) error {
	if w.parser == nil {
		mlog.Debug("parser not configured, skipping article")
		w.deleteRaw(ctx, rawID, mlog)
		return nil
	}

	page, err := w.parser.ParsePage(ctx, raw)
	if err != nil {
		return w.handleRuleError(ctx, rawID, "parse_page", err, mlog)
	}

	content := general.ConvertPage(page, raw)
	if err := w.publishContents(ctx, []*core.Content{content}, crawlerName); err != nil {
		return fmt.Errorf("publish article content: %w", err)
	}

	w.deleteRaw(ctx, rawID, mlog)
	return nil
}

// handleRuleError 는 rule.Error (parse 실패) 와 그 외 에러를 구분합니다.
//
//   - rule.Error → raw 잔존 + commit (warn 로그) — LLM 재처리 윈도우
//   - 기타 → 호출자에게 error 전파 → commit 안 함 → 재시도
func (w *ParserWorker) handleRuleError(ctx context.Context, rawID, stage string, err error, mlog *logger.Logger) error {
	_ = ctx // ctx 는 향후 LLM 비동기 enqueue 시 활용 (이슈 #149)
	var rerr *rule.Error
	if errors.As(err, &rerr) {
		mlog.WithFields(map[string]interface{}{
			"stage":      stage,
			"error_code": string(rerr.Code),
		}).WithError(err).Warn("rule-based parse failed, raw retained for LLM retry")
		// raw 는 의도적으로 잔존 — cleanup cron 이 TTL (default 1h) 후 정리
		// 또는 이슈 #149 LLM 자동 rule 생성 후 reprocess 가능
		return nil
	}
	return fmt.Errorf("%s: %w", stage, err)
}

// publishContents 는 *core.Content 슬라이스를 contents 저장 + ContentRef 발행 (TopicNormalized).
func (w *ParserWorker) publishContents(ctx context.Context, contents []*core.Content, crawlerName string) error {
	for _, c := range contents {
		storedID, _, err := w.contentSvc.Store(ctx, c)
		if err != nil {
			return fmt.Errorf("store content: %w", err)
		}

		ref := core.ContentRef{
			ID:      storedID,
			URL:     c.URL,
			Country: c.Country,
			SourceInfo: core.SourceInfo{
				Country:  c.Country,
				Type:     c.SourceType,
				Name:     c.SourceID,
				Language: c.Language,
			},
		}
		refData, err := json.Marshal(ref)
		if err != nil {
			return fmt.Errorf("marshal content ref: %w", err)
		}

		pm := core.ProcessingMessage{
			ID:        storedID,
			Timestamp: time.Now(),
			Country:   c.Country,
			Stage:     "normalized",
			Data:      refData,
			Metadata: map[string]interface{}{
				"crawler": crawlerName,
			},
		}
		pmBytes, err := json.Marshal(pm)
		if err != nil {
			return fmt.Errorf("marshal processing message: %w", err)
		}

		partitionKey := c.CanonicalURL
		if partitionKey == "" {
			partitionKey = c.URL
		}

		msg := queue.Message{
			Topic: queue.TopicNormalized,
			Key:   []byte(partitionKey),
			Value: pmBytes,
			Headers: map[string]string{
				"source":  c.SourceID,
				"country": c.Country,
				"crawler": crawlerName,
			},
		}
		if err := w.producer.Publish(ctx, msg); err != nil {
			return fmt.Errorf("publish normalized: %w", err)
		}
	}
	return nil
}

// deleteRaw 는 처리 완료된 raw_contents row 를 즉시 정리합니다.
// 실패는 non-fatal — cleanup cron 이 안전망으로 동작.
func (w *ParserWorker) deleteRaw(ctx context.Context, rawID string, mlog *logger.Logger) {
	if err := w.rawSvc.Delete(ctx, rawID); err != nil {
		mlog.WithError(err).Warn("raw delete failed (non-fatal — cleanup cron will catch)")
	}
}

// uniqueURLs 는 LinkItem 슬라이스에서 빈 URL 제거 + limit 까지의 unique URL 반환.
func uniqueURLs(items []parser.LinkItem, limit int) []string {
	seen := make(map[string]struct{}, len(items))
	urls := make([]string, 0)
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

// parseTimeoutHeader 는 timeout_ms 헤더를 time.Duration 으로 파싱합니다.
// 실패 시 defaultJobTimeout 반환.
func parseTimeoutHeader(s string) time.Duration {
	if s == "" {
		return defaultJobTimeout
	}
	ms, err := strconv.ParseInt(s, 10, 64)
	if err != nil || ms <= 0 {
		return defaultJobTimeout
	}
	return time.Duration(ms) * time.Millisecond
}

// isNotFound 는 storage 의 NotFound 에러 여부를 판별합니다.
func isNotFound(err error) bool {
	return errors.Is(err, storage.ErrNotFound)
}
