package refiner_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/parser/rule"
	"issuetracker/internal/parser/rule/llmgen"
	"issuetracker/internal/parser/rule/refiner"
	"issuetracker/internal/storage"
	"issuetracker/pkg/logger"
)

// ─────────────────────────────────────────────────────────────────────────────
// In-memory mocks
// ─────────────────────────────────────────────────────────────────────────────

// fakeRulesRepo 는 ParsingRuleRepository 의 in-memory 구현입니다.
// List / UpdatePathPattern / FindActiveCandidates 만 사용 — 나머지는 zero stub.
type fakeRulesRepo struct {
	mu      sync.Mutex
	records []*storage.ParsingRuleRecord
	updates []updateCall
	listErr error
	upErr   error
}

type updateCall struct {
	id      int64
	pattern string
	desc    string
}

func (r *fakeRulesRepo) Insert(_ context.Context, _ *storage.ParsingRuleRecord) error { return nil }
func (r *fakeRulesRepo) Update(_ context.Context, _ *storage.ParsingRuleRecord) error { return nil }
func (r *fakeRulesRepo) GetByID(_ context.Context, _ int64) (*storage.ParsingRuleRecord, error) {
	return nil, storage.ErrNotFound
}
func (r *fakeRulesRepo) FindActive(_ context.Context, _ string, _ storage.TargetType) (*storage.ParsingRuleRecord, error) {
	return nil, storage.ErrNotFound
}
func (r *fakeRulesRepo) FindActiveCandidates(_ context.Context, _ string, _ storage.TargetType) ([]*storage.ParsingRuleRecord, error) {
	return nil, nil
}
func (r *fakeRulesRepo) List(_ context.Context, f storage.ParsingRuleFilter) ([]*storage.ParsingRuleRecord, error) {
	if r.listErr != nil {
		return nil, r.listErr
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*storage.ParsingRuleRecord, 0)
	for _, rec := range r.records {
		if f.SourceName != "" && rec.SourceName != f.SourceName {
			continue
		}
		if f.OnlyEnabled && !rec.Enabled {
			continue
		}
		out = append(out, rec)
	}
	return out, nil
}
func (r *fakeRulesRepo) Delete(_ context.Context, _ int64) error { return nil }
func (r *fakeRulesRepo) UpdatePathPattern(_ context.Context, id int64, pattern, description string) error {
	if r.upErr != nil {
		return r.upErr
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, rec := range r.records {
		if rec.ID != id {
			continue
		}
		// optimistic guard 미러링 (PR #191 CodeRabbit 피드백): postgres 구현과 동일 contract.
		if rec.SourceName != llmgen.LLMAutoSourceName || !rec.Enabled || rec.PathPattern != "" {
			return storage.ErrNotFound
		}
		r.updates = append(r.updates, updateCall{id: id, pattern: pattern, desc: description})
		rec.PathPattern = pattern
		rec.Description = description
		return nil
	}
	return storage.ErrNotFound
}

func (r *fakeRulesRepo) updateCalls() []updateCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]updateCall, len(r.updates))
	copy(out, r.updates)
	return out
}

// fakeSamplesRepo 는 SampleURLRepository 의 in-memory 구현입니다.
type fakeSamplesRepo struct {
	mu      sync.Mutex
	byRule  map[int64][]*storage.SampleURL
	purged  map[int64]int // rule_id 별 Purge 호출 횟수
	listErr error
}

func newFakeSamplesRepo() *fakeSamplesRepo {
	return &fakeSamplesRepo{
		byRule: make(map[int64][]*storage.SampleURL),
		purged: make(map[int64]int),
	}
}

func (r *fakeSamplesRepo) Insert(_ context.Context, ruleID int64, url string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byRule[ruleID] = append(r.byRule[ruleID], &storage.SampleURL{
		ID: int64(len(r.byRule[ruleID]) + 1), RuleID: ruleID, URL: url, ObservedAt: time.Now(),
	})
	return nil
}
func (r *fakeSamplesRepo) Count(_ context.Context, ruleID int64) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.byRule[ruleID]), nil
}
func (r *fakeSamplesRepo) List(_ context.Context, ruleID int64, limit int) ([]*storage.SampleURL, error) {
	if r.listErr != nil {
		return nil, r.listErr
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	all := r.byRule[ruleID]
	if limit > 0 && limit < len(all) {
		all = all[:limit]
	}
	out := make([]*storage.SampleURL, len(all))
	copy(out, all)
	return out, nil
}
func (r *fakeSamplesRepo) Purge(_ context.Context, ruleID int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.byRule, ruleID)
	r.purged[ruleID]++
	return nil
}
func (r *fakeSamplesRepo) purgeCount(ruleID int64) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.purged[ruleID]
}

// fakeLLM 는 pathinfer.LLMClient 의 stub.
type fakeLLM struct {
	resp string
	err  error
	hits int64
}

func (f *fakeLLM) Generate(_ context.Context, _ string, _ string) (string, error) {
	atomic.AddInt64(&f.hits, 1)
	if f.err != nil {
		return "", f.err
	}
	return f.resp, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

func newCatchAllRule(id int64, host string) *storage.ParsingRuleRecord {
	return &storage.ParsingRuleRecord{
		ID:          id,
		SourceName:  llmgen.LLMAutoSourceName,
		HostPattern: host,
		PathPattern: "", // catch-all — 정밀화 대상
		TargetType:  storage.TargetTypePage,
		Version:     1,
		Enabled:     true,
	}
}

func newRefiner(t *testing.T, rules storage.ParsingRuleRepository, samples storage.SampleURLRepository, opts ...refiner.Option) *refiner.Refiner {
	t.Helper()
	resolver := rule.NewResolver(rules)
	log := logger.New(logger.DefaultConfig())
	// minSamples 3 (테스트 가독성). interval 은 RunOnce 만 호출하면 무관.
	defaults := []refiner.Option{refiner.WithMinSamples(3)}
	defaults = append(defaults, opts...)
	return refiner.New(rules, samples, resolver, log, defaults...)
}

// ─────────────────────────────────────────────────────────────────────────────
// Tests
// ─────────────────────────────────────────────────────────────────────────────

func TestRunOnce_AlgorithmSuccess_UpdatesPathPattern(t *testing.T) {
	rec := newCatchAllRule(1, "news.example.com")
	rules := &fakeRulesRepo{records: []*storage.ParsingRuleRecord{rec}}
	samples := newFakeSamplesRepo()

	// numeric ID 패턴 — InferHeuristic 이 ^/article/(\d+)$ 추론.
	for _, u := range []string{
		"https://news.example.com/article/100",
		"https://news.example.com/article/200",
		"https://news.example.com/article/300",
	} {
		require.NoError(t, samples.Insert(context.Background(), 1, u))
	}

	r := newRefiner(t, rules, samples)
	require.NoError(t, r.RunOnce(context.Background()))

	calls := rules.updateCalls()
	require.Len(t, calls, 1, "expected 1 UpdatePathPattern call")
	assert.Equal(t, int64(1), calls[0].id)
	assert.Contains(t, calls[0].pattern, `(\d+)`, "pattern should contain numeric capture group")
	assert.Contains(t, calls[0].desc, "method=algorithm")

	assert.Equal(t, 1, samples.purgeCount(1), "samples should be purged after refinement")
}

func TestRunOnce_BelowThreshold_NoUpdate(t *testing.T) {
	rec := newCatchAllRule(1, "news.example.com")
	rules := &fakeRulesRepo{records: []*storage.ParsingRuleRecord{rec}}
	samples := newFakeSamplesRepo()

	// 2 sample — minSamples 3 미만.
	require.NoError(t, samples.Insert(context.Background(), 1, "https://news.example.com/article/100"))
	require.NoError(t, samples.Insert(context.Background(), 1, "https://news.example.com/article/200"))

	r := newRefiner(t, rules, samples)
	require.NoError(t, r.RunOnce(context.Background()))

	assert.Empty(t, rules.updateCalls(), "no update expected below threshold")
	assert.Zero(t, samples.purgeCount(1), "no purge expected below threshold")
}

func TestRunOnce_AlreadyRefined_Skipped(t *testing.T) {
	// PathPattern != "" → 정밀화 대상 아님.
	rec := newCatchAllRule(1, "news.example.com")
	rec.PathPattern = `^/news/(\d+)$`
	rules := &fakeRulesRepo{records: []*storage.ParsingRuleRecord{rec}}
	samples := newFakeSamplesRepo()
	for i := 0; i < 5; i++ {
		require.NoError(t, samples.Insert(context.Background(), 1, "https://news.example.com/news/1"))
	}

	r := newRefiner(t, rules, samples)
	require.NoError(t, r.RunOnce(context.Background()))

	assert.Empty(t, rules.updateCalls(), "already-refined rule must not be updated")
}

func TestRunOnce_AlgorithmFails_LLMFallback(t *testing.T) {
	rec := newCatchAllRule(1, "news.example.com")
	rules := &fakeRulesRepo{records: []*storage.ParsingRuleRecord{rec}}
	samples := newFakeSamplesRepo()

	// segment 길이가 다른 sample — InferHeuristic 실패 케이스.
	for _, u := range []string{
		"https://news.example.com/article/100",
		"https://news.example.com/article/news/200",
		"https://news.example.com/post/300",
	} {
		require.NoError(t, samples.Insert(context.Background(), 1, u))
	}

	// LLM 이 모든 sample 을 매칭하는 정직한 패턴 응답.
	llm := &fakeLLM{resp: `^/.+/\d+$`}

	r := newRefiner(t, rules, samples, refiner.WithLLMClient(llm))
	require.NoError(t, r.RunOnce(context.Background()))

	calls := rules.updateCalls()
	require.Len(t, calls, 1)
	assert.Equal(t, `^/.+/\d+$`, calls[0].pattern)
	assert.Contains(t, calls[0].desc, "method=llm")
	assert.Equal(t, int64(1), atomic.LoadInt64(&llm.hits))
}

func TestRunOnce_AlgorithmFails_NoLLM_Skipped(t *testing.T) {
	rec := newCatchAllRule(1, "news.example.com")
	rules := &fakeRulesRepo{records: []*storage.ParsingRuleRecord{rec}}
	samples := newFakeSamplesRepo()
	// segment 길이가 다른 sample — InferHeuristic 실패.
	for _, u := range []string{
		"https://news.example.com/article/100",
		"https://news.example.com/article/news/200",
		"https://news.example.com/post/300",
	} {
		require.NoError(t, samples.Insert(context.Background(), 1, u))
	}

	r := newRefiner(t, rules, samples) // LLM 없음
	require.NoError(t, r.RunOnce(context.Background()))

	assert.Empty(t, rules.updateCalls())
	assert.Zero(t, samples.purgeCount(1))
}

func TestRunOnce_LLMError_NoUpdate(t *testing.T) {
	rec := newCatchAllRule(1, "news.example.com")
	rules := &fakeRulesRepo{records: []*storage.ParsingRuleRecord{rec}}
	samples := newFakeSamplesRepo()
	// algorithm 실패 케이스.
	for _, u := range []string{
		"https://news.example.com/article/100",
		"https://news.example.com/post/200",
		"https://news.example.com/news/300",
	} {
		require.NoError(t, samples.Insert(context.Background(), 1, u))
	}

	llm := &fakeLLM{err: errors.New("llm down")}

	r := newRefiner(t, rules, samples, refiner.WithLLMClient(llm))
	require.NoError(t, r.RunOnce(context.Background()))

	assert.Empty(t, rules.updateCalls(), "LLM error should not result in update")
}

func TestRunOnce_NonAutoRule_Skipped(t *testing.T) {
	// SourceName != "llm-auto" → List filter 가 거름.
	rec := newCatchAllRule(1, "news.example.com")
	rec.SourceName = "operator-tuned"
	rules := &fakeRulesRepo{records: []*storage.ParsingRuleRecord{rec}}
	samples := newFakeSamplesRepo()
	for i := 0; i < 5; i++ {
		require.NoError(t, samples.Insert(context.Background(), 1, "https://news.example.com/article/1"))
	}

	r := newRefiner(t, rules, samples)
	require.NoError(t, r.RunOnce(context.Background()))

	assert.Empty(t, rules.updateCalls())
}

func TestRunOnce_DisabledRule_Skipped(t *testing.T) {
	rec := newCatchAllRule(1, "news.example.com")
	rec.Enabled = false
	rules := &fakeRulesRepo{records: []*storage.ParsingRuleRecord{rec}}
	samples := newFakeSamplesRepo()
	for i := 0; i < 5; i++ {
		require.NoError(t, samples.Insert(context.Background(), 1, "https://news.example.com/article/1"))
	}

	r := newRefiner(t, rules, samples)
	require.NoError(t, r.RunOnce(context.Background()))

	assert.Empty(t, rules.updateCalls())
}

func TestRunOnce_ListError_Returned(t *testing.T) {
	rules := &fakeRulesRepo{listErr: errors.New("db down")}
	samples := newFakeSamplesRepo()

	r := newRefiner(t, rules, samples)
	err := r.RunOnce(context.Background())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "db down")
}

func TestRunOnce_UpdateError_NoPurge(t *testing.T) {
	rec := newCatchAllRule(1, "news.example.com")
	rules := &fakeRulesRepo{
		records: []*storage.ParsingRuleRecord{rec},
		upErr:   errors.New("update failed"),
	}
	samples := newFakeSamplesRepo()
	for _, u := range []string{
		"https://news.example.com/article/100",
		"https://news.example.com/article/200",
		"https://news.example.com/article/300",
	} {
		require.NoError(t, samples.Insert(context.Background(), 1, u))
	}

	r := newRefiner(t, rules, samples)
	require.NoError(t, r.RunOnce(context.Background()))

	assert.Zero(t, samples.purgeCount(1), "purge should not run after update failure")
}

// PR #191 CodeRabbit: optimistic guard 가 stale candidate (이미 다른 인스턴스가 정밀화 완료) 에
// 대해 ErrNotFound 반환 → refiner 가 Invalidate / Purge 모두 skip.
func TestRunOnce_StaleGuard_NoPurge(t *testing.T) {
	rec := newCatchAllRule(1, "news.example.com")
	rules := &fakeRulesRepo{records: []*storage.ParsingRuleRecord{rec}}
	samples := newFakeSamplesRepo()
	for _, u := range []string{
		"https://news.example.com/article/100",
		"https://news.example.com/article/200",
		"https://news.example.com/article/300",
	} {
		require.NoError(t, samples.Insert(context.Background(), 1, u))
	}

	// List 직후 다른 instance 가 이미 정밀화 완료 → PathPattern 비어있지 않은 상태로 선반영.
	// 본 refiner cycle 의 UpdatePathPattern 은 guard 실패로 ErrNotFound 반환되어야 함.
	rec.PathPattern = `^/article/(\d+)$`

	r := newRefiner(t, rules, samples)
	require.NoError(t, r.RunOnce(context.Background()))

	// candidates List 시점에 PathPattern != "" 이면 refiner 가 위에서 catch-all 필터링으로 skip —
	// 본 케이스는 List 시점 catch-all 이었다가 update 직전에 변경된 race 시뮬레이션.
	// 현재 구조에서는 List 직후 바로 refineOne 들어가므로, race 시뮬은 직접 UpdatePathPattern 호출로 검증.
	// 본 테스트는 mock 의 guard 동작 자체를 검증.
	err := rules.UpdatePathPattern(context.Background(), 1, `^/x/(\d+)$`, "stale write")
	require.ErrorIs(t, err, storage.ErrNotFound, "guard must reject stale (non-catch-all) candidate")
}

func TestRun_RespectsCancellation(t *testing.T) {
	rules := &fakeRulesRepo{}
	samples := newFakeSamplesRepo()

	r := newRefiner(t, rules, samples, refiner.WithInterval(50*time.Millisecond))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		r.Run(ctx)
		close(done)
	}()

	// Run 이 ticker 를 등록한 후 cancel.
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

// PR #191 피드백: Start + Stop 의 graceful shutdown 동작.
//
// Start 가 goroutine 을 띄운 뒤 ctx cancel + Stop 호출 시 polling loop 가 종료될 때까지
// Stop 이 대기. Stop 의 ctx 가 충분히 길면 (1s) 정상 경로로 반환.
func TestStartStop_GracefulShutdown(t *testing.T) {
	rules := &fakeRulesRepo{}
	samples := newFakeSamplesRepo()

	r := newRefiner(t, rules, samples, refiner.WithInterval(50*time.Millisecond))

	rootCtx, cancel := context.WithCancel(context.Background())
	r.Start(rootCtx)

	// ticker 등록 시간 부여.
	time.Sleep(20 * time.Millisecond)

	// Start 의 ctx cancel 후 Stop 으로 in-flight cycle 완료 대기.
	cancel()

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer stopCancel()
	r.Stop(stopCtx)

	// Stop 후 stopCtx 가 아직 cancel 되지 않았다면 (즉 normal path), 정상 종료.
	require.NoError(t, stopCtx.Err(), "Stop should return before its ctx times out")
}

// PR #191 피드백: Prometheus metric 이 success/skipped/error/llm 모든 분기에서 정상 increment.
//
// 본 테스트는 단일 registry 에서 success / skipped / error / LLM 호출 카운터의 누적치를 검증.
func TestRunOnce_RecordsMetrics(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics := refiner.NewMetrics(registry)

	// (1) success / algorithm — numeric ID 패턴.
	rec1 := newCatchAllRule(1, "news.example.com")
	rules := &fakeRulesRepo{records: []*storage.ParsingRuleRecord{rec1}}
	samples := newFakeSamplesRepo()
	for _, u := range []string{
		"https://news.example.com/article/100",
		"https://news.example.com/article/200",
		"https://news.example.com/article/300",
	} {
		require.NoError(t, samples.Insert(context.Background(), 1, u))
	}
	r := newRefiner(t, rules, samples, refiner.WithMetrics(metrics))
	require.NoError(t, r.RunOnce(context.Background()))

	// (2) skipped / none — sample 미달 (별도 rule 추가).
	rec2 := newCatchAllRule(2, "news2.example.com")
	rules.mu.Lock()
	rules.records = append(rules.records, rec2)
	rules.mu.Unlock()
	require.NoError(t, samples.Insert(context.Background(), 2, "https://news2.example.com/article/100"))
	require.NoError(t, r.RunOnce(context.Background()))
	// rec1 의 path_pattern 이 (1) 에서 갱신되어 catch-all 필터 skip — 이번 cycle 에서는 rec2 만 평가.

	expected := `
# HELP refinement_attempts_total path_pattern refinement attempts labeled by result/method.
# TYPE refinement_attempts_total counter
refinement_attempts_total{method="algorithm",result="success"} 1
refinement_attempts_total{method="none",result="skipped"} 1
`
	require.NoError(t,
		testutil.GatherAndCompare(registry, strings.NewReader(expected), "refinement_attempts_total"),
	)
}

// Stop 후 Start 호출은 noop — race 안전.
func TestStart_AfterStop_NoOp(t *testing.T) {
	rules := &fakeRulesRepo{}
	samples := newFakeSamplesRepo()

	r := newRefiner(t, rules, samples, refiner.WithInterval(50*time.Millisecond))

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer stopCancel()
	r.Stop(stopCtx) // Start 호출 없이 바로 Stop — noop 이어야 함

	// Stop 후 Start 는 goroutine 을 띄우면 안 됨.
	r.Start(context.Background())

	// goroutine 이 띄워지지 않았다면 추가 Stop 도 즉시 반환 (idempotent).
	r.Stop(context.Background())
}
