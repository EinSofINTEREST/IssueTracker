// Package worker 는 enrich 단계의 Kafka consumer worker 를 제공합니다 (이슈 #446).
//
// 본 sub-issue 는 스켈레톤 단계 — 입력 (TopicValidated) 메시지를 그대로 TopicEnriched 로
// forward 만 수행. 실제 enrichment 로직 (claudegen 호출, 교차 검증, 외부 맥락, 신뢰도 점수) 은
// 후속 sub-issue (#447 ~ #450) 에서 점진적으로 채워집니다.
//
// 패키지 구조는 validate worker (internal/processor/validate/worker) 를 미러링.
package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"issuetracker/internal/bus"
	"issuetracker/internal/locks"
	enrichcore "issuetracker/internal/processor/enrich/core"
	"issuetracker/internal/processor/fetcher/core"
	"issuetracker/internal/storage"
	"issuetracker/internal/storage/model"
	"issuetracker/internal/storage/repository"
	"issuetracker/internal/storage/service"
	"issuetracker/internal/workerpool"
	"issuetracker/pkg/logger"
	"issuetracker/pkg/queue"
)

// StageName 은 enrich 단계의 식별자입니다 (locks.StageEnricher 와 일치).
const StageName = "enricher"

// drainTimeout 은 graceful shutdown 후 Kafka publish 를 한 번 더 시도할 때 사용하는 별도
// context 의 타임아웃입니다 (at-least-once).
const drainTimeout = workerpool.DefaultDrainTimeout

// Worker 는 issuetracker.validated 토픽을 소비하여 enrich 후 issuetracker.enriched 에 발행합니다.
//
// 이슈 #447 에서 추가된 동작:
//   - ContentService 로 ref.ID 의 Content (Title/Body 포함) 조회
//   - core.Extract 로 EnrichedFacts 추출
//
// 이슈 #448 에서 추가된 동작:
//   - extracted claims 가 있으면 ContentService 로 동일 국가·최근 윈도우 후보 조회
//   - title token overlap 기반 top-N 후보 선정 후 verifier.Verify 호출
//   - facts.Verifications 에 결과 첨부
//
// 결과는 ProcessingMessage.Metadata["enriched_facts"] 에 JSON 으로 첨부 후 forward.
// 추출/검증 실패는 파이프라인을 멈추지 않습니다 — warn 로깅 + 빈 결과로 fallback + forward 진행.
// content 조회 실패 (ErrNotFound) 는 idempotent 정상 분기 — 이미 처리된 메시지로 보고 commit.
type Worker struct {
	consumer       bus.Consumer
	pub            *bus.Publisher
	contentSvc     service.ContentService
	extractor      enrichcore.Extractor
	verifier       enrichcore.Verifier
	contextualizer enrichcore.Contextualizer
	scorer         enrichcore.Scorer
	enrichedRepo   repository.EnrichedContentRepository
	gate           locks.StageGate // nil 허용 → NoopStageGate 로 fallback
	workerCount    int

	pool *workerpool.ConsumerPool

	// retryScheduler 는 process 실패 시 재시도 발행 경로 (이슈 #524 / 메타 #515 Phase 2).
	// nil 허용 — nil 이면 기존 동작 (commit skip → Kafka redeliver).
	//
	// ZSET 인입 모드에서는 BZPOPMIN 이 곧 ack 라 commit skip 으로 redeliver 불가 — RetryScheduler
	// 주입 필수. 주입 시 Handle 이 process error 에 대해 Enqueue 후 commit (메시지 손실 방지).
	retryScheduler bus.RetryScheduler
}

// NewWorker 는 새로운 Worker 를 생성합니다.
//
// pub / contentSvc / extractor / verifier / contextualizer / scorer 가 nil 이면 panic — fail-fast.
// enrichedRepo 는 nil 허용 — nil 이면 DB write skip + metadata 첨부만 (sub-issue #450 도입 전 동작 호환).
// gate 가 nil 이면 NoopStageGate 로 fallback.
func NewWorker(
	consumer bus.Consumer,
	pub *bus.Publisher,
	contentSvc service.ContentService,
	ex enrichcore.Extractor,
	vr enrichcore.Verifier,
	cz enrichcore.Contextualizer,
	sc enrichcore.Scorer,
	enrichedRepo repository.EnrichedContentRepository,
	gate locks.StageGate,
	workerCount int,
) *Worker {
	if pub == nil {
		panic("enrich.NewWorker: pub must not be nil")
	}
	if contentSvc == nil {
		panic("enrich.NewWorker: contentSvc must not be nil")
	}
	if ex == nil {
		panic("enrich.NewWorker: extractor must not be nil (use enrichcore.NoopExtractor for disabled enrichment)")
	}
	if vr == nil {
		panic("enrich.NewWorker: verifier must not be nil (use enrichcore.NoopVerifier for disabled verification)")
	}
	if cz == nil {
		panic("enrich.NewWorker: contextualizer must not be nil (use enrichcore.NoopContextualizer for disabled context)")
	}
	if sc == nil {
		panic("enrich.NewWorker: scorer must not be nil (use enrichcore.NoopScorer for disabled scoring)")
	}
	if gate == nil {
		gate = locks.NewNoopStageGate()
	}
	return &Worker{
		consumer:       consumer,
		pub:            pub,
		contentSvc:     contentSvc,
		extractor:      ex,
		verifier:       vr,
		contextualizer: cz,
		scorer:         sc,
		enrichedRepo:   enrichedRepo,
		gate:           gate,
		workerCount:    workerCount,
	}
}

// SetRetryScheduler 는 process 실패 시 재시도 발행 경로를 주입합니다 (이슈 #524 / 메타 #515 Phase 2).
//
// nil 주입 시 기존 동작 (commit skip → Kafka redeliver) 유지. ZSET 인입 모드에서는 pop 이
// 곧 ack 이라 commit skip 으로는 redeliver 불가 — 본 setter 로 RetryScheduler 를 반드시 주입.
//
// Start 호출 전 wiring 단계에서 1회 설정.
func (w *Worker) SetRetryScheduler(rs bus.RetryScheduler) {
	w.retryScheduler = rs
}

// Start 는 workerpool harness 를 기동합니다. 인스턴스당 1회만 호출 (2회차는 panic).
func (w *Worker) Start(ctx context.Context) {
	if w.pool != nil {
		panic("enrich worker: Start called more than once on the same instance")
	}
	plainLog := logger.FromContext(ctx)
	ctx = plainLog.WithField("worker_pool", StageName).ToContext(ctx)

	w.pool = workerpool.New(workerpool.Config{
		Consumer:     w.consumer,
		Handler:      w,
		WorkerCount:  w.workerCount,
		DrainTimeout: drainTimeout,
		Log:          plainLog,
		Name:         StageName,
	})
	w.pool.Start(ctx)
}

// Stop 은 workerpool harness 의 정상 종료를 수행합니다.
// 미기동 (Start 미호출) 상태에서는 consumer.Close 만 수행.
func (w *Worker) Stop(ctx context.Context) error {
	if w.pool == nil {
		return w.consumer.Close()
	}
	return w.pool.Stop(ctx)
}

// Handle 은 workerpool.Handler 구현 — 각 메시지마다 호출됩니다.
//
// process 의 결과에 따른 분기:
//   - nil → 성공 (process 내부에서 commit 이미 수행)
//   - context.Canceled → graceful shutdown (DEBUG 강등)
//   - 그 외 process 실패:
//   - retryScheduler 주입 시 (이슈 #524): RetryScheduler.Enqueue → commit (메시지 손실 방지)
//   - enqueue 실패 시 DLQ fallback (영구 손실 방지)
//   - 미주입 시: commit skip (Kafka redeliver, 기존 동작)
func (w *Worker) Handle(ctx context.Context, msg *queue.Message) {
	log := logger.FromContext(ctx)
	err := w.process(ctx, msg)
	if err == nil {
		return
	}
	if errors.Is(err, context.Canceled) {
		log.WithError(err).Debug("enrich worker canceled during shutdown")
		return
	}
	if w.retryScheduler != nil {
		if enqueueErr := w.enqueueRetry(ctx, msg, err); enqueueErr != nil {
			// ZSET 모드에서 메시지는 이미 pop 됐으므로 enqueue 실패 시 영구 손실 — fallback 으로
			// DLQ 발행하여 운영 가시성 + 수동 복구 가능.
			log.WithError(enqueueErr).Warn("retry enqueue failed, sending to dlq as fallback")
			if dlqErr := w.sendToDLQ(ctx, msg, fmt.Errorf("retry enqueue failed: %w (original: %v)", enqueueErr, err)); dlqErr != nil {
				log.WithError(dlqErr).Error("dlq fallback failed, message will be lost in zset mode")
				return
			}
			if commitErr := w.commit(ctx, msg); commitErr != nil && ctx.Err() == nil {
				log.WithError(commitErr).Warn("commit after dlq fallback failed")
			}
			return
		}
		if commitErr := w.commit(ctx, msg); commitErr != nil && ctx.Err() == nil {
			log.WithError(commitErr).Warn("commit after retry enqueue failed")
		}
		log.WithError(err).Info("enrich process failed, enqueued for retry")
		return
	}
	log.WithError(err).Error("enrich worker failed to process message")
}

// enqueueRetry 는 process 실패한 메시지를 RetryScheduler 경유로 재시도 큐에 등록합니다 (이슈 #524).
//
// ContentRef → CrawlJob 변환은 BuildRetryJob 으로 분리 (unit test 용이성).
func (w *Worker) enqueueRetry(ctx context.Context, msg *queue.Message, lastErr error) error {
	job, err := BuildRetryJob(msg)
	if err != nil {
		return err
	}
	return w.retryScheduler.Enqueue(ctx, job, lastErr)
}

// BuildRetryJob 은 process 실패한 Kafka 메시지를 retry 용 CrawlJob 으로 변환합니다 (이슈 #524).
//
// ProcessingMessage.Data 에서 ContentRef 를 추출하여 URL + CrawlerName + priority + target_type
// 을 복원. msg.Headers 가 우선:
//   - crawler 헤더 우선, 없으면 ContentRef.SourceInfo.Name, 둘 다 빈 값이면 "enrich-retry"
//   - priority 헤더는 PriorityFromHeader 헬퍼로 통일
//   - target_type 헤더가 유효 (article/category) 이면 사용, 없으면 Article default
//
// retry_reason / original_ref_id 메타로 추적 가시성 제공.
//
// 빈 URL 또는 unmarshal 실패 시 error — 호출자가 retry 불가로 분기.
func BuildRetryJob(msg *queue.Message) (*core.CrawlJob, error) {
	var pm core.ProcessingMessage
	if uerr := json.Unmarshal(msg.Value, &pm); uerr != nil {
		return nil, fmt.Errorf("retry unmarshal processing message: %w", uerr)
	}
	var ref core.ContentRef
	if uerr := json.Unmarshal(pm.Data, &ref); uerr != nil {
		return nil, fmt.Errorf("retry unmarshal content ref: %w", uerr)
	}
	if ref.URL == "" {
		return nil, fmt.Errorf("retry unmarshal: empty URL in ContentRef %s", ref.ID)
	}

	priority := core.Priority(queue.PriorityFromHeader(msg.Headers))

	crawlerName := msg.Headers["crawler"]
	if crawlerName == "" {
		crawlerName = ref.SourceInfo.Name
	}
	if crawlerName == "" {
		crawlerName = "enrich-retry"
	}

	targetType := core.TargetTypeArticle
	if t, ok := msg.Headers["target_type"]; ok {
		if tt := core.TargetType(t); isValidTargetType(tt) {
			targetType = tt
		}
	}

	// timeout — 원본 timeout_ms 헤더 계승, 부재 시 기본 (gemini PR #527 #3275211693 패턴).
	jobTimeout := buildRetryDefaultTimeout
	if raw := msg.Headers[core.HeaderTimeoutMs]; raw != "" {
		if ms, perr := strconv.Atoi(raw); perr == nil && ms > 0 {
			jobTimeout = time.Duration(ms) * time.Millisecond
		}
	}

	return &core.CrawlJob{
		ID:          ref.ID,
		CrawlerName: crawlerName,
		Target: core.Target{
			URL:  ref.URL,
			Type: targetType,
			Metadata: map[string]interface{}{
				"retry_reason":    "enrich_process_failed",
				"original_ref_id": ref.ID,
			},
		},
		Priority:    priority,
		ScheduledAt: time.Now(),
		Timeout:     jobTimeout,
		MaxRetries:  bus.DefaultMaxRetries,
	}, nil
}

// buildRetryDefaultTimeout 은 BuildRetryJob 의 timeout_ms 헤더가 부재할 때 사용하는 기본값입니다.
const buildRetryDefaultTimeout = 30 * time.Second

// 이슈 #524 (gemini #3278202670 DRY) — PriorityFromHeader 는 queue.PriorityFromHeader 로 이관.
// 본 패키지의 동일 이름 함수는 제거. 단위 테스트는 test/pkg/queue/priority_header_test.go.

// isValidTargetType 은 retry job 에 적용 가능한 TargetType 인지 검증합니다.
// article / category 만 허용 — 알 수 없는 값은 caller 가 Article default 로 보정.
func isValidTargetType(t core.TargetType) bool {
	switch t {
	case core.TargetTypeArticle, core.TargetTypeCategory:
		return true
	default:
		return false
	}
}

// process 는 단일 메시지를 처리합니다.
//
// 본 sub-issue 의 동작: 메시지 deserialize → ContentRef.URL 기반 StageGate acquire → passthrough
// publish (TopicEnriched) → commit. 후속 sub-issue 가 acquire 이후 enrichment 로직을 삽입.
func (w *Worker) process(ctx context.Context, msg *queue.Message) error {
	log := logger.FromContext(ctx)

	var pm core.ProcessingMessage
	if err := json.Unmarshal(msg.Value, &pm); err != nil {
		log.WithError(err).Error("failed to unmarshal processing message, sending to dlq")
		if dlqErr := w.sendToDLQ(ctx, msg, err); dlqErr != nil {
			return fmt.Errorf("send to dlq (unmarshal): %w", dlqErr)
		}
		return w.commit(ctx, msg)
	}

	var ref core.ContentRef
	if err := json.Unmarshal(pm.Data, &ref); err != nil {
		log.WithFields(map[string]interface{}{
			"job_id": pm.ID,
		}).WithError(err).Error("failed to unmarshal content ref, sending to dlq")
		if dlqErr := w.sendToDLQ(ctx, msg, err); dlqErr != nil {
			return fmt.Errorf("send to dlq (ref unmarshal): %w", dlqErr)
		}
		return w.commit(ctx, msg)
	}

	release, acquired, gateErr := w.gate.Acquire(ctx, ref.URL)
	if gateErr != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("enricher stage gate acquire aborted by ctx: %w", gateErr)
		}
		log.WithFields(map[string]interface{}{
			"job_id": pm.ID,
			"ref_id": ref.ID,
		}).WithError(gateErr).Warn("failed to acquire enricher stage gate, proceeding without gate")
	} else if !acquired {
		log.WithFields(map[string]interface{}{
			"job_id": pm.ID,
			"ref_id": ref.ID,
		}).Debug("enricher processing lock already held by another worker, skipping")
		return nil
	} else {
		defer release()
	}

	// 이슈 #447 / #448 / #449 / #450 — Content 조회 + extractor + cross-verify + context + score + DB upsert + facts 첨부.
	// 본 단계 어떤 실패도 forward 를 막지 않음 — pipeline 진행이 enrichment 보다 우선.
	content, facts := w.runExtraction(ctx, &pm, &ref)
	if facts != nil {
		w.runVerification(ctx, &pm, &ref, content, facts)
		w.runContextEnrichment(ctx, &pm, &ref, content, facts)
		w.runScoring(ctx, &pm, &ref, content, facts)
		w.persistEnriched(ctx, &pm, &ref, facts)
		w.attachFacts(&pm, facts)
	}

	if err := w.publishEnriched(ctx, &ref, &pm, msg); err != nil {
		if errors.Is(err, context.Canceled) {
			drainCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), drainTimeout)
			defer cancel()
			if drainErr := w.publishEnriched(drainCtx, &ref, &pm, msg); drainErr != nil {
				return fmt.Errorf("publish enriched ref %s (drain retry failed): %w", ref.ID, drainErr)
			}
			return w.commit(drainCtx, msg)
		}
		return fmt.Errorf("publish enriched ref %s: %w", ref.ID, err)
	}

	return w.commit(ctx, msg)
}

// runExtraction 은 content 를 조회하고 extractor 를 호출합니다.
// 모든 실패 경로는 (nil, nil) 을 반환 — 호출자가 facts 첨부 skip 하고 forward 진행.
// 성공 시 (content, facts) 를 함께 반환 — 호출자가 verification 단계에서 content 메타데이터
// (Country/PublishedAt) 를 재사용할 수 있도록.
func (w *Worker) runExtraction(ctx context.Context, pm *core.ProcessingMessage, ref *core.ContentRef) (*core.Content, *enrichcore.EnrichedFacts) {
	log := logger.FromContext(ctx)

	content, err := w.contentSvc.GetByID(ctx, ref.ID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			log.WithFields(map[string]interface{}{
				"job_id": pm.ID,
				"ref_id": ref.ID,
			}).Info("content not found, skipping enrichment (already deleted or duplicate)")
			return nil, nil
		}
		log.WithFields(map[string]interface{}{
			"job_id": pm.ID,
			"ref_id": ref.ID,
		}).WithError(err).Warn("failed to fetch content for enrichment, forwarding without facts")
		return nil, nil
	}

	host := ""
	// url.Parse 실패 시 host 가 빈 문자열로 남고 extractor 가 degraded 입력을 받는데,
	// 이는 forward-first 정책 일관 — 다만 진단을 위해 DEBUG 로깅 (coderabbit-review PR #452).
	if u, perr := url.Parse(ref.URL); perr != nil {
		log.WithFields(map[string]interface{}{
			"job_id": pm.ID,
			"ref_id": ref.ID,
			"url":    ref.URL,
		}).WithError(perr).Debug("url parse failed for host extraction, using empty host")
	} else {
		host = u.Host
	}

	in := enrichcore.Input{
		URL:   ref.URL,
		Host:  host,
		Title: content.Title,
		HTML:  content.Body,
	}

	facts, err := w.extractor.Extract(ctx, in)
	if err != nil {
		log.WithFields(map[string]interface{}{
			"job_id": pm.ID,
			"ref_id": ref.ID,
			"host":   host,
		}).WithError(err).Warn("enrichment extraction failed, forwarding without facts")
		return content, nil
	}

	log.WithFields(map[string]interface{}{
		"job_id":       pm.ID,
		"ref_id":       ref.ID,
		"entity_count": len(facts.Entities),
		"claim_count":  len(facts.Claims),
		"fact_count":   len(facts.Facts),
	}).Debug("enrichment extraction completed")
	return content, facts
}

// runVerification 은 추출된 claims 에 대해 cross-verification 을 수행하고 facts.Verifications
// 에 결과를 첨부합니다.
//
// 모든 실패 경로는 verifications 미첨부로 fallback — pipeline 진행 보장 (forward-first).
// claims 가 없으면 skip — verifier 호출 불필요.
//
// 이슈 #472 부터 DB 후보 prefetch / in-Go ranking 은 제거됨 — LLM 이 MCP postgres 도구로
// contents 테이블을 직접 조회 + WebFetch 로 외부 보강. 한글 title 의 ASCII-only tokenize
// 깨짐 (이슈 #469) 도 본 변경으로 자동 해소.
func (w *Worker) runVerification(
	ctx context.Context,
	pm *core.ProcessingMessage,
	ref *core.ContentRef,
	content *core.Content,
	facts *enrichcore.EnrichedFacts,
) {
	log := logger.FromContext(ctx)

	if facts == nil || len(facts.Claims) == 0 {
		return
	}
	if content == nil {
		return
	}

	in := enrichcore.VerifyInput{
		URL:    ref.URL,
		Host:   hostOf(ref.URL),
		Title:  content.Title,
		Claims: facts.Claims,
	}

	verifications, err := w.verifier.Verify(ctx, in)
	if err != nil {
		log.WithFields(map[string]interface{}{
			"job_id":      pm.ID,
			"ref_id":      ref.ID,
			"claim_count": len(facts.Claims),
		}).WithError(err).Warn("enrichment verification failed, forwarding without verifications")
		return
	}
	facts.Verifications = verifications
	log.WithFields(map[string]interface{}{
		"job_id":             pm.ID,
		"ref_id":             ref.ID,
		"claim_count":        len(facts.Claims),
		"verification_count": len(verifications),
	}).Debug("enrichment verification completed")
}

// runContextEnrichment 는 entities + claims 를 입력으로 외부 맥락을 수집하고
// facts.Context 에 첨부합니다 (이슈 #449).
//
// entities/claims 가 모두 비어있으면 skip. contextualizer error / 빈 결과는 미첨부 — forward 보장.
func (w *Worker) runContextEnrichment(
	ctx context.Context,
	pm *core.ProcessingMessage,
	ref *core.ContentRef,
	content *core.Content,
	facts *enrichcore.EnrichedFacts,
) {
	log := logger.FromContext(ctx)

	if facts == nil {
		return
	}
	if len(facts.Entities) == 0 && len(facts.Claims) == 0 {
		return
	}

	title := ""
	if content != nil {
		title = content.Title
	}
	in := enrichcore.ContextInput{
		URL:      ref.URL,
		Host:     hostOf(ref.URL),
		Title:    title,
		Entities: facts.Entities,
		Claims:   facts.Claims,
	}

	pageCtx, err := w.contextualizer.Provide(ctx, in)
	if err != nil {
		log.WithFields(map[string]interface{}{
			"job_id":       pm.ID,
			"ref_id":       ref.ID,
			"entity_count": len(facts.Entities),
			"claim_count":  len(facts.Claims),
		}).WithError(err).Warn("enrichment context provider failed, forwarding without context")
		return
	}
	if pageCtx.IsEmpty() {
		log.WithFields(map[string]interface{}{
			"job_id": pm.ID,
			"ref_id": ref.ID,
		}).Debug("context provider returned empty result, skipping attach")
		return
	}
	facts.Context = pageCtx

	// pageCtx 는 위 IsEmpty() 가 false → 도달 시점에 non-nil 보장 (gemini-review PR #454).
	log.WithFields(map[string]interface{}{
		"job_id":           pm.ID,
		"ref_id":           ref.ID,
		"background_count": len(pageCtx.Background),
		"timeline_count":   len(pageCtx.Timeline),
		"has_implications": pageCtx.Implications != nil,
	}).Debug("enrichment context completed")
}

// runScoring 은 추출 + 검증 + 외부 맥락 결과를 종합하여 trust_score 를 산출하고 facts.TrustScore
// 에 첨부합니다 (이슈 #450).
//
// facts 가 nil 이거나 facts 가 모두 empty (claims / verifications / context 부재) 면 skip.
// scorer error 시 미첨부 + forward 계속 (forward-first 정책).
func (w *Worker) runScoring(
	ctx context.Context,
	pm *core.ProcessingMessage,
	ref *core.ContentRef,
	content *core.Content,
	facts *enrichcore.EnrichedFacts,
) {
	log := logger.FromContext(ctx)

	if facts == nil {
		return
	}
	// 점수 산출 근거가 전혀 없으면 skip — extractor 가 빈 facts 만 반환한 경우 등.
	if len(facts.Claims) == 0 && len(facts.Verifications) == 0 && facts.Context.IsEmpty() {
		return
	}

	factsJSON, verificationsJSON, contextJSON, err := marshalFactsTriple(facts)
	if err != nil {
		log.WithFields(map[string]interface{}{
			"job_id": pm.ID,
			"ref_id": ref.ID,
		}).WithError(err).Warn("marshal facts triple for scoring failed, forwarding without trust_score")
		return
	}

	title := ""
	if content != nil {
		title = content.Title
	}
	in := enrichcore.ScoreInput{
		URL:           ref.URL,
		Host:          hostOf(ref.URL),
		Title:         title,
		Facts:         factsJSON,
		Verifications: verificationsJSON,
		Context:       contextJSON,
	}

	res, err := w.scorer.Score(ctx, in)
	if err != nil {
		log.WithFields(map[string]interface{}{
			"job_id": pm.ID,
			"ref_id": ref.ID,
		}).WithError(err).Warn("enrichment scoring failed, forwarding without trust_score")
		return
	}
	if res == nil {
		return
	}
	score := res.TrustScore
	facts.TrustScore = &score
	// 이슈 #457: scorer 진단 정보 (rationale + factors) 도 facts 에 첨부 → 후속 persistEnriched 가 DB 영속화.
	facts.Rationale = res.Rationale
	factors := res.Factors
	facts.TrustFactors = &factors
	log.WithFields(map[string]interface{}{
		"job_id":      pm.ID,
		"ref_id":      ref.ID,
		"trust_score": score,
	}).Debug("enrichment scoring completed")
}

// persistEnriched 는 facts 결과를 enriched_contents 테이블에 UPSERT 합니다 (이슈 #450).
//
// 다음 조건들에서는 skip (forward 만 계속):
//   - enrichedRepo 가 nil (미configured 환경)
//   - facts.TrustScore 가 nil (scoring 실패 / Noop)
//
// DB 쓰기 실패는 warn 로깅 후 forward — DB 일시 장애가 pipeline 을 막지 않도록 (forward-first).
func (w *Worker) persistEnriched(
	ctx context.Context,
	pm *core.ProcessingMessage,
	ref *core.ContentRef,
	facts *enrichcore.EnrichedFacts,
) {
	log := logger.FromContext(ctx)
	if w.enrichedRepo == nil {
		return
	}
	if facts == nil || facts.TrustScore == nil {
		return
	}

	factsJSON, verificationsJSON, contextJSON, err := marshalFactsTriple(facts)
	if err != nil {
		log.WithFields(map[string]interface{}{
			"job_id": pm.ID,
			"ref_id": ref.ID,
		}).WithError(err).Warn("marshal facts triple for persistence failed, skipping enriched upsert")
		return
	}

	// 이슈 #457 — scorer 진단 정보 영속화. TrustFactors 가 nil 이면 빈 객체.
	var factorsJSON []byte
	if facts.TrustFactors != nil {
		factorsJSON, err = json.Marshal(facts.TrustFactors)
		if err != nil {
			log.WithFields(map[string]interface{}{
				"job_id": pm.ID,
				"ref_id": ref.ID,
			}).WithError(err).Warn("marshal trust factors failed, persisting with empty factors")
			factorsJSON = nil
		}
	}

	rec := &model.EnrichedContentRecord{
		ContentID:     ref.ID,
		TrustScore:    *facts.TrustScore,
		Facts:         factsJSON,
		Verifications: verificationsJSON,
		Context:       contextJSON,
		Rationale:     facts.Rationale,
		Factors:       factorsJSON,
	}
	if err := w.enrichedRepo.Upsert(ctx, rec); err != nil {
		log.WithFields(map[string]interface{}{
			"job_id":      pm.ID,
			"ref_id":      ref.ID,
			"trust_score": *facts.TrustScore,
		}).WithError(err).Warn("enriched_contents upsert failed, forwarding anyway")
		return
	}
	log.WithFields(map[string]interface{}{
		"job_id":      pm.ID,
		"ref_id":      ref.ID,
		"enriched_id": rec.ID,
		"trust_score": *facts.TrustScore,
	}).Debug("enriched_contents upsert completed")
}

// hostOf 는 URL 의 host 부분만 추출합니다. parse 실패 시 빈 문자열 반환.
func hostOf(u string) string {
	parsed, err := url.Parse(u)
	if err != nil {
		return ""
	}
	return parsed.Host
}

// attachFacts 는 추출된 facts 를 ProcessingMessage.Metadata 에 JSON 으로 저장합니다.
//
// 본 sub-issue 는 wire format 으로 metadata 활용 — 후속 #450 sub-issue 가 별도
// enriched_contents 테이블에 영속화하면 metadata 의존을 줄일 수 있음.
//
// 직렬화 실패 시 best-effort — 메시지는 그대로 forward (warn 로깅 후 skip).
func (w *Worker) attachFacts(pm *core.ProcessingMessage, facts *enrichcore.EnrichedFacts) {
	if pm.Metadata == nil {
		pm.Metadata = map[string]interface{}{}
	}
	pm.Metadata["enriched_facts"] = facts
}

// publishEnriched 는 ContentRef 를 TopicEnriched 에 발행합니다.
func (w *Worker) publishEnriched(ctx context.Context, ref *core.ContentRef, pm *core.ProcessingMessage, orig *queue.Message) error {
	data, err := json.Marshal(ref)
	if err != nil {
		return fmt.Errorf("marshal content ref: %w", err)
	}

	out := core.ProcessingMessage{
		ID:        pm.ID,
		Timestamp: time.Now(),
		Country:   ref.Country,
		Stage:     "enriched",
		Data:      data,
		Metadata:  pm.Metadata,
	}

	outBytes, err := json.Marshal(out)
	if err != nil {
		return fmt.Errorf("marshal processing message: %w", err)
	}

	// 원본 메시지 헤더 (trace ID / request ID 등 observability 메타데이터) 를 base 로 복사 후
	// stage-specific 키만 덮어쓰기 — sendToDLQ 패턴과 일관 (gemini-review #451 반영).
	headers := make(map[string]string, len(orig.Headers)+3)
	for k, v := range orig.Headers {
		headers[k] = v
	}
	headers["source"] = ref.SourceInfo.Name
	headers["country"] = ref.Country
	headers["stage"] = "enriched"

	outMsg := queue.Message{
		Topic:   queue.TopicEnriched,
		Key:     orig.Key,
		Value:   outBytes,
		Headers: headers,
	}

	return w.pub.Forward(ctx, outMsg)
}

// sendToDLQ 는 메시지를 DLQ 토픽으로 발행합니다.
func (w *Worker) sendToDLQ(ctx context.Context, msg *queue.Message, reason error) error {
	log := logger.FromContext(ctx)

	headers := make(map[string]string, len(msg.Headers)+2)
	for k, v := range msg.Headers {
		headers[k] = v
	}
	headers["original-topic"] = msg.Topic
	headers["error"] = reason.Error()

	dlqMsg := queue.Message{
		Topic:   queue.TopicDLQ,
		Key:     msg.Key,
		Value:   msg.Value,
		Headers: headers,
	}

	err := w.pub.Forward(ctx, dlqMsg)
	if err != nil && errors.Is(err, context.Canceled) {
		drainCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), drainTimeout)
		defer cancel()
		err = w.pub.Forward(drainCtx, dlqMsg)
	}
	if err != nil {
		log.WithError(err).Error("failed to send message to dlq")
		return err
	}
	return nil
}

// commit 은 Kafka offset 을 commit 합니다 (drain 재시도 포함).
func (w *Worker) commit(ctx context.Context, msg *queue.Message) error {
	if w.pool == nil {
		return workerpool.CommitWithDrain(ctx, w.consumer, msg, drainTimeout)
	}
	return w.pool.Commit(ctx, msg)
}

// promptFactsPayload 는 facts 의 LLM/DB 직렬화 공통 wire format 입니다 (이슈 #450).
//
// runScoring (LLM 입력) / persistEnriched (DB JSONB) 두 곳에서 동일한 facts schema 를
// 사용해야 하므로 익명 구조체 중복을 제거하고 본 타입으로 통합 (gemini-review PR #455).
//
// json 태그는 omitempty 미사용 — DB 영속화 시 일관된 schema (모든 필드 항상 존재) 보장.
// LLM 입력 측에서도 빈 배열이 명시적으로 보여 prompt 가 부재 vs zero-count 를 구분 가능.
type promptFactsPayload struct {
	Entities  []enrichcore.Entity  `json:"entities"`
	Claims    []enrichcore.Claim   `json:"claims"`
	Facts     []enrichcore.Fact    `json:"facts"`
	Topics    []string             `json:"topics"`
	Sentiment enrichcore.Sentiment `json:"sentiment"`
}

// marshalFactsTriple 은 facts / verifications / context 를 LLM 입력 + DB 영속화에 공통으로
// 쓰이는 JSON triple 로 직렬화합니다 (gemini-review PR #455).
//
// 직렬화 실패 시 첫 에러 즉시 반환 — caller (runScoring / persistEnriched) 는 forward 만 진행.
func marshalFactsTriple(facts *enrichcore.EnrichedFacts) (factsJSON, verificationsJSON, contextJSON []byte, err error) {
	factsJSON, err = json.Marshal(promptFactsPayload{
		Entities:  facts.Entities,
		Claims:    facts.Claims,
		Facts:     facts.Facts,
		Topics:    facts.Topics,
		Sentiment: facts.Sentiment,
	})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("marshal facts: %w", err)
	}
	verificationsJSON, err = json.Marshal(facts.Verifications)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("marshal verifications: %w", err)
	}
	if facts.Context != nil {
		contextJSON, err = json.Marshal(facts.Context)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("marshal context: %w", err)
		}
	}
	return factsJSON, verificationsJSON, contextJSON, nil
}
