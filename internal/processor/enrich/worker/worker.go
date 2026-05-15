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
	"sort"
	"strings"
	"time"

	"issuetracker/internal/bus"
	"issuetracker/internal/locks"
	"issuetracker/internal/processor/enrich/extractor"
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
//   - extractor.Extract 로 EnrichedFacts 추출
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
	extractor      extractor.Extractor
	verifier       extractor.Verifier
	contextualizer extractor.Contextualizer
	scorer         extractor.Scorer
	enrichedRepo   repository.EnrichedContentRepository
	gate           locks.StageGate // nil 허용 → NoopStageGate 로 fallback
	workerCount    int

	pool *workerpool.ConsumerPool
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
	ex extractor.Extractor,
	vr extractor.Verifier,
	cz extractor.Contextualizer,
	sc extractor.Scorer,
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
		panic("enrich.NewWorker: extractor must not be nil (use extractor.NoopExtractor for disabled enrichment)")
	}
	if vr == nil {
		panic("enrich.NewWorker: verifier must not be nil (use extractor.NoopVerifier for disabled verification)")
	}
	if cz == nil {
		panic("enrich.NewWorker: contextualizer must not be nil (use extractor.NoopContextualizer for disabled context)")
	}
	if sc == nil {
		panic("enrich.NewWorker: scorer must not be nil (use extractor.NoopScorer for disabled scoring)")
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
func (w *Worker) Handle(ctx context.Context, msg *queue.Message) {
	log := logger.FromContext(ctx)
	if err := w.process(ctx, msg); err != nil {
		if errors.Is(err, context.Canceled) {
			log.WithError(err).Debug("enrich worker canceled during shutdown")
		} else {
			log.WithError(err).Error("enrich worker failed to process message")
		}
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
func (w *Worker) runExtraction(ctx context.Context, pm *core.ProcessingMessage, ref *core.ContentRef) (*core.Content, *extractor.EnrichedFacts) {
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

	in := extractor.Input{
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

// 검증 단계 상수 (이슈 #448).
const (
	// verificationWindow: 후보 article 시간 윈도우 — 발행 시각 ±N. ±24h 가 일반적인 뉴스 사이클.
	verificationWindow = 24 * time.Hour
	// verificationCandidateFetchLimit: DB 후보 조회 fetch limit — 너무 많으면 ranking 비용 증가.
	verificationCandidateFetchLimit = 50
	// verificationTopK: 최종 verifier 에 넘기는 후보 수 — prompt context 비용 보호.
	verificationTopK = 5
)

// runVerification 은 추출된 claims 에 대해 cross-verification 을 수행하고 facts.Verifications
// 에 결과를 첨부합니다.
//
// 모든 실패 경로는 verifications 미첨부로 fallback — pipeline 진행 보장 (forward-first).
// claims 가 없으면 skip — verifier 호출 불필요.
//
// 후보 선정:
//  1. ContentService.ListByCountry 로 동일 country + 시간 윈도우 (±24h) 내 최근 발행 article fetch (limit 50)
//  2. 본인 article (URL 또는 ID 동일) 제외
//  3. title token overlap 으로 ranking → top 5
//
// 그 5개를 CandidateRef 로 verifier 에 전달. verifier (prompt) 가 WebFetch 로 추가 신규
// 페치 + 검증을 수행.
func (w *Worker) runVerification(
	ctx context.Context,
	pm *core.ProcessingMessage,
	ref *core.ContentRef,
	content *core.Content,
	facts *extractor.EnrichedFacts,
) {
	log := logger.FromContext(ctx)

	if facts == nil || len(facts.Claims) == 0 {
		return
	}
	if content == nil {
		return
	}

	candidates := w.findCandidates(ctx, content)
	host := hostOf(ref.URL)

	in := extractor.VerifyInput{
		URL:        ref.URL,
		Host:       host,
		Title:      content.Title,
		Claims:     facts.Claims,
		Candidates: candidates,
	}

	verifications, err := w.verifier.Verify(ctx, in)
	if err != nil {
		log.WithFields(map[string]interface{}{
			"job_id":          pm.ID,
			"ref_id":          ref.ID,
			"claim_count":     len(facts.Claims),
			"candidate_count": len(candidates),
		}).WithError(err).Warn("enrichment verification failed, forwarding without verifications")
		return
	}
	facts.Verifications = verifications
	log.WithFields(map[string]interface{}{
		"job_id":             pm.ID,
		"ref_id":             ref.ID,
		"claim_count":        len(facts.Claims),
		"candidate_count":    len(candidates),
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
	facts *extractor.EnrichedFacts,
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
	in := extractor.ContextInput{
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
	facts *extractor.EnrichedFacts,
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
	in := extractor.ScoreInput{
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
	facts *extractor.EnrichedFacts,
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

	rec := &model.EnrichedContentRecord{
		ContentID:     ref.ID,
		TrustScore:    *facts.TrustScore,
		Facts:         factsJSON,
		Verifications: verificationsJSON,
		Context:       contextJSON,
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

// findCandidates 는 본 content 와 같은 country + 시간 윈도우의 후보 article 들을 조회하고
// title token overlap 으로 ranking 한 top-K 를 반환합니다. 본인 article 은 제외.
//
// 본 메소드는 DB error 시 빈 slice 반환 — verifier 는 DB 후보 없이도 WebFetch 만으로 진행 가능.
func (w *Worker) findCandidates(ctx context.Context, src *core.Content) []extractor.CandidateRef {
	log := logger.FromContext(ctx)

	// PublishedAt zero-value 면 시간 윈도우 적용 불가 — 빈 후보로 진행.
	if src.PublishedAt.IsZero() {
		return nil
	}

	after := src.PublishedAt.Add(-verificationWindow)
	before := src.PublishedAt.Add(verificationWindow)
	filter := model.ContentFilter{
		PublishedAfter:  &after,
		PublishedBefore: &before,
		Pagination:      model.Pagination{Limit: verificationCandidateFetchLimit},
	}
	rows, err := w.contentSvc.ListByCountry(ctx, src.Country, filter)
	if err != nil {
		log.WithError(err).Debug("candidate fetch failed, proceeding with empty candidates")
		return nil
	}

	srcTokens := tokenize(src.Title)
	type scored struct {
		c     *core.Content
		score int
	}
	ranked := make([]scored, 0, len(rows))
	for _, r := range rows {
		if r == nil {
			continue
		}
		// 본인 제외 (ID 또는 URL 일치)
		if r.ID == src.ID || (r.URL != "" && r.URL == src.URL) {
			continue
		}
		s := tokenOverlap(srcTokens, tokenize(r.Title))
		if s == 0 {
			continue
		}
		ranked = append(ranked, scored{c: r, score: s})
	}
	sort.Slice(ranked, func(i, j int) bool { return ranked[i].score > ranked[j].score })

	limit := verificationTopK
	if len(ranked) < limit {
		limit = len(ranked)
	}
	out := make([]extractor.CandidateRef, 0, limit)
	for i := 0; i < limit; i++ {
		c := ranked[i].c
		out = append(out, extractor.CandidateRef{
			URL:   c.URL,
			Title: c.Title,
			Host:  hostOf(c.URL),
		})
	}
	return out
}

// hostOf 는 URL 의 host 부분만 추출합니다. parse 실패 시 빈 문자열 반환.
func hostOf(u string) string {
	parsed, err := url.Parse(u)
	if err != nil {
		return ""
	}
	return parsed.Host
}

// tokenize 는 title 을 소문자 단어 토큰 set 으로 분해합니다. 길이 2 이하 토큰은 stopword 제외.
func tokenize(s string) map[string]struct{} {
	out := map[string]struct{}{}
	cur := make([]rune, 0, 32)
	flush := func() {
		if len(cur) > 2 {
			out[strings.ToLower(string(cur))] = struct{}{}
		}
		cur = cur[:0]
	}
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			cur = append(cur, r)
		} else {
			flush()
		}
	}
	flush()
	return out
}

// tokenOverlap 은 두 토큰 set 의 교집합 크기를 반환합니다.
func tokenOverlap(a, b map[string]struct{}) int {
	if len(a) > len(b) {
		a, b = b, a
	}
	n := 0
	for t := range a {
		if _, ok := b[t]; ok {
			n++
		}
	}
	return n
}

// attachFacts 는 추출된 facts 를 ProcessingMessage.Metadata 에 JSON 으로 저장합니다.
//
// 본 sub-issue 는 wire format 으로 metadata 활용 — 후속 #450 sub-issue 가 별도
// enriched_contents 테이블에 영속화하면 metadata 의존을 줄일 수 있음.
//
// 직렬화 실패 시 best-effort — 메시지는 그대로 forward (warn 로깅 후 skip).
func (w *Worker) attachFacts(pm *core.ProcessingMessage, facts *extractor.EnrichedFacts) {
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
	Entities  []extractor.Entity  `json:"entities"`
	Claims    []extractor.Claim   `json:"claims"`
	Facts     []extractor.Fact    `json:"facts"`
	Topics    []string            `json:"topics"`
	Sentiment extractor.Sentiment `json:"sentiment"`
}

// marshalFactsTriple 은 facts / verifications / context 를 LLM 입력 + DB 영속화에 공통으로
// 쓰이는 JSON triple 로 직렬화합니다 (gemini-review PR #455).
//
// 직렬화 실패 시 첫 에러 즉시 반환 — caller (runScoring / persistEnriched) 는 forward 만 진행.
func marshalFactsTriple(facts *extractor.EnrichedFacts) (factsJSON, verificationsJSON, contextJSON []byte, err error) {
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
