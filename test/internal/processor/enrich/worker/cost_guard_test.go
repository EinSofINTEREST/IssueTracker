// CostGuard 동작 검증 (이슈 #456).
//
// 이 가드는 비용을 막으려고 enrichment 를 건너뛰는데, 조건이 잘못되면 두 방향으로 조용히
// 실패한다 — 너무 안 막아 비용이 새거나, 너무 막아 enrichment 가 사실상 꺼진다.
// 아래 테스트가 양쪽 경계를 고정한다.
package worker_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/processor/enrich/worker"
)

// stubBacklog 는 고정 lag 을 반환하는 BacklogChecker 입니다. 호출 횟수를 셉니다.
type stubBacklog struct {
	lag   int64
	err   error
	calls int32
}

func (s *stubBacklog) Backlog(_ context.Context, _ string, _ string) (int64, error) {
	atomic.AddInt32(&s.calls, 1)
	return atomic.LoadInt64(&s.lag), s.err
}

// setLag 는 테스트 도중 lag 을 바꿉니다 — 가드가 재조회할 때 새 값을 보도록.
func (s *stubBacklog) setLag(v int64) { atomic.StoreInt64(&s.lag, v) }

func (s *stubBacklog) Calls() int32 { return atomic.LoadInt32(&s.calls) }

func TestCostGuard_NilGuard_AlwaysAllows(t *testing.T) {
	var g *worker.CostGuard
	ok, reason := g.Allow(context.Background())
	assert.True(t, ok)
	assert.Equal(t, worker.SkipReasonNone, reason)
	assert.False(t, g.Enabled())
}

func TestCostGuard_NoLimits_AlwaysAllows(t *testing.T) {
	g := worker.NewCostGuard(worker.CostGuardConfig{}, nil, nil, nil)
	assert.False(t, g.Enabled(), "한도가 없으면 가드는 비활성")
	for i := 0; i < 100; i++ {
		ok, _ := g.Allow(context.Background())
		require.True(t, ok)
	}
}

func TestCostGuard_DailyLimit_BlocksAfterThreshold(t *testing.T) {
	g := worker.NewCostGuard(worker.CostGuardConfig{DailyCallLimit: 3}, nil, nil, nil)
	require.True(t, g.Enabled())

	for i := 0; i < 3; i++ {
		ok, reason := g.Allow(context.Background())
		require.True(t, ok, "한도 이내 %d 번째는 통과", i+1)
		require.Equal(t, worker.SkipReasonNone, reason)
	}

	ok, reason := g.Allow(context.Background())
	assert.False(t, ok, "한도 초과 후에는 차단")
	assert.Equal(t, worker.SkipReasonDailyBudget, reason)
}

// TestCostGuard_DailyLimit_Concurrent_DoesNotExceed 는 동시 호출에서 한도가 새지 않는지
// 확인합니다 — 판단과 카운터 증가가 분리돼 있으면 여기서 초과가 발생합니다.
func TestCostGuard_DailyLimit_Concurrent_DoesNotExceed(t *testing.T) {
	const limit = 10
	const goroutines = 50

	g := worker.NewCostGuard(worker.CostGuardConfig{DailyCallLimit: limit}, nil, nil, nil)

	var allowed int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if ok, _ := g.Allow(context.Background()); ok {
				atomic.AddInt32(&allowed, 1)
			}
		}()
	}
	close(start)
	wg.Wait()

	assert.Equal(t, int32(limit), atomic.LoadInt32(&allowed),
		"동시 호출이어도 통과 수가 한도를 넘으면 안 된다")
}

func TestCostGuard_Backlog_BlocksOverThreshold(t *testing.T) {
	checker := &stubBacklog{lag: 500}
	g := worker.NewCostGuard(worker.CostGuardConfig{
		MaxBacklog:   100,
		BacklogTopic: "t",
		BacklogGroup: "g",
	}, checker, nil, nil)

	ok, reason := g.Allow(context.Background())
	assert.False(t, ok)
	assert.Equal(t, worker.SkipReasonBacklog, reason)
}

func TestCostGuard_Backlog_AllowsUnderThreshold(t *testing.T) {
	checker := &stubBacklog{lag: 50}
	g := worker.NewCostGuard(worker.CostGuardConfig{
		MaxBacklog:   100,
		BacklogTopic: "t",
		BacklogGroup: "g",
	}, checker, nil, nil)

	ok, reason := g.Allow(context.Background())
	assert.True(t, ok)
	assert.Equal(t, worker.SkipReasonNone, reason)
}

// TestCostGuard_Backlog_CheckerError_Allows 는 lag 조회 실패가 enrichment 를 막지 않는지
// 확인합니다. Kafka 일시 장애로 enrichment 를 멈추면 가드가 장애를 증폭시킵니다.
func TestCostGuard_Backlog_CheckerError_Allows(t *testing.T) {
	checker := &stubBacklog{err: errors.New("kafka down")}
	g := worker.NewCostGuard(worker.CostGuardConfig{
		MaxBacklog:   1,
		BacklogTopic: "t",
		BacklogGroup: "g",
	}, checker, nil, nil)

	ok, reason := g.Allow(context.Background())
	assert.True(t, ok, "조회 실패는 통과로 처리")
	assert.Equal(t, worker.SkipReasonNone, reason)
}

// TestCostGuard_Backlog_ResultIsCached 는 매 메시지마다 Kafka 에 묻지 않는지 확인합니다.
func TestCostGuard_Backlog_ResultIsCached(t *testing.T) {
	checker := &stubBacklog{lag: 10}
	g := worker.NewCostGuard(worker.CostGuardConfig{
		MaxBacklog:           100,
		BacklogTopic:         "t",
		BacklogGroup:         "g",
		BacklogCheckInterval: time.Hour, // 테스트 동안 만료되지 않게
	}, checker, nil, nil)

	for i := 0; i < 20; i++ {
		ok, _ := g.Allow(context.Background())
		require.True(t, ok)
	}
	assert.Equal(t, int32(1), checker.Calls(), "캐시 수명 안에서는 1회만 조회")
}

// TestCostGuard_BacklogBlock_DoesNotConsumeDailyBudget 는 backlog 차단이 일일 카운터를
// 소모하지 않는지 확인합니다.
//
// 차단된 호출이 예산을 깎으면, backlog 가 해소된 뒤 쓸 수 있는 한도가 이미 줄어 있다.
// 같은 가드 인스턴스로 검증해야 의미가 있다 — 새 가드를 만들면 카운터가 당연히 0 이라
// 아무것도 증명하지 못한다.
func TestCostGuard_BacklogBlock_DoesNotConsumeDailyBudget(t *testing.T) {
	// 캐시를 사실상 끈다 — 매 호출마다 lag 을 다시 읽어 중간에 값을 바꿀 수 있게.
	checker := &stubBacklog{lag: 999}
	g := worker.NewCostGuard(worker.CostGuardConfig{
		DailyCallLimit:       5,
		MaxBacklog:           10,
		BacklogTopic:         "t",
		BacklogGroup:         "g",
		BacklogCheckInterval: time.Nanosecond,
	}, checker, nil, nil)

	for i := 0; i < 3; i++ {
		ok, reason := g.Allow(context.Background())
		require.False(t, ok)
		require.Equal(t, worker.SkipReasonBacklog, reason)
	}

	// backlog 해소 — 같은 가드가 한도 5 를 온전히 갖고 있어야 한다.
	checker.setLag(0)
	for i := 0; i < 5; i++ {
		ok, reason := g.Allow(context.Background())
		require.True(t, ok, "backlog 로 차단된 호출은 예산을 소모하지 않아야 한다 (%d 번째)", i+1)
		require.Equal(t, worker.SkipReasonNone, reason)
	}

	// 6 번째는 한도 초과로 막혀야 한다 — 앞의 5회가 정확히 예산을 다 썼다는 뜻.
	ok, reason := g.Allow(context.Background())
	assert.False(t, ok)
	assert.Equal(t, worker.SkipReasonDailyBudget, reason)
}

// stubZSetLen 은 고정 길이를 반환하는 ZSetLen 입니다.
type stubZSetLen struct {
	n   int64
	err error
}

func (s *stubZSetLen) Len(_ context.Context) (int64, error) { return s.n, s.err }

// TestZSetBacklogChecker_ReportsQueueLength 는 ZSET 모드에서 backlog 가 ZCARD 로
// 측정되는지 확인합니다 (이슈 #456).
//
// ZSET 모드는 intake 가 Kafka 를 즉시 commit 하므로 Kafka lag 이 0 에 가깝다.
// Kafka 기준으로 재면 ENRICH_MAX_BACKLOG 가 영영 발동하지 않는다.
func TestZSetBacklogChecker_ReportsQueueLength(t *testing.T) {
	c := worker.NewZSetBacklogChecker(&stubZSetLen{n: 4200})
	require.NotNil(t, c)

	lag, err := c.Backlog(context.Background(), "ignored-topic", "ignored-group")
	require.NoError(t, err)
	assert.Equal(t, int64(4200), lag)
}

func TestNewZSetBacklogChecker_NilQueue_ReturnsNil(t *testing.T) {
	assert.Nil(t, worker.NewZSetBacklogChecker(nil),
		"nil 큐면 호출자가 Kafka checker 로 fallback 할 수 있게 nil 을 반환")
}

// TestCostGuard_WithZSetChecker_BlocksOverThreshold 는 ZSET 길이로 차단이 동작하는지
// 확인합니다 — 체커 교체가 실제로 가드에 반영되는지.
func TestCostGuard_WithZSetChecker_BlocksOverThreshold(t *testing.T) {
	g := worker.NewCostGuard(worker.CostGuardConfig{
		MaxBacklog:           100,
		BacklogCheckInterval: time.Hour,
	}, worker.NewZSetBacklogChecker(&stubZSetLen{n: 500}), nil, nil)

	ok, reason := g.Allow(context.Background())
	assert.False(t, ok)
	assert.Equal(t, worker.SkipReasonBacklog, reason)
}

// TestCostGuard_BacklogCheckerError_IsCached 는 조회 실패도 캐시되는지 확인합니다
// (CodeRabbit 피드백).
//
// 실패를 캐시하지 않으면 Kafka 장애 동안 **모든 메시지** 가 새 RPC 를 띄우고 각자
// 타임아웃을 기다리며 WARN 을 남긴다 — 장애를 견디려는 가드가 파이프라인을 느리게 만든다.
func TestCostGuard_BacklogCheckerError_IsCached(t *testing.T) {
	checker := &stubBacklog{err: errors.New("kafka down")}
	g := worker.NewCostGuard(worker.CostGuardConfig{
		MaxBacklog:           10,
		BacklogTopic:         "t",
		BacklogGroup:         "g",
		BacklogCheckInterval: time.Hour,
	}, checker, nil, nil)

	for i := 0; i < 15; i++ {
		ok, _ := g.Allow(context.Background())
		require.True(t, ok, "조회 실패 중에도 통과해야 한다")
	}
	assert.Equal(t, int32(1), checker.Calls(),
		"실패도 캐시해야 장애 중 RPC 폭주를 막는다")
}
