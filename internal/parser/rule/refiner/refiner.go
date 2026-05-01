// Package refiner 는 catch-all + llm-auto rule 의 누적 sample URL 로부터 path_pattern 을
// 추론하여 자동 갱신하는 점진적 정밀화 워크플로를 구현합니다 (이슈 #173 단계 4-2).
//
// 흐름 (Run goroutine 1회 cycle):
//  1. ParsingRuleRepository.List(SourceName='llm-auto', OnlyEnabled=true) 로 후보 rule 조회.
//  2. PathPattern == "" (catch-all) 인 rule 만 정밀화 대상으로 선별.
//  3. 각 대상 rule 마다:
//     a. SampleURLRepository.Count(rule.ID) >= MinSamples 검사 — 미만이면 skip.
//     b. SampleURLRepository.List(rule.ID, MinSamples) 로 sample 로드.
//     c. pathinfer.InferHeuristic(samples) 시도 — 성공하면 algorithm 방식 채택.
//     d. 실패 + LLMClient != nil 이면 pathinfer.InferLLM(samples) 시도 — 성공하면 llm 방식 채택.
//     e. 결과 regex 가 비어있으면 skip (다음 polling 에서 재시도 — sample 누적 추가 후 재평가).
//     f. ParsingRuleRepository.UpdatePathPattern — rule.PathPattern + description (정밀화 시각/방식) 갱신.
//     g. resolver.Invalidate(host, type) — cache flush (다음 lookup 부터 갱신된 rule 적용).
//     h. SampleURLRepository.Purge(rule.ID) — sample 정리 (다음 cycle 에서 재누적).
//
// 모든 실패는 non-fatal — 단계별 Warn 로그만 기록하고 다음 rule / 다음 cycle 로 진행.
//
// goroutine-safe: Refiner 자체는 단일 goroutine 에서만 동작 (Run). 의존성 (repo / resolver /
// LLMClient) 은 호출자 책임으로 goroutine-safe.
package refiner

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"issuetracker/internal/parser/rule"
	"issuetracker/internal/parser/rule/llmgen"
	"issuetracker/internal/parser/rule/pathinfer"
	"issuetracker/internal/storage"
	"issuetracker/pkg/logger"
)

// DefaultInterval 은 polling 주기의 기본값입니다 (운영 LoadRefinement 와 일치).
const DefaultInterval = 5 * time.Minute

// DefaultMinSamples 는 rule 당 정밀화 트리거 임계값의 기본값입니다 (운영 LoadRefinement 와 일치).
const DefaultMinSamples = 5

// Refiner 는 정밀화 polling goroutine 의 본체입니다.
//
// LLMClient 는 nil 허용 — nil 이면 InferLLM 단계 skip (algorithm-only).
// 운영자가 LLM 비활성 환경 (REFINEMENT 활성 + LLM_ENABLE=false) 도 동작.
//
// Lifecycle (PR #191 피드백):
//   - Start(ctx) 가 background goroutine 으로 polling 시작 — sync.WaitGroup 으로 추적
//   - Stop(ctx) 호출 시 in-flight cycle 의 완료 대기 (graceful shutdown). ctx cancel 시
//     대기 timeout — in-flight 호출은 background ctx 가 아니라 Start 의 ctx 를 사용하므로
//     Start ctx 가 cancel 된 상태라면 즉시 정리됨.
//   - Start 두 번 호출은 두 번째가 noop, Stop 후 Start 는 noop (race 안전).
//   - 테스트 친화 — Run / RunOnce 는 그대로 유지하여 외부에서 단일 cycle 호출 가능.
type Refiner struct {
	rules    storage.ParsingRuleRepository
	samples  storage.SampleURLRepository
	resolver *rule.Resolver
	llm      pathinfer.LLMClient // nil 허용
	metrics  *Metrics            // nil 허용 (Record* 메소드가 noop)
	log      *logger.Logger

	interval   time.Duration
	minSamples int

	wg      sync.WaitGroup
	started atomic.Bool
	stopped atomic.Bool
}

// Option 은 Refiner 생성 옵션입니다.
type Option func(*Refiner)

// WithInterval 은 polling 주기를 override 합니다 (d > 0 일 때만).
func WithInterval(d time.Duration) Option {
	return func(r *Refiner) {
		if d > 0 {
			r.interval = d
		}
	}
}

// WithMinSamples 는 rule 당 트리거 임계값을 override 합니다 (n > 0 일 때만).
func WithMinSamples(n int) Option {
	return func(r *Refiner) {
		if n > 0 {
			r.minSamples = n
		}
	}
}

// WithLLMClient 는 InferLLM 에 사용할 LLMClient 를 주입합니다.
// nil 이면 algorithm-only 동작 (InferLLM 단계 skip).
func WithLLMClient(c pathinfer.LLMClient) Option {
	return func(r *Refiner) { r.llm = c }
}

// WithMetrics 는 Prometheus collector 를 주입합니다 (PR #191 피드백).
// nil 또는 미지정 시 모든 Record* 호출이 noop — REFINEMENT 활성 + METRICS 비활성 환경 cover.
func WithMetrics(m *Metrics) Option {
	return func(r *Refiner) { r.metrics = m }
}

// New 는 Refiner 를 생성합니다. rules / samples / resolver / log 가 nil 이면 panic —
// wire 누락 즉시 가시화. LLMClient 만 nil 허용 (algorithm-only 동작).
func New(
	rules storage.ParsingRuleRepository,
	samples storage.SampleURLRepository,
	resolver *rule.Resolver,
	log *logger.Logger,
	opts ...Option,
) *Refiner {
	if rules == nil {
		panic("refiner: New requires non-nil rules repo")
	}
	if samples == nil {
		panic("refiner: New requires non-nil samples repo")
	}
	if resolver == nil {
		panic("refiner: New requires non-nil resolver")
	}
	if log == nil {
		panic("refiner: New requires non-nil logger")
	}
	r := &Refiner{
		rules:      rules,
		samples:    samples,
		resolver:   resolver,
		log:        log,
		interval:   DefaultInterval,
		minSamples: DefaultMinSamples,
	}
	for _, o := range opts {
		o(r)
	}
	return r
}

// Start 는 background goroutine 으로 polling 을 시작하고 즉시 반환합니다 (PR #191 피드백).
//
// 첫 호출만 goroutine 을 spawn — 이후 호출은 noop (atomic CAS). Stop 후 호출도 noop.
// 호출자는 Stop(ctx) 으로 in-flight cycle 의 완료를 대기 가능.
func (r *Refiner) Start(ctx context.Context) {
	if r.stopped.Load() {
		return
	}
	if !r.started.CompareAndSwap(false, true) {
		return
	}
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		r.Run(ctx)
	}()
}

// Stop 은 in-flight polling cycle 의 완료를 대기합니다 (graceful shutdown, PR #191 피드백).
//
// Start 의 ctx 가 이미 cancel 된 상태라면 in-flight RunOnce 가 ctx 전파로 빠르게 종료되어
// Stop 도 즉시 반환. ctx (Stop 인자) 가 cancel 되면 대기 timeout — in-flight 가 길게 걸리는
// 경우 강제 반환. Stop 자체는 idempotent — 두 번째 호출은 즉시 반환.
//
// 호출자는 Start 의 ctx 를 먼저 cancel 한 후 Stop 을 호출해야 함 — 그래야 polling loop 가
// 종료 신호를 받음. 일반 패턴:
//
//	rootCtx, cancel := context.WithCancel(ctx)
//	refiner.Start(rootCtx)
//	// ... 실행 ...
//	cancel()  // signal stop
//	refiner.Stop(shutdownCtx)  // wait for in-flight cycle
func (r *Refiner) Stop(ctx context.Context) {
	if !r.stopped.CompareAndSwap(false, true) {
		return
	}
	if !r.started.Load() {
		return // Start 호출 안 됨 — 대기할 goroutine 없음
	}

	done := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		r.log.Info("refiner stopped — in-flight cycle completed")
	case <-ctx.Done():
		r.log.Warn("refiner stop timeout — in-flight cycle may still be running")
	}
}

// Run 은 ctx 가 끝날 때까지 interval 주기로 한 번씩 RunOnce 를 호출합니다.
//
// 첫 cycle 은 interval 만큼 대기 후 시작 — 시작 직후 burst 회피.
// ctx.Done 시 정상 반환 (in-flight RunOnce 는 ctx 전파로 자체 종료).
//
// 일반 운영 코드는 Start(ctx) + Stop(ctx) 사용 권장 — Run 은 직접 단일 goroutine 으로
// 띄우는 테스트 / 임베디드 시나리오용.
func (r *Refiner) Run(ctx context.Context) {
	r.log.WithFields(map[string]interface{}{
		"interval":    r.interval.String(),
		"min_samples": r.minSamples,
		"llm_enabled": r.llm != nil,
	}).Info("refiner started")

	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			r.log.Info("refiner stopped")
			return
		case <-ticker.C:
			if err := r.RunOnce(ctx); err != nil {
				r.log.WithError(err).Warn("refiner cycle failed (non-fatal)")
			}
		}
	}
}

// RunOnce 는 한 번의 정밀화 cycle 을 실행합니다.
//
// 모든 rule 의 단계별 실패는 cycle 안에서 흡수 (Warn 로그) — 함수는 cycle 자체의 치명적
// 에러 (rule List 실패) 만 에러 반환. test 친화 — Run 외부에서 단발 호출 가능.
func (r *Refiner) RunOnce(ctx context.Context) error {
	candidates, err := r.rules.List(ctx, storage.ParsingRuleFilter{
		SourceName:  llmgen.LLMAutoSourceName,
		OnlyEnabled: true,
		Limit:       1000, // 운영상 llm-auto rule 은 호스트 수 만큼 — 1000 이면 충분.
	})
	if err != nil {
		return fmt.Errorf("list llm-auto rules: %w", err)
	}

	for _, rec := range candidates {
		// catch-all (PathPattern=="") 만 정밀화 대상.
		if rec.PathPattern != "" {
			continue
		}
		r.refineOne(ctx, rec)
	}
	return nil
}

// refineOne 은 단일 rule 에 대한 정밀화 단계를 수행합니다.
// 모든 단계 실패는 함수 내에서 Warn 로그 + 조용히 return — 호출자가 다음 rule 로 진행 가능.
func (r *Refiner) refineOne(ctx context.Context, rec *storage.ParsingRuleRecord) {
	rlog := r.log.WithFields(map[string]interface{}{
		"rule_id": rec.ID,
		"host":    rec.HostPattern,
		"target":  string(rec.TargetType),
	})

	count, err := r.samples.Count(ctx, rec.ID)
	if err != nil {
		rlog.WithError(err).Warn("sample count failed")
		r.metrics.RecordAttempt(ResultError, MethodNone)
		return
	}
	if count < r.minSamples {
		// trigger 미충족 — 다음 cycle 까지 대기.
		rlog.WithFields(map[string]interface{}{
			"sample_count": count,
			"min_samples":  r.minSamples,
		}).Debug("sample threshold not reached")
		r.metrics.RecordAttempt(ResultSkipped, MethodNone)
		return
	}

	samples, err := r.samples.List(ctx, rec.ID, r.minSamples)
	if err != nil {
		rlog.WithError(err).Warn("sample list failed")
		r.metrics.RecordAttempt(ResultError, MethodNone)
		return
	}
	paths := extractPaths(samples)
	if len(paths) < r.minSamples {
		// URL parse 실패 등으로 path 가 부족 — 다음 cycle 에 재시도.
		rlog.WithFields(map[string]interface{}{
			"sample_count": len(samples),
			"path_count":   len(paths),
		}).Warn("not enough valid paths after extraction")
		r.metrics.RecordAttempt(ResultSkipped, MethodNone)
		return
	}

	// 1) algorithm 우선 시도 → 실패 시 LLM fallback. LLM 호출 에러는 별도 분기 (PR #191 gemini 피드백).
	pattern, method, inferErr := inferPattern(ctx, paths, r.llm, r.minSamples, r.metrics)
	if inferErr != nil {
		rlog.WithFields(map[string]interface{}{
			"sample_count": len(paths),
		}).WithError(inferErr).Warn("llm inference call failed (non-fatal — next cycle will retry)")
		r.metrics.RecordAttempt(ResultError, MethodLLM)
		return
	}
	if pattern == "" {
		rlog.WithFields(map[string]interface{}{
			"sample_count": len(paths),
			"llm_enabled":  r.llm != nil,
		}).Debug("no pattern inferred (algorithm + llm both rejected)")
		// algorithm 실패 + (LLM 비활성 또는 LLM 거부) — algorithm 단계까지는 시도된 셈.
		// LLM 이 활성이었지만 invalid 결과로 거부됐을 때도 포함됨 (LLM 호출 자체는 RecordLLMCall 로 별도 추적).
		fallbackMethod := MethodAlgorithm
		if r.llm != nil {
			fallbackMethod = MethodLLM
		}
		r.metrics.RecordAttempt(ResultSkipped, fallbackMethod)
		return
	}

	desc := buildDescription(rec.Description, method, len(paths))
	if err := r.rules.UpdatePathPattern(ctx, rec.ID, pattern, desc); err != nil {
		rlog.WithFields(map[string]interface{}{
			"pattern": pattern,
			"method":  method,
		}).WithError(err).Warn("update path_pattern failed")
		r.metrics.RecordAttempt(ResultError, method)
		return
	}

	// 2) cache flush — 다음 lookup 부터 갱신된 rule 적용.
	r.resolver.Invalidate(rec.HostPattern, rec.TargetType)

	// 3) sample purge — 다음 cycle 에서 재누적 (다른 path 패턴 발견 가능성 대비).
	if err := r.samples.Purge(ctx, rec.ID); err != nil {
		rlog.WithError(err).Warn("sample purge failed (non-fatal — next cycle will re-evaluate)")
	}

	rlog.WithFields(map[string]interface{}{
		"pattern":      pattern,
		"method":       method,
		"sample_count": len(paths),
	}).Info("path_pattern refined")
	r.metrics.RecordAttempt(ResultSuccess, method)
}

// inferPattern 은 algorithm 우선 → LLM fallback hybrid 흐름을 수행합니다.
//
// 반환값: (pattern, method, err)
//   - 정상 추론 성공  : (regex, "algorithm" 또는 "llm", nil)
//   - 추론 거부       : ("", "", nil) — algorithm 휴리스틱 실패 + LLM 검증 거부 (호출자가 Debug 로그)
//   - LLM 호출 에러   : ("", "", err) — network / API 에러 (호출자가 Warn 로그, 다음 cycle 재시도)
//
// PR #191 gemini 피드백: LLM 에러를 삼키지 않고 호출자에게 전달 — 운영자가 LLM 장애 / 할당량
// 초과 등을 로그로 즉시 인지 가능.
//
// PR #191 후속 (metrics): LLM Generate 호출 1건이 발생할 때 metrics.RecordLLMCall 호출 —
// success / error 라벨로 운영자가 호출 빈도 + 실패율 추적 가능. metrics nil 허용.
func inferPattern(ctx context.Context, paths []string, llm pathinfer.LLMClient, minSamples int, metrics *Metrics) (string, string, error) {
	opt := pathinfer.WithMinSamples(minSamples)

	if pattern, ok := pathinfer.InferHeuristic(pathinfer.PathSamples{Articles: paths}, opt); ok {
		return pattern, MethodAlgorithm, nil
	}

	if llm == nil {
		return "", "", nil
	}

	pattern, ok, err := pathinfer.InferLLM(ctx, pathinfer.LLMSamples{Articles: paths}, llm, opt)
	if err != nil {
		metrics.RecordLLMCall(LLMStatusError)
		return "", "", err
	}
	metrics.RecordLLMCall(LLMStatusSuccess)
	if !ok || pattern == "" {
		return "", "", nil
	}
	return pattern, MethodLLM, nil
}

// extractPaths 는 SampleURL 슬라이스의 URL 들에서 path 부분만 추출합니다.
// URL parse 실패한 항목은 skip — 호출자에서 길이로 부족 여부 판단.
func extractPaths(samples []*storage.SampleURL) []string {
	out := make([]string, 0, len(samples))
	for _, s := range samples {
		u, err := url.Parse(s.URL)
		if err != nil {
			continue
		}
		path := u.Path
		if path == "" {
			path = "/"
		}
		out = append(out, path)
	}
	return out
}

// buildDescription 은 정밀화 결과를 description 에 누적 기록합니다.
// 형식: "<기존 description> | refined(method=<m>, samples=<n>, at=<RFC3339>)"
//
// 기존 description 이 비어있으면 "refined(...)" 만.
// 정밀화 누적 시 history 형태로 보존 (운영자가 어떤 방식으로 몇 번 갱신됐는지 추적).
func buildDescription(existing, method string, sampleCount int) string {
	tag := fmt.Sprintf("refined(method=%s, samples=%d, at=%s)", method, sampleCount, time.Now().UTC().Format(time.RFC3339))
	existing = strings.TrimSpace(existing)
	if existing == "" {
		return tag
	}
	return existing + " | " + tag
}
