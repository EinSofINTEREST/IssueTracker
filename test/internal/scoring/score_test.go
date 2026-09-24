package scoring_test

import (
	"context"
	"errors"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/scoring"
	"issuetracker/internal/storage"
	"issuetracker/internal/storage/model"
	"issuetracker/pkg/logger"
)

func sig(fresh, impact, trust float64, n int) model.HostSignals {
	return model.HostSignals{Freshness: fresh, Impact: impact, HostTrust: trust, SampleCount: n}
}

// TestScore_ColdStart_NoScore 는 표본이 적으면 점수를 내지 않는지 검증합니다.
//
// 표본이 적은 점수를 그대로 쓰면 노이즈가 우선순위에 반영된다 — 기사 2건으로 계산된
// host_trust 1.0 이 High 로 올라가는 식이다.
func TestScore_ColdStart_NoScore(t *testing.T) {
	_, ok := scoring.Score(sig(1, 1, 1, scoring.MinSampleCount-1), scoring.DefaultWeights)
	assert.False(t, ok, "표본 부족은 점수를 내지 않아야 한다")

	_, ok = scoring.Score(sig(1, 1, 1, scoring.MinSampleCount), scoring.DefaultWeights)
	assert.True(t, ok, "최소 표본을 채우면 점수가 나와야 한다")
}

// TestScore_Normalized 는 결과가 [0,1] 안에 들어오는지 검증합니다.
func TestScore_Normalized(t *testing.T) {
	tests := []struct {
		name string
		s    model.HostSignals
		want float64
	}{
		{"전부 1", sig(1, 1, 1, 100), 1},
		{"전부 0", sig(0, 0, 0, 100), 0},
		{"중립", sig(0.5, 0.5, 0.5, 100), 0.5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := scoring.Score(tt.s, scoring.DefaultWeights)
			require.True(t, ok)
			assert.InDelta(t, tt.want, got, 0.0001)
		})
	}
}

// TestScore_ClampsOutOfRangeInputs 는 범위를 벗어난 입력이 잘려 들어가는지 검증합니다.
//
// 정규화는 집계 쪽 책임이지만, 버그로 1 을 넘는 값이 와도 점수가 1 을 넘으면 안 된다 —
// threshold 비교가 무의미해진다.
func TestScore_ClampsOutOfRangeInputs(t *testing.T) {
	got, ok := scoring.Score(sig(5, 5, 5, 100), scoring.DefaultWeights)
	require.True(t, ok)
	assert.LessOrEqual(t, got, 1.0)

	got, ok = scoring.Score(sig(-3, -3, -3, 100), scoring.DefaultWeights)
	require.True(t, ok)
	assert.GreaterOrEqual(t, got, 0.0)
}

// TestScore_NaN_TreatedAsZero 는 NaN 이 0 으로 취급되는지 검증합니다.
//
// NaN 은 모든 비교에서 false 라 threshold 판정이 조용히 Normal 로 떨어진다.
// 명시적으로 0 을 주는 편이 추적하기 쉽다.
func TestScore_NaN_TreatedAsZero(t *testing.T) {
	got, ok := scoring.Score(sig(math.NaN(), 1, 1, 100), scoring.DefaultWeights)
	require.True(t, ok)
	assert.False(t, math.IsNaN(got), "점수에 NaN 이 새어나오면 안 된다")
	assert.Less(t, got, 1.0, "NaN 신호는 0 으로 취급돼 만점이 될 수 없다")
}

// TestScore_ZeroWeights_NoScore 는 가중치가 전부 0 이면 점수를 내지 않는지 검증합니다.
//
// 조용히 0 을 돌려주면 모든 host 가 Normal 로 고정돼 "켰는데 효과가 없다" 는 진단 불가
// 상태가 된다.
func TestScore_ZeroWeights_NoScore(t *testing.T) {
	_, ok := scoring.Score(sig(1, 1, 1, 100), scoring.Weights{})
	assert.False(t, ok)
}

// TestScore_TrustDominates 는 기본 가중치에서 host_trust 가 가장 큰 영향을 갖는지 검증합니다.
func TestScore_TrustDominates(t *testing.T) {
	trustOnly, ok := scoring.Score(sig(0, 0, 1, 100), scoring.DefaultWeights)
	require.True(t, ok)
	freshOnly, ok := scoring.Score(sig(1, 0, 0, 100), scoring.DefaultWeights)
	require.True(t, ok)
	impactOnly, ok := scoring.Score(sig(0, 1, 0, 100), scoring.DefaultWeights)
	require.True(t, ok)

	assert.Greater(t, trustOnly, freshOnly)
	assert.Greater(t, freshOnly, impactOnly)
}

// ── Scorer ──────────────────────────────────────────────────────────────────

type fakeAggregator struct {
	mu     sync.Mutex
	result []scoring.HostAggregate
	err    error
	calls  int
}

func (f *fakeAggregator) Aggregate(_ context.Context, _ int) ([]scoring.HostAggregate, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.result, f.err
}

func (f *fakeAggregator) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

type fakeRepo struct {
	mu       sync.Mutex
	upserted []*model.HostScoringState
	err      error
}

func (f *fakeRepo) Upsert(_ context.Context, s *model.HostScoringState) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.upserted = append(f.upserted, s)
	return nil
}

func (f *fakeRepo) Get(_ context.Context, _ string) (*model.HostScoringState, error) {
	return nil, storage.ErrNotFound
}

func (f *fakeRepo) ListAll(_ context.Context) ([]*model.HostScoringState, error) { return nil, nil }

func (f *fakeRepo) rows() []*model.HostScoringState {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*model.HostScoringState(nil), f.upserted...)
}

type fakeSink struct {
	mu     sync.Mutex
	scores map[string]float64
	calls  int
}

func (f *fakeSink) SetScores(s map[string]float64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scores = s
	f.calls++
}

func (f *fakeSink) snapshot() (map[string]float64, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.scores, f.calls
}

func newScorer(agg scoring.Aggregator, repo *fakeRepo, sink *fakeSink) *scoring.Scorer {
	return scoring.NewScorer(agg, repo, sink, scoring.Config{
		Interval:      time.Hour, // 주기 tick 은 테스트에서 쓰지 않는다 — Start 의 최초 1회만 본다
		WindowMinutes: 60,
		Weights:       scoring.DefaultWeights,
	}, logger.New(logger.DefaultConfig()))
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("조건이 시간 내에 만족되지 않음")
}

// TestScorer_Start_RunsImmediately 는 부팅 직후 1회를 먼저 수행하는지 검증합니다.
//
// 첫 주기를 기다리면 재시작할 때마다 Interval 만큼 기능이 꺼진 상태가 된다.
func TestScorer_Start_RunsImmediately(t *testing.T) {
	agg := &fakeAggregator{result: []scoring.HostAggregate{
		{Host: "a.example.com", Signals: sig(1, 1, 1, 100)},
	}}
	repo, sink := &fakeRepo{}, &fakeSink{}
	s := newScorer(agg, repo, sink)

	s.Start(t.Context())
	t.Cleanup(s.Stop)

	waitFor(t, func() bool { _, calls := sink.snapshot(); return calls > 0 })

	scores, _ := sink.snapshot()
	assert.InDelta(t, 1.0, scores["a.example.com"], 0.0001)
	assert.Len(t, repo.rows(), 1, "점수가 저장돼야 한다")
}

// TestScorer_AggregateFailure_KeepsPreviousSnapshot 은 집계 실패가 기존 스냅샷을
// 지우지 않는지 검증합니다.
//
// 실패 시 스냅샷을 비우면 DB 일시 장애가 곧바로 전체 host 의 우선순위 변동으로 번진다.
func TestScorer_AggregateFailure_KeepsPreviousSnapshot(t *testing.T) {
	agg := &fakeAggregator{err: errors.New("db down")}
	repo, sink := &fakeRepo{}, &fakeSink{}
	s := newScorer(agg, repo, sink)

	s.Start(t.Context())
	t.Cleanup(s.Stop)

	waitFor(t, func() bool { return agg.callCount() > 0 })
	time.Sleep(50 * time.Millisecond)

	_, calls := sink.snapshot()
	assert.Zero(t, calls, "집계 실패 시 SetScores 를 호출하면 안 된다")
}

// TestScorer_ColdStartHosts_Excluded 는 표본 부족 host 가 스냅샷에서 빠지는지 검증합니다.
//
// 빠지면 resolver 의 CanResolve 가 false 가 되어 chain 의 기존 경로에 위임된다.
func TestScorer_ColdStartHosts_Excluded(t *testing.T) {
	agg := &fakeAggregator{result: []scoring.HostAggregate{
		{Host: "ready.example.com", Signals: sig(1, 1, 1, 100)},
		{Host: "cold.example.com", Signals: sig(1, 1, 1, 3)},
	}}
	repo, sink := &fakeRepo{}, &fakeSink{}
	s := newScorer(agg, repo, sink)

	s.Start(t.Context())
	t.Cleanup(s.Stop)

	waitFor(t, func() bool { _, calls := sink.snapshot(); return calls > 0 })

	scores, _ := sink.snapshot()
	assert.Contains(t, scores, "ready.example.com")
	assert.NotContains(t, scores, "cold.example.com", "표본 부족 host 는 점수를 내지 않는다")

	for _, r := range repo.rows() {
		assert.NotEqual(t, "cold.example.com", r.Host, "표본 부족 host 는 저장도 하지 않는다")
	}
}

// TestScorer_UpsertFailure_StillPublishesSnapshot 은 저장 실패가 스냅샷 공급을 막지
// 않는지 검증합니다.
//
// 점수 자체는 메모리에서 유효하고 다음 주기에 다시 저장을 시도한다. 저장 실패로 스냅샷까지
// 막으면 DB 쓰기 장애가 우선순위 기능 전체를 멈춘다.
func TestScorer_UpsertFailure_StillPublishesSnapshot(t *testing.T) {
	agg := &fakeAggregator{result: []scoring.HostAggregate{
		{Host: "a.example.com", Signals: sig(1, 1, 1, 100)},
	}}
	repo := &fakeRepo{err: errors.New("write failed")}
	sink := &fakeSink{}
	s := newScorer(agg, repo, sink)

	s.Start(t.Context())
	t.Cleanup(s.Stop)

	waitFor(t, func() bool { _, calls := sink.snapshot(); return calls > 0 })

	scores, _ := sink.snapshot()
	assert.Contains(t, scores, "a.example.com")
}

// TestScorer_Stop_Idempotent 는 Stop 중복 호출이 안전한지 검증합니다.
func TestScorer_Stop_Idempotent(t *testing.T) {
	agg := &fakeAggregator{}
	s := newScorer(agg, &fakeRepo{}, &fakeSink{})
	s.Start(t.Context())
	s.Stop()
	s.Stop()
}

// TestScorer_ZeroAggregateTimeout_UsesDefault 는 상한 미설정이 기능을 죽이지 않는지
// 검증합니다.
//
// 0 을 그대로 context.WithTimeout 에 넘기면 즉시 만료돼 **매 주기가 실패** 한다.
// 설정을 빠뜨렸을 때 조용히 죽는 대신 기본값으로 동작해야 한다.
func TestScorer_ZeroAggregateTimeout_UsesDefault(t *testing.T) {
	agg := &fakeAggregator{result: []scoring.HostAggregate{
		{Host: "a.example.com", Signals: sig(1, 1, 1, 100)},
	}}
	repo, sink := &fakeRepo{}, &fakeSink{}

	// AggregateTimeout 을 명시하지 않는다 (= 0).
	s := scoring.NewScorer(agg, repo, sink, scoring.Config{
		Interval:      time.Hour,
		WindowMinutes: 60,
		Weights:       scoring.DefaultWeights,
	}, logger.New(logger.DefaultConfig()))

	s.Start(t.Context())
	t.Cleanup(s.Stop)

	waitFor(t, func() bool { _, calls := sink.snapshot(); return calls > 0 })

	scores, _ := sink.snapshot()
	assert.Contains(t, scores, "a.example.com", "상한 미설정이 주기를 실패시키면 안 된다")
}
