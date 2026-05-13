// Package worker 는 fetcher 와 분리된 parser worker 를 제공합니다.
//
// Package worker provides a parser worker decoupled from fetcher workers.
//
// 흐름 (Claim Check 패턴):
//  1. queue.TopicFetched 에서 RawContentRef consume
//  2. RawContentService.GetByID 로 raw HTML 로드
//  3. target_type 분기:
//     - Category (TargetTypeCategory): rule.Parser.ParseLinks → bus.PublishChained (chained jobs)
//     - Article (TargetTypeArticle): rule.Parser.ParsePage → ConvertPage → content store + publish normalized
//  4. 정상 처리 후 RawContentService.Delete (raw_contents 정리)
//
// 실패 정책:
//   - rule.Error (parse_failure / empty_selector / no_rule): raw 잔존 + Kafka commit (재시도 X)
//     → LLM 으로 새 rule 생성 후 cleanup cron 이전에 재처리 가능
//   - 기타 transient 에러: commit 안 함 → Kafka 재배달 → 재시도
package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"time"
	"unicode/utf8"

	"issuetracker/internal/bus"
	"issuetracker/internal/locks"
	"issuetracker/internal/processor/fetcher/core"
	"issuetracker/internal/processor/fetcher/domain/general"
	fetcherRule "issuetracker/internal/processor/fetcher/rule"
	"issuetracker/internal/processor/parser"
	"issuetracker/internal/processor/parser/rule"
	"issuetracker/internal/processor/parser/rule/llmgen"
	"issuetracker/internal/storage"
	"issuetracker/internal/storage/service"
	"issuetracker/internal/workerpool"
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
//
// 이슈 #392 — Kafka I/O (consume + 3 종 publish + chained job publish) 모두 publisher
// facade 로 위임. Kafka 구현체 (*queue.KafkaConsumer, queue.Producer) 에 직접 의존하지 않음.
// queue 패키지 자체는 토픽 상수 (TopicFetched / TopicNormalized) 와 데이터 타입
// (queue.Message) 사용을 위해 그대로 import — I/O 구현체 직접 호출만 publisher 위임.
type ParserWorker struct {
	consumer   bus.Consumer
	pub        *bus.Publisher
	rawSvc     service.RawContentService
	contentSvc service.ContentService
	parser     *rule.Parser
	resolver   *rule.Resolver              // sample 누적 시 매칭된 rule lookup
	sampleSvc  storage.SampleURLRepository // nil 허용 — nil 이면 sample 누적 skip (단계 4-1)
	gate       locks.StageGate             // nil 이면 NoopStageGate 사용 — 이슈 #355 (StageGate 합성: ProcessingLock + per-stage Semaphore)
	llmGen     *llmgen.Generator           // nil 허용 — nil 이면 ErrNoRule 시 raw 잔존만 (LLM auto-rule 비활성)

	// failureCounter: host 단위 fetcher 실패 카운터.
	// nil 시 noopFailureCounter 로 fallback — 카운팅 자체 비활성.
	// rule.ErrParseFailure / rule.ErrEmptySelector 또는 빈본문 발생 시 Record 호출.
	failureCounter storage.FailureCounter

	// rawIDTracker: 같은 실패 시점에 host 별 raw_id 추적.
	// nil 시 noopRawIDTracker — republish 비활성 (단계 3 trigger 가 빈 raw_id 받음).
	rawIDTracker storage.RawIDTracker

	// upgrader: 임계값 도달 시 chromedp 자동 전환 + 실패 raw republish 트리거.
	// nil 허용 — nil 이면 thresholdReached 신호 발신만 (자동 전환 비활성).
	upgrader *fetcherRule.Upgrader

	emptyBodyTitleMin   int
	emptyBodyContentMin int

	// guard: PipelineGuard — Category cycle 종료 시 Release 호출용.
	// nil 허용 — nil 이면 release skip (TTL fallback 으로 자동 회수).
	guard PipelineGuard

	// blacklist: page-parse 블랙리스트 Matcher.
	// nil 허용 — nil 이면 차단 비활성 (모든 카테고리 링크가 그대로 발행).
	// processCategoryPage 의 bus.PublishChained 직전에 Filter 호출.
	blacklist *rule.BlacklistMatcher

	// staleCounter: stale rule 재학습 트리거 카운터.
	// nil 허용 — nil 이면 stale 재학습 비활성 (chromedp 자동 전환만 동작).
	// ErrParseFailure / ErrEmptySelector 발생 시 Record 호출, threshold 도달 시
	// llmGen.EnqueueStale 트리거.
	staleCounter storage.StaleCounter
	// staleThreshold: window 내 stale failure 임계값.
	// 임계 도달 첫 회 (count == staleThreshold) 만 enqueue — count > threshold 인 후속 호출은
	// 카운트만 누적하고 enqueue 회피 (window 내 동일 host 의 version churn / 비용 spike 방지).
	staleThreshold int

	workerCount int
	log         *logger.Logger

	// pool 은 workerpool harness — Start 에서 lazy 생성. lifecycle (poll / dispatch / shutdown /
	// commit-with-drain) 을 위임 (이슈 #406 — 메타 #403 Sub 3).
	pool *workerpool.ConsumerPool
}

// PipelineGuard 는 Category cycle 종료 시 marker 를 release 하기 위한 최소 인터페이스입니다.
//
// parser_worker 는 release 만 필요하므로 locks.PipelineGuard 의 전체 surface 가 아닌 Release 만
// 노출 — interface segregation 원칙. locks.PipelineGuard 는 구조적 타이핑으로 본 인터페이스 만족.
type PipelineGuard interface {
	Release(ctx context.Context, url string) error
}

// NewParserWorker 는 ParserWorker 를 생성합니다.
//
//   - pub 는 운영 환경에서 **필수 wiring** — nil 이면 Forward / PublishChained 호출이
//     `publisher: Forward called on nil *Publisher` 등 error 를 반환하고, 호출 사이트가
//     ProcessMessage 실패로 분류 → commit skip → Kafka 가 동일 메시지를 무한 재배달 가능.
//     nil 은 publish 경로에 도달하지 않는 단위 테스트 (예: stage gate 격리 검증) 에만 허용.
//     이슈 #392 — 구 publisher / producer 두 필드 통합 + Copilot 피드백 반영.
//   - gate 는 nil 허용 — nil 이면 NoopStageGate 로 fallback (단일 인스턴스 환경에서 dedup + cap 비활성)
//   - llmGen 은 nil 허용 — nil 이면 ErrNoRule 시 raw 잔존만 (LLM auto-rule 비활성)
//   - resolver / sampleSvc 는 nil 허용 — nil 이면 sample 누적 skip
//   - failureCounter 는 nil 허용 — nil 이면 NoopFailureCounter 로 fallback
//   - rawIDTracker 는 nil 허용 — nil 이면 NoopRawIDTracker 로 fallback
//
// 도메인 중립화 이후 news_articles 도메인 특화 보존은 제거됐습니다 — 모든 article
// 결과는 contentSvc.Store 로 contents 단일 테이블에 저장됩니다.
//
// StageGate (ProcessingLock + Semaphore 합성, 이슈 #353/#354) 로 fetcher / parser / validator
// 가 동일 패턴으로 stage 별 동시성 cap + URL 중복 처리 차단을 단일 API 로 적용 — parser 단계는
// raw.URL 단위로 acquire, Kafka rebalance 시 같은 raw 가 두 worker 에 도달해도 1회만 파싱.
func NewParserWorker(
	consumer bus.Consumer,
	pub *bus.Publisher,
	rawSvc service.RawContentService,
	contentSvc service.ContentService,
	parser *rule.Parser,
	resolver *rule.Resolver,
	sampleSvc storage.SampleURLRepository,
	gate locks.StageGate,
	llmGen *llmgen.Generator,
	failureCounter storage.FailureCounter,
	rawIDTracker storage.RawIDTracker,
	upgrader *fetcherRule.Upgrader,
	emptyBodyTitleMin int,
	emptyBodyContentMin int,
	workerCount int,
	log *logger.Logger,
) *ParserWorker {
	if workerCount <= 0 {
		workerCount = 1
	}
	if gate == nil {
		gate = locks.NewNoopStageGate()
	}
	if failureCounter == nil {
		failureCounter = storage.NewNoopFailureCounter()
	}
	if rawIDTracker == nil {
		rawIDTracker = storage.NewNoopRawIDTracker()
	}
	return &ParserWorker{
		consumer:            consumer,
		pub:                 pub,
		rawSvc:              rawSvc,
		contentSvc:          contentSvc,
		parser:              parser,
		resolver:            resolver,
		sampleSvc:           sampleSvc,
		gate:                gate,
		llmGen:              llmGen,
		failureCounter:      failureCounter,
		rawIDTracker:        rawIDTracker,
		upgrader:            upgrader,
		emptyBodyTitleMin:   emptyBodyTitleMin,
		emptyBodyContentMin: emptyBodyContentMin,
		workerCount:         workerCount,
		log:                 log,
	}
}

// SetPipelineGuard 는 Category cycle 완료 시 marker release 용 PipelineGuard 를 주입합니다.
// nil 주입 시 release 비활성 (TTL fallback). Start 호출 전 wiring 단계에서 1회 설정.
func (w *ParserWorker) SetPipelineGuard(g PipelineGuard) {
	w.guard = g
}

// SetBlacklist 는 page-parse 블랙리스트 Matcher 를 주입합니다.
//
// nil 주입 시 차단 비활성 (모든 카테고리 링크가 그대로 article job 으로 발행).
// Start 호출 전 wiring 단계에서 1회 설정.
func (w *ParserWorker) SetBlacklist(b *rule.BlacklistMatcher) {
	w.blacklist = b
}

// SetStaleCounter 는 stale rule 재학습 트리거 카운터 + 임계값을 주입합니다.
//
// nil counter 주입 시 stale 재학습 비활성 (기존 chromedp 자동 전환만 동작).
// threshold <= 0 이면 threshold-crossing gate 비활성 — counter.Record 의 thresholdReached 만으로 트리거
// (window 내 매 후속 호출마다 enqueue 가능, 호환 fallback).
// Start 호출 전 wiring 단계에서 1회 설정.
func (w *ParserWorker) SetStaleCounter(c storage.StaleCounter, threshold int) {
	w.staleCounter = c
	w.staleThreshold = threshold
}

// Start 는 workerpool harness 를 기동합니다 (이슈 #406 — 메타 #403 Sub 3).
//
// harness 가 N 개 worker goroutine + poll goroutine 을 관리하며, 각 메시지마다 본 worker 의
// Handle 메소드를 호출합니다. ctx 에는 worker_pool 필드 logger 가 주입되어 Handle 내부에서
// FromContext 로 접근 가능.
func (w *ParserWorker) Start(ctx context.Context) {
	w.log.WithFields(map[string]interface{}{
		"worker_count": w.workerCount,
		"input_topic":  queue.TopicFetched,
		"output_topic": queue.TopicNormalized,
	}).Info("parser worker started")

	const name = "parser"
	plainLog := logger.FromContext(ctx)
	ctx = plainLog.WithField("worker_pool", name).ToContext(ctx)

	w.pool = workerpool.New(workerpool.Config{
		Consumer:     w.consumer,
		Handler:      w,
		WorkerCount:  w.workerCount,
		DrainTimeout: workerpool.DefaultDrainTimeout,
		Log:          plainLog,
		Name:         name,
	})
	w.pool.Start(ctx)
}

// Stop 은 workerpool harness 의 정상 종료를 수행합니다.
// 호출 전 ctx 가 cancel 되어야 함 (외부 책임 — graceful shutdown).
func (w *ParserWorker) Stop(ctx context.Context) error {
	if w.pool == nil {
		return nil
	}
	err := w.pool.Stop(ctx)
	if err == nil {
		w.log.Info("parser worker stopped")
	} else {
		w.log.WithError(err).Warn("parser worker stop returned error")
	}
	return err
}

// Handle 은 workerpool.Handler 구현 — 각 메시지마다 호출됩니다.
//
// 흐름:
//   - ProcessMessage 성공 → commit
//   - ErrStageGateNotAcquired → commit skip (Debug — 다른 worker 가 처리 중인 정상 dedup)
//   - 기타 transient 에러 → commit skip (Warn — Kafka 가 redeliver)
//
// commit 실패는 ctx cancel 인 경우 강등하지 않으면 셧다운 시점에 noise. Warn 으로 가시성 유지.
func (w *ParserWorker) Handle(ctx context.Context, msg *queue.Message) {
	log := logger.FromContext(ctx)

	if err := w.ProcessMessage(ctx, msg); err != nil {
		// StageGate not-acquired sentinel — commit 없이 다음 메시지로. 정상 dedup 경로라
		// Warn 대신 Debug (이슈 #355 PR #358 리뷰 반영).
		if errors.Is(err, ErrStageGateNotAcquired) {
			log.WithField("offset", msg.Offset).Debug("stage gate not acquired by this worker, uncommitted")
			return
		}
		// ProcessMessage 가 commit 하지 않은 경우 — 재시도 위해 commit skip (Kafka 가 redeliver).
		log.WithError(err).WithField("offset", msg.Offset).Warn("process message failed, will be redelivered")
		return
	}

	if commitErr := w.pool.Commit(ctx, msg); commitErr != nil {
		if ctx.Err() == nil {
			log.WithError(commitErr).Warn("commit failed after success")
		}
	}
}

// ErrStageGateNotAcquired 는 parser stage 의 ProcessingLock 이 다른 worker 에 점유 중이라
// 본 worker 가 스킵해야 함을 나타내는 sentinel. Handle 메소드가 본 에러를 감지하면 Warn 대신
// Debug 로 처리하여 정상 dedup 경로를 시끄럽게 만들지 않습니다 (PR #358 리뷰 반영).
//
// commit 정책: 본 sentinel 반환 시 호출자는 commit 하지 않음 — 같은 partition 의 다음 msg
// 처리로 진행하고, 본 offset 의 commit 은 실제 처리 담당 worker 가 수행 (또는 재배달 stream
// 에서 다시 결정).
//
// exported 이유: 외부 테스트 / 모니터링 코드에서 errors.Is 매칭으로 dedup-skip 을 일반 실패와
// 구분할 수 있도록.
var ErrStageGateNotAcquired = errors.New("parser stage gate not acquired by this worker")

// ProcessMessage 는 단일 메시지 처리 흐름. 성공 시 nil, 재시도 필요 시 error 반환.
//
// Commit 정책:
//   - 정상 처리 → 호출자가 commit
//   - rule.Error (parse 실패) → commit (raw 잔존, 재시도 X)
//   - payload 손상 → commit (DLQ 발행 후, 재시도 무의미)
//   - 기타 transient → commit 안 함 (재시도)
//
// 일반적으로 runWorker 가 내부에서 호출하지만, 외부 테스트가 분기별 행동을 black-box 로
// 검증할 수 있도록 exported. consumer 가 nil 인 worker 도 본 메소드는 호출 가능.
func (w *ParserWorker) ProcessMessage(ctx context.Context, msg *queue.Message) error {
	var ref core.RawContentRef
	if err := json.Unmarshal(msg.Value, &ref); err != nil {
		w.log.WithError(err).Error("malformed RawContentRef payload, dropping")
		// commit 을 호출자가 하도록 nil 반환 — payload 손상은 재시도 의미 없음
		return nil
	}

	// validator → parser 재학습 cycle (이슈 #366):
	//   1. inbox headers 를 ctx 에 첨부 — publishContents 가 reparse_* 를 normalized 메시지로 전파
	//   2. validate_reparse_reason 헤더가 있으면 ctx 에 llmgen.WithRejectReason 으로 주입 —
	//      claudegen.ExtractEnriched 가 prompt 에 reason context 포함 (Sub B #365 메커니즘)
	ctx = core.WithInboxHeaders(ctx, msg.Headers)
	if reason := msg.Headers[core.HeaderValidateReparseReason]; reason != "" {
		ctx = llmgen.WithRejectReason(ctx, reason)
	}

	mlog := w.log.WithFields(map[string]interface{}{
		"raw_id": ref.ID,
		"url":    ref.URL,
	})
	if reparseCount := msg.Headers[core.HeaderValidateReparseCount]; reparseCount != "" {
		mlog = mlog.WithFields(map[string]interface{}{
			"validate_reparse_count": reparseCount,
		})
		mlog.Info("parser processing reparse cycle message")
	}

	// parser 단계 StageGate (ProcessingLock + Semaphore 합성, 이슈 #355) —
	// 같은 URL 의 동시 파싱 차단 + parser stage 의 동시 처리 슬롯 cap.
	// Kafka rebalance / 재배달 시 같은 raw 가 두 parser worker 에 도달해도 1회만 처리.
	// ref.URL 은 fetcher 가 정규화한 URL — Ingestion Lock 키와 같은 정규형 사용.
	release, acquired, gateErr := w.gate.Acquire(ctx, ref.URL)
	if gateErr != nil {
		// ctx cancel / deadline — shutdown 또는 semaphore 대기 timeout. fail-open 으로 진행하면
		// 취소된 ctx 로 쓸데없는 작업 + per-stage cap 무력화 위험 (PR #358 gemini / Copilot 반영).
		// commit 안 하고 종료 → Kafka redeliver 보장.
		// SoT: ctx.Err() != nil (errors.Is(ctx.Canceled) 대비 정확 — 종료 사유 단일화).
		if ctx.Err() != nil {
			return fmt.Errorf("stage gate acquire aborted by ctx: %w", gateErr)
		}
		// 그 외 인프라 에러 (예: Redis 장애) — fail-open. 다른 worker 와의 dedup 만 일시 비활성화.
		mlog.WithError(gateErr).Warn("failed to acquire parser stage gate, proceeding without gate")
	} else if !acquired {
		mlog.Debug("parser processing lock already held by another worker, skipping")
		// 다른 parser worker 가 처리 중 — sentinel 반환으로 commit skip. runWorker 가 Warn 대신
		// Debug 로 처리. 처리 담당 worker 가 commit 책임 (PR #358 CodeRabbit 반영).
		return ErrStageGateNotAcquired
	} else {
		defer release()
	}

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
		return w.processCategoryPage(ctx, raw, ref.ID, crawlerName, jobTimeout, ref.LLMRetryCount, mlog)
	}

	// article 페이지 — ParsePage → ConvertPage → publish normalized
	return w.processArticlePage(ctx, raw, ref.ID, crawlerName, ref.LLMRetryCount, mlog)
}

func (w *ParserWorker) processCategoryPage(ctx context.Context, raw *core.RawContent, rawID, crawlerName string, jobTimeout time.Duration, llmRetryCount int, mlog *logger.Logger) error {
	// Category cycle 종료 시 PipelineGuard marker release — defer 로 어떤 경로
	// (성공 / handleRuleError / 0 links / publish 실패) 든 release 보장. Release 실패는 non-fatal
	// (TTL fallback 으로 자동 회수).
	defer w.releaseCategoryMarker(ctx, raw.URL, mlog)

	if w.parser == nil || w.pub == nil {
		mlog.Debug("parser or publisher not configured, skipping category job")
		w.deleteRaw(ctx, rawID, mlog)
		return nil
	}

	items, err := w.parser.ParseLinks(ctx, raw)
	if err != nil {
		return w.handleRuleError(ctx, raw, rawID, "parse_links", storage.TargetTypeList, err, llmRetryCount, crawlerName, jobTimeout, mlog)
	}
	if len(items) == 0 {
		mlog.Debug("no article links found in category page")
		w.deleteRaw(ctx, rawID, mlog)
		return nil
	}

	urls := uniqueURLs(items, maxChainedURLs)

	// page-parse 블랙리스트 적용 — mode 별 분기:
	//   - 'drop' 매칭     : 어느 슬라이스에도 미포함 (완전 drop, fetch / parse 안 함)
	//   - 'extract_links_only' 매칭 : list 로 강제 발행 → fetch + ParseLinks 만 진행 (ParsePage skip)
	//   - 매칭 X         : 정상 article 로 발행
	// Matcher 미설정 (nil) 이면 모든 URL 을 article 로 통과 (기능 OFF).
	articleURLs := urls
	var listURLs []string
	droppedCount := 0
	if w.blacklist != nil && len(urls) > 0 {
		decision := w.blacklist.Classify(ctx, urls)
		articleURLs = decision.Allowed
		listURLs = decision.ExtractLinksOnly
		droppedCount = len(urls) - len(decision.Allowed) - len(decision.ExtractLinksOnly)
	}

	if len(articleURLs) == 0 && len(listURLs) == 0 {
		mlog.WithFields(map[string]interface{}{
			"crawler":       crawlerName,
			"dropped_count": droppedCount,
		}).Info("all category links blocked by blacklist — no chained jobs published")
		w.deleteRaw(ctx, rawID, mlog)
		return nil
	}

	if len(articleURLs) > 0 {
		if err := w.pub.PublishChained(ctx, crawlerName, articleURLs, core.TargetTypeArticle, jobTimeout); err != nil {
			return fmt.Errorf("publish chained article jobs: %w", err)
		}
	}
	if len(listURLs) > 0 {
		if err := w.pub.PublishChained(ctx, crawlerName, listURLs, core.TargetTypeCategory, jobTimeout); err != nil {
			return fmt.Errorf("publish chained list jobs (extract_links_only): %w", err)
		}
	}

	mlog.WithFields(map[string]interface{}{
		"crawler":       crawlerName,
		"article_count": len(articleURLs),
		"list_count":    len(listURLs),
		"dropped_count": droppedCount,
	}).Info("chained jobs published from category page")

	// 카테고리 페이지는 contents/news_articles 에 저장하지 않음 — raw 즉시 정리
	w.deleteRaw(ctx, rawID, mlog)
	return nil
}

// releaseCategoryMarker 는 Category cycle 종료 시 PipelineGuard marker 를 release 합니다.
//
// guard 미설정 시 noop. Release 실패는 non-fatal — TTL 만료 fallback 으로 자동 회수.
// ctx.WithoutCancel 로 parent ctx 취소 신호 분리 — shutdown 중에도 release 시도 보장.
func (w *ParserWorker) releaseCategoryMarker(ctx context.Context, url string, mlog *logger.Logger) {
	if w.guard == nil {
		return
	}
	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := w.guard.Release(releaseCtx, url); err != nil {
		mlog.WithFields(map[string]interface{}{
			"url": url,
		}).WithError(err).Warn("pipeline guard release failed (non-fatal — TTL fallback)")
	}
}

func (w *ParserWorker) processArticlePage(ctx context.Context, raw *core.RawContent, rawID, crawlerName string, llmRetryCount int, mlog *logger.Logger) error {
	if w.parser == nil {
		mlog.Debug("parser not configured, skipping article")
		w.deleteRaw(ctx, rawID, mlog)
		return nil
	}

	page, err := w.parser.ParsePage(ctx, raw)
	if err != nil {
		return w.handleRuleError(ctx, raw, rawID, "parse_page", storage.TargetTypePage, err, llmRetryCount, crawlerName, 0, mlog)
	}

	// parse 자체는 성공했지만 Title / MainContent 텍스트 길이가 임계값 미달이면
	// 빈본문 신호로 host 카운터에 누적. 정상 흐름은 차단하지 않음 (downstream validator 가
	// 별도 정책으로 처리).
	w.recordEmptyBodyIfApplicable(ctx, raw, rawID, page, mlog)

	content := general.ConvertPage(page, raw)
	if err := w.publishContents(ctx, []*core.Content{content}, crawlerName); err != nil {
		return fmt.Errorf("publish article content: %w", err)
	}

	// 정상 파싱 성공 시 sample URL 누적 — 단계 4-2 의 정밀화 트리거 입력.
	// 누적 실패는 정상 흐름 차단 안 함 (warn 로그).
	w.accumulateSample(ctx, raw, mlog)

	w.deleteRaw(ctx, rawID, mlog)
	return nil
}

// handleRuleError 는 rule.Error (parse 실패) 와 그 외 에러를 구분합니다.
//
//   - rule.ErrNoRule + llmGen 활성화 → LLM rule generator 비동기 enqueue + raw 잔존 + commit
//   - 기타 rule.Error → raw 잔존 + commit (warn 로그) — 운영자 review 윈도우
//   - 기타 → 호출자에게 error 전파 → commit 안 함 → 재시도
//
// rule.ErrParseFailure / rule.ErrEmptySelector 는 host 단위 카운터로
// 누적 — 임계값 도달 시 단계 3 (#221) 의 chromedp 자동 전환 트리거 입력. ErrNoRule 은 LLM 자동
// rule 생성 의 책임 영역이라 카운팅 제외 (다른 정책으로 처리됨).
func (w *ParserWorker) handleRuleError(ctx context.Context, raw *core.RawContent, rawID, stage string, targetType storage.TargetType, err error, llmRetryCount int, crawlerName string, jobTimeout time.Duration, mlog *logger.Logger) error {
	var rerr *rule.Error
	if errors.As(err, &rerr) {
		mlog.WithFields(map[string]interface{}{
			"stage":       stage,
			"error_code":  string(rerr.Code),
			"target_type": string(targetType),
		}).WithError(err).Warn("rule-based parse failed, raw retained for LLM retry")

		// ErrNoRule + llmGen 활성화 → LLM 자동 rule 생성 비동기 트리거
		// crawlerName 은 validate 실패 시 재큐 메시지 헤더 복원용.
		// jobTimeout 은 pending 재투입 시 카테고리 chained job timeout 보존.
		//
		// Resolver miss (ErrNoRule) 가 운영자가 의도적으로 disable 한 룰 잔존
		// 인 경우 LLM 재학습 트리거 회피 — 매 fetch 마다 LLM 호출 → ErrDuplicate 흐름이
		// 운영자의 의도된 disable 을 무력화하지 않도록.
		// HasAnyRule lookup 실패는 best-effort (fail-open — 기존 동작 유지하여 LLM enqueue 진행).
		if rerr.Code == rule.ErrNoRule && w.llmGen != nil {
			shouldEnqueue := true
			if w.resolver != nil {
				exists, hasEnabled, herr := w.resolver.HasAnyRule(ctx, rerr.Host, targetType)
				shouldEnqueue = ShouldEnqueueLLMOnNoRule(exists, hasEnabled, herr)
				switch {
				case herr == nil && exists && !hasEnabled:
					// 운영자가 모든 룰을 disable 한 호스트 — LLM 재학습 회피, 운영 가시성 로그.
					mlog.WithFields(map[string]interface{}{
						"host":        rerr.Host,
						"target_type": string(targetType),
					}).Warn("rule exists but all disabled — skipping LLM regen (manual re-enable required)")
				case herr != nil && ctx.Err() == nil:
					// lookup 실패 — fail-open (warn 로그 + 기존 LLM enqueue 경로 진행).
					// ctx.Err() 체크: shutdown 중 cancellation 을 lookup 장애로 오인 회피.
					mlog.WithFields(map[string]interface{}{
						"host":        rerr.Host,
						"target_type": string(targetType),
					}).WithError(herr).Warn("HasAnyRule lookup failed, falling through to LLM enqueue")
				}
			}
			if shouldEnqueue {
				w.llmGen.Enqueue(ctx, rerr.Host, targetType, raw, llmRetryCount, crawlerName, jobTimeout)
			}
		}

		// page 단계의 ParseFailure / EmptySelector 만 host 카운터에 누적.
		// list (category) 는 본질적으로 다른 selector 셋이라 chromedp 전환 신호로 부적절.
		// 같은 시점에 raw_id 를 host 별 추적 — 단계 3 의 republish 대상 수집.
		// 동일 시점에 stale counter 누적 — 임계 도달 시 LLM 재학습 트리거.
		if targetType == storage.TargetTypePage &&
			(rerr.Code == rule.ErrParseFailure || rerr.Code == rule.ErrEmptySelector) {
			w.recordHostFailure(ctx, rerr.Host, rawID, storage.FailureReasonRuleParseFailure, mlog)
			w.recordStaleAndMaybeRelearn(ctx, rerr.Host, targetType, raw, llmRetryCount, crawlerName, jobTimeout, mlog)
		}

		_ = rawID // 본 시그니처에선 rawID 직접 사용 안 함, 향후 audit log 에서 활용
		// raw 는 의도적으로 잔존 — cleanup cron 이 TTL (default 1h) 후 정리
		// 또는 LLM 자동 rule 생성 + 운영자 enable=true flip 후 reprocess 가능
		return nil
	}
	return fmt.Errorf("%s: %w", stage, err)
}

// recordHostFailure 는 host 단위 fetcher 실패 카운터에 1건 누적하고 raw_id 를 host Set 에
// 추적합니다.
//
// 카운팅 / 트래킹 자체 실패는 non-fatal — warn 로그만 남기고 정상 흐름 유지 (Redis 장애가
// parse 실패 처리 흐름을 막지 않도록).
//
// 카운터가 thresholdReached=true 반환하면 Upgrader.Trigger 를 별도 goroutine 으로
// 비동기 호출 — parser 본 흐름의 latency 차단 회피. Upgrader 가 nil 이면 신호만 발신.
//
// rawID 는 단계 3 의 chromedp 자동 전환 trigger 가 republish 대상으로 사용. 빈 문자열이면
// Tracker 가 noop.
func (w *ParserWorker) recordHostFailure(ctx context.Context, host, rawID string, reason storage.FailureReason, mlog *logger.Logger) {
	if w.failureCounter == nil {
		return
	}
	_, thresholdReached, err := w.failureCounter.Record(ctx, host, reason)
	if err != nil {
		mlog.WithFields(map[string]interface{}{
			"host":   host,
			"reason": string(reason),
		}).WithError(err).Warn("failure counter record failed (non-fatal)")
	}
	if w.rawIDTracker != nil {
		if err := w.rawIDTracker.Track(ctx, host, rawID); err != nil {
			mlog.WithFields(map[string]interface{}{
				"host":   host,
				"raw_id": rawID,
			}).WithError(err).Warn("raw id tracker track failed (non-fatal)")
		}
	}
	// 임계값 도달 시 자동 chromedp 전환 + 실패 raw republish trigger.
	// 비동기 — parser 흐름의 latency 차단 회피. Upgrader 자체가 in-flight dedup / 이미 chromedp
	// skip 등 안전망 보유.
	//
	// context.WithoutCancel + WithTimeout: parent ctx 의 logger / trace_id 등 value 는 보존하되
	// (gemini 피드백) parent cancel 과 분리. 다만 unbounded 면 의존성 stall 시 goroutine leak 위험
	// (CodeRabbit 피드백) — 30s timeout 으로 bound. Upgrader 의 5분 in-flight lock TTL 보다 짧지만
	// Redis/DB/Kafka 호출 한 사이클은 30s 면 충분.
	if thresholdReached && w.upgrader != nil {
		go func() {
			triggerCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			defer cancel()
			w.upgrader.Trigger(triggerCtx, host)
		}()
	}
}

// recordStaleAndMaybeRelearn 은 stale rule 발생 1건을 (host, target_type) 카운터에 누적하고
// 임계 도달 시 LLM 재학습을 트리거합니다.
//
// 동작 요건 (모두 충족 시만 enqueue):
//  1. staleCounter / llmGen 둘 다 wiring 됨 (둘 중 하나 nil 이면 noop)
//  2. host != "" — 빈 host 는 카운팅 skip
//  3. counter.Record 가 thresholdReached=true 반환
//  4. resolver 의 HasAnyRule 결과 — 운영자가 모든 rule 을 disable 한 host 는 skip
//
// 카운팅 / lookup 실패는 non-fatal — warn 로그만 남기고 정상 흐름 유지. 임계 도달 후 EnqueueStale
// 은 비동기 (Generator 내부 goroutine) 라 본 함수 latency 영향 없음.
func (w *ParserWorker) recordStaleAndMaybeRelearn(ctx context.Context, host string, targetType storage.TargetType, raw *core.RawContent, llmRetryCount int, crawlerName string, jobTimeout time.Duration, mlog *logger.Logger) {
	if w.staleCounter == nil || w.llmGen == nil {
		return
	}
	if host == "" {
		return
	}
	count, thresholdReached, err := w.staleCounter.Record(ctx, host, targetType)
	if err != nil {
		mlog.WithFields(map[string]interface{}{
			"host":        host,
			"target_type": string(targetType),
		}).WithError(err).Warn("stale counter record failed (non-fatal)")
		return
	}
	if !thresholdReached {
		return
	}
	// thresholdReached 는 window 내 count >= threshold 일 때
	// 매번 true 이므로, 첫 crossing (count == threshold) 만 enqueue 하여 동일 window 안의 중복
	// version 생성 / 비용 spike 회피. staleThreshold == 0 이면 호환 모드 (gate 비활성).
	if w.staleThreshold > 0 && count != w.staleThreshold {
		mlog.WithFields(map[string]interface{}{
			"host":        host,
			"target_type": string(targetType),
			"count":       count,
			"threshold":   w.staleThreshold,
		}).Debug("stale threshold already crossed in current window — skipping duplicate enqueue")
		return
	}

	// 운영자 disable 잔존 검증 — 같은 fail-open 정책.
	shouldEnqueue := true
	if w.resolver != nil {
		exists, hasEnabled, herr := w.resolver.HasAnyRule(ctx, host, targetType)
		shouldEnqueue = ShouldEnqueueLLMOnNoRule(exists, hasEnabled, herr)
		if herr == nil && exists && !hasEnabled {
			mlog.WithFields(map[string]interface{}{
				"host":        host,
				"target_type": string(targetType),
				"count":       count,
			}).Warn("stale threshold reached but rule disabled — skipping LLM relearn (manual re-enable required)")
		} else if herr != nil && ctx.Err() == nil {
			mlog.WithFields(map[string]interface{}{
				"host":        host,
				"target_type": string(targetType),
			}).WithError(herr).Warn("HasAnyRule lookup failed during stale relearn, falling through to enqueue")
		}
	}
	if !shouldEnqueue {
		return
	}

	mlog.WithFields(map[string]interface{}{
		"host":        host,
		"target_type": string(targetType),
		"count":       count,
	}).Info("stale rule relearn triggered — enqueueing LLM regen via version bump")
	w.llmGen.EnqueueStale(ctx, host, targetType, raw, llmRetryCount, crawlerName, jobTimeout)
}

// hostOf 는 raw.URL 에서 host 만 추출합니다 (port 제거).
// 파싱 실패 시 빈 문자열 — 호출자가 빈 host 를 noop 으로 흡수.
func hostOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Hostname()
}

// recordEmptyBodyIfApplicable 는 page 의 Title / MainContent 길이가 임계값 미달이면
// host 카운터에 empty_body 신호로 누적합니다.
//
// 임계값 어느 한쪽 미달이어도 신호 발신 — chromedp 전환 후 둘 다 정상 추출되는 케이스 가정.
// 임계값이 0 이면 해당 필드 검증 비활성 (운영 옵션).
//
// rawID 도 host 별 추적 — 단계 3 의 republish 대상 수집.
func (w *ParserWorker) recordEmptyBodyIfApplicable(ctx context.Context, raw *core.RawContent, rawID string, page *parser.Page, mlog *logger.Logger) {
	if w.emptyBodyTitleMin <= 0 && w.emptyBodyContentMin <= 0 {
		return
	}
	// gemini 피드백: byte 길이 대신 rune count — 한글 (multibyte) 페이지 다수라 byte 기준이면
	// 같은 임계값에서 의도보다 훨씬 짧은 본문이 통과됨. 임계값은 글자 수 의미로 통일.
	titleLen := utf8.RuneCountInString(page.Title)
	bodyLen := utf8.RuneCountInString(page.MainContent)
	titleShort := w.emptyBodyTitleMin > 0 && titleLen < w.emptyBodyTitleMin
	bodyShort := w.emptyBodyContentMin > 0 && bodyLen < w.emptyBodyContentMin
	if !titleShort && !bodyShort {
		return
	}
	host := hostOf(raw.URL)
	if host == "" {
		return
	}
	mlog.WithFields(map[string]interface{}{
		"host":         host,
		"title_length": titleLen,
		"body_length":  bodyLen,
		"reason":       string(storage.FailureReasonEmptyBody),
	}).Warn("empty body detected — recording host failure")
	w.recordHostFailure(ctx, host, rawID, storage.FailureReasonEmptyBody, mlog)
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

		headers := map[string]string{
			"source":           c.SourceID,
			"country":          c.Country,
			core.HeaderCrawler: crawlerName,
		}
		// reparse / trace 헤더 propagation — 화이트리스트 기반 (이슈 #366 gemini 반영).
		// validate worker 가 reparse_count 헤더로 retry 한도 enforce — 미전파 시 무한 루프 위험.
		core.PropagateInboxHeaders(ctx, headers)

		msg := queue.Message{
			Topic:   queue.TopicNormalized,
			Key:     []byte(partitionKey),
			Value:   pmBytes,
			Headers: headers,
		}
		if err := w.pub.Forward(ctx, msg); err != nil {
			return fmt.Errorf("publish normalized: %w", err)
		}
	}
	return nil
}

const (
	// maxLLMRetries 는 LLM selector 검증 실패 시 raw content 를 재큐잉할 최대 횟수입니다.
	// 이 값을 초과하면 재큐잉 없이 raw 를 TTL cleanup 에 맡깁니다.
	maxLLMRetries = 3
)

// RequeueForLLMRetry 는 LLM selector 검증 실패 시 raw content 를 issuetracker.fetched 에
// 재발행합니다. llmgen.Generator 의 validateFailureHandler 로 등록됩니다.
//
// llmRetryCount >= maxLLMRetries 이면 재큐잉을 중단하고 Warn 로그만 남깁니다 — 무한루프 방지.
// targetType, crawlerName 은 Kafka 메시지 헤더로 설정 — 재큐 후 processMessage 가 올바른
// 파싱 경로 (category vs article) 로 분기할 수 있도록 보존합니다 (gemini/Copilot/CodeRabbit 피드백).
// 재발행 실패는 non-fatal — raw 는 TTL cleanup 으로 최종 정리됩니다.
func (w *ParserWorker) RequeueForLLMRetry(ctx context.Context, ref core.RawContentRef, llmRetryCount int, targetType storage.TargetType, crawlerName string) {
	nextCount := llmRetryCount + 1
	if nextCount > maxLLMRetries {
		w.log.WithFields(map[string]interface{}{
			"raw_id":          ref.ID,
			"url":             ref.URL,
			"llm_retry_count": llmRetryCount,
			"max_llm_retries": maxLLMRetries,
		}).Warn("llm retry limit reached, abandoning raw content requeue")
		return
	}

	requeued := core.RawContentRef{
		ID:            ref.ID,
		URL:           ref.URL,
		FetchedAt:     ref.FetchedAt,
		SourceInfo:    ref.SourceInfo,
		LLMRetryCount: nextCount,
	}
	payload, err := json.Marshal(requeued)
	if err != nil {
		w.log.WithFields(map[string]interface{}{
			"raw_id": ref.ID,
		}).WithError(err).Error("failed to marshal RawContentRef for llm requeue")
		return
	}

	// Publish timeout 바운딩 — Kafka 장애 시 goroutine 장시간 정체 방지 (Copilot 피드백).
	pubCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()

	msg := queue.Message{
		Topic: queue.TopicFetched,
		Value: payload,
		Headers: map[string]string{
			// storageToFetcherTargetType: storage "list"/"page" → fetcher "category"/"article"
			// 파서 워커가 target_type 을 core.TargetType 으로 읽으므로 변환 필수.
			"target_type": storageToFetcherTargetType(targetType),
			"crawler":     crawlerName,
		},
	}
	if err := w.pub.Forward(pubCtx, msg); err != nil {
		w.log.WithFields(map[string]interface{}{
			"raw_id":          ref.ID,
			"url":             ref.URL,
			"llm_retry_count": nextCount,
		}).WithError(err).Warn("failed to requeue raw content for llm retry (non-fatal)")
		return
	}

	w.log.WithFields(map[string]interface{}{
		"raw_id":          ref.ID,
		"url":             ref.URL,
		"llm_retry_count": nextCount,
		"target_type":     storageToFetcherTargetType(targetType),
		"crawler":         crawlerName,
	}).Info("raw content requeued for llm retry after selector validation failure")
}

// RequeueParsing 은 pending 대기 URL 목록을 issuetracker.fetched 에 재발행합니다.
// llmgen.Generator 의 RequeueFunc 로 등록됩니다.
//
// 룰 생성 완료 후 호출 — 대기 중이던 URL 이 새 룰로 재파싱될 수 있도록 TopicFetched 에 투입.
// Kafka 발행에 실패한 항목을 반환 — Generator 가 pending queue 에 재적재.
func (w *ParserWorker) RequeueParsing(ctx context.Context, items []llmgen.PendingItem) (failed []llmgen.PendingItem) {
	// WithoutCancel 은 루프 외부에서 1회 — 루프마다 새 컨텍스트 파생 불필요 (Gemini 피드백).
	baseCtx := context.WithoutCancel(ctx)
	for _, item := range items {
		// LLMRetryCount 를 ref 에 반영하여 직렬화 — item.RawRef 는 원본(0)이므로 별도 세팅 필요
		ref := item.RawRef
		ref.LLMRetryCount = item.LLMRetryCount
		payload, err := json.Marshal(ref)
		if err != nil {
			w.log.WithFields(map[string]interface{}{
				"raw_id": item.RawRef.ID,
			}).WithError(err).Error("failed to marshal RawContentRef for pending requeue")
			failed = append(failed, item)
			continue
		}

		pubCtx, cancel := context.WithTimeout(baseCtx, 10*time.Second)
		msg := queue.Message{
			Topic: queue.TopicFetched,
			Value: payload,
			Headers: map[string]string{
				// storage "list"/"page" → fetcher "category"/"article" 변환.
				"target_type": storageToFetcherTargetType(item.TargetType),
				"crawler":     item.CrawlerName,
				// timeout_ms 보존 — 카테고리 재투입 시 chained job timeout 유지.
				"timeout_ms": strconv.FormatInt(item.TimeoutMs, 10),
			},
		}
		if err := w.pub.Forward(pubCtx, msg); err != nil {
			w.log.WithFields(map[string]interface{}{
				"raw_id": item.RawRef.ID,
				"url":    item.RawRef.URL,
			}).WithError(err).Warn("failed to requeue pending URL for parsing, will re-push to pending queue")
			failed = append(failed, item)
		}
		cancel()
	}
	return failed
}

// deleteRaw 는 처리 완료된 raw_contents row 를 즉시 정리합니다.
// 실패는 non-fatal — cleanup cron 이 안전망으로 동작.
func (w *ParserWorker) deleteRaw(ctx context.Context, rawID string, mlog *logger.Logger) {
	if err := w.rawSvc.Delete(ctx, rawID); err != nil {
		mlog.WithError(err).Warn("raw delete failed (non-fatal — cleanup cron will catch)")
	}
}

// accumulateSample 은 정상 파싱 성공한 article 의 URL 을 sample 에 누적합니다.
//
// 누적 조건 (모두 충족 시만):
//  1. resolver / sampleSvc 둘 다 wiring 됨
//  2. raw.URL 의 host 로 활성 rule lookup 성공
//  3. 매칭된 rule.PathPattern == "" (catch-all — 정밀화 대상)
//  4. 매칭된 rule.SourceName == llmgen.LLMAutoSourceName (운영자 hand-tuned 는 정밀화 대상 아님 — 누적 X)
//
// 모든 실패 (lookup 실패 / Insert 에러 / cap 도달) 는 정상 흐름 차단 X — DEBUG/WARN 로그만.
//
// 변수명 matchedRule: import 된 rule 패키지와의 shadowing 회피.
func (w *ParserWorker) accumulateSample(ctx context.Context, raw *core.RawContent, mlog *logger.Logger) {
	if w.resolver == nil || w.sampleSvc == nil {
		return
	}
	matchedRule, err := w.resolver.ResolveByURL(ctx, raw.URL, storage.TargetTypePage)
	if err != nil {
		// rule 매칭 안 되거나 resolver 에러 — 정상 파싱 후라 비정상 상황. 단지 디버그 로그.
		mlog.WithError(err).Debug("sample accumulate: rule lookup failed")
		return
	}
	if matchedRule.PathPattern != "" || matchedRule.SourceName != llmgen.LLMAutoSourceName {
		// 정밀화 대상 아닌 rule — 누적 skip (catch-all + llm-auto 만 누적)
		return
	}
	if err := w.sampleSvc.Insert(ctx, matchedRule.ID, raw.URL); err != nil {
		// 중복은 정상 (이미 누적된 URL) — 그 외만 warn.
		if !errors.Is(err, storage.ErrDuplicate) {
			mlog.WithFields(map[string]interface{}{
				"rule_id": matchedRule.ID,
				"url":     raw.URL,
			}).WithError(err).Warn("sample accumulate failed (non-fatal)")
		}
		return
	}
	mlog.WithFields(map[string]interface{}{
		"rule_id": matchedRule.ID,
		"url":     raw.URL,
	}).Debug("sample accumulated for refinement trigger")
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

// ShouldEnqueueLLMOnNoRule 은 ErrNoRule 상황에서 HasAnyRule 결과로 LLM Enqueue 여부를 결정합니다.
//
// 정책:
//   - lookup 실패 (err != nil)         : fail-open — true (기존 동작 유지)
//   - !exists                          : 진짜 룰 부재 — true
//   - exists && hasEnabled             : cache stale 의심 — true (Generator pre-check 가 차단)
//   - exists && !hasEnabled            : 운영자 disable 잔존 — false (수동 재활성 영역)
//
// pure function — 외부 의존성 없음. handleRuleError 의 결정 로직만 분리해 단위 테스트 용이.
func ShouldEnqueueLLMOnNoRule(exists, hasEnabled bool, lookupErr error) bool {
	if lookupErr != nil {
		return true
	}
	if exists && !hasEnabled {
		return false
	}
	return true
}

// storageToFetcherTargetType 은 storage.TargetType ("list"/"page") 을
// TopicFetched 헤더에서 사용하는 core.TargetType 문자열 ("category"/"article") 로 변환합니다.
//
// 초기 fetcher 는 core.TargetType 값을 target_type 헤더로 발행하므로,
// RequeueParsing / RequeueForLLMRetry 에서 재발행 시 동일 변환이 필요합니다.
func storageToFetcherTargetType(t storage.TargetType) string {
	if t == storage.TargetTypeList {
		return string(core.TargetTypeCategory)
	}
	return string(core.TargetTypeArticle)
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
