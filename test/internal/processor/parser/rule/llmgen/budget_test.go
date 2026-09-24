// CallBudget 동작 검증 (이슈 #169).
//
// 이 가드는 비용을 막으려고 LLM 호출을 건너뛰는데, 조건이 잘못되면 두 방향으로 조용히
// 실패한다 — 너무 안 막아 무료 한도를 넘기거나, 너무 막아 룰 학습이 사실상 멈춘다.
package llmgen_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/processor/parser/rule/llmgen"
)

// stubCounter 는 창별 카운트를 메모리에 두는 BudgetCounter 입니다.
type stubCounter struct {
	mu     sync.Mutex
	counts map[string]int
	err    error
	calls  int32
}

func newStubCounter() *stubCounter {
	return &stubCounter{counts: map[string]int{}}
}

func (c *stubCounter) Record(_ context.Context, window string, _ time.Duration) (int, error) {
	atomic.AddInt32(&c.calls, 1)
	if c.err != nil {
		return 0, c.err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.counts[window]++
	return c.counts[window], nil
}

func (c *stubCounter) Count(_ context.Context, window string, _ time.Duration) (int, error) {
	atomic.AddInt32(&c.calls, 1)
	if c.err != nil {
		return 0, c.err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.counts[window], nil
}

func TestCallBudget_Disabled_ReturnsNil(t *testing.T) {
	b := llmgen.NewCallBudget(llmgen.BudgetConfig{}, nil, nil, nil)
	assert.Nil(t, b, "상한이 없으면 가드 자체를 만들지 않는다")

	// nil 가드는 항상 통과해야 한다.
	ok, window := b.Allow(context.Background())
	assert.True(t, ok)
	assert.Empty(t, window)
}

func TestCallBudget_HourlyCap_BlocksAfterThreshold(t *testing.T) {
	b := llmgen.NewCallBudget(llmgen.BudgetConfig{HourlyCap: 3}, newStubCounter(), nil, nil)
	require.NotNil(t, b)

	for i := 0; i < 3; i++ {
		ok, _ := b.Allow(context.Background())
		require.True(t, ok, "한도 이내 %d 번째는 통과", i+1)
	}

	ok, window := b.Allow(context.Background())
	assert.False(t, ok)
	assert.Equal(t, llmgen.BudgetWindowHourly, window)
}

func TestCallBudget_DailyCap_BlocksAfterThreshold(t *testing.T) {
	b := llmgen.NewCallBudget(llmgen.BudgetConfig{DailyCap: 2}, newStubCounter(), nil, nil)

	for i := 0; i < 2; i++ {
		ok, _ := b.Allow(context.Background())
		require.True(t, ok)
	}
	ok, window := b.Allow(context.Background())
	assert.False(t, ok)
	assert.Equal(t, llmgen.BudgetWindowDaily, window)
}

// TestCallBudget_HourlyBlocksBeforeDaily 는 더 촘촘한 창이 먼저 걸리는지 확인합니다.
func TestCallBudget_HourlyBlocksBeforeDaily(t *testing.T) {
	b := llmgen.NewCallBudget(llmgen.BudgetConfig{DailyCap: 100, HourlyCap: 2}, newStubCounter(), nil, nil)

	for i := 0; i < 2; i++ {
		ok, _ := b.Allow(context.Background())
		require.True(t, ok)
	}
	ok, window := b.Allow(context.Background())
	require.False(t, ok)
	assert.Equal(t, llmgen.BudgetWindowHourly, window, "시간당 한도가 먼저 걸려야 한다")
}

// TestCallBudget_CounterError_Allows 는 counter 장애가 룰 학습을 막지 않는지 확인합니다.
//
// Redis 장애로 LLM 호출을 멈추면 가드가 장애를 증폭시킨다.
func TestCallBudget_CounterError_Allows(t *testing.T) {
	c := newStubCounter()
	c.err = errors.New("redis down")
	b := llmgen.NewCallBudget(llmgen.BudgetConfig{HourlyCap: 1}, c, nil, nil)

	for i := 0; i < 5; i++ {
		ok, _ := b.Allow(context.Background())
		require.True(t, ok, "조회 실패 중에는 통과해야 한다")
	}
}

// TestCallBudget_InProcessFallback 은 counter 미주입 시 프로세스 로컬로 동작하는지
// 확인합니다 — Redis 부재 환경에서도 상한이 아예 사라지지는 않아야 합니다.
func TestCallBudget_InProcessFallback(t *testing.T) {
	b := llmgen.NewCallBudget(llmgen.BudgetConfig{HourlyCap: 3}, nil, nil, nil)
	require.NotNil(t, b)

	for i := 0; i < 3; i++ {
		ok, _ := b.Allow(context.Background())
		require.True(t, ok)
	}
	ok, window := b.Allow(context.Background())
	assert.False(t, ok, "counter 없이도 프로세스 로컬 상한이 동작해야 한다")
	assert.Equal(t, llmgen.BudgetWindowHourly, window)
}

// TestCallBudget_Concurrent_InProcess_DoesNotExceedGrossly 는 동시 호출에서 통과 수가
// 한도를 크게 벗어나지 않는지 확인합니다.
//
// 프로세스 로컬 경로는 count → record 가 두 개의 임계구역이라 엄밀한 상한이 아닙니다.
// 정확한 상한이 필요하면 Redis counter 를 주입해야 한다는 점을 테스트로 고정합니다.
func TestCallBudget_Concurrent_InProcess_DoesNotExceedGrossly(t *testing.T) {
	const cap = 10
	const goroutines = 40
	b := llmgen.NewCallBudget(llmgen.BudgetConfig{HourlyCap: cap}, nil, nil, nil)

	var allowed int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if ok, _ := b.Allow(context.Background()); ok {
				atomic.AddInt32(&allowed, 1)
			}
		}()
	}
	close(start)
	wg.Wait()

	got := int(atomic.LoadInt32(&allowed))
	assert.GreaterOrEqual(t, got, cap, "적어도 한도만큼은 통과해야 한다")
	assert.Less(t, got, goroutines, "상한이 전혀 작동하지 않으면 안 된다")
}
