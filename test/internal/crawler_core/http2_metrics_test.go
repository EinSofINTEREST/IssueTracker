package core_test

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	core "issuetracker/internal/crawler/core"
)

// TestHTTP2ErrorCounter_Increment_StartsFromOne:
// 새 errType 의 첫 Increment 는 1 을 반환해야 함.
func TestHTTP2ErrorCounter_Increment_StartsFromOne(t *testing.T) {
	c := core.NewHTTP2ErrorCounter()
	got := c.Increment("stream_error_received_data_after_end_stream")
	assert.Equal(t, uint64(1), got)
}

// TestHTTP2ErrorCounter_Increment_AccumulatesPerErrType:
// 동일 errType 반복 호출 시 카운트가 누적되어야 함.
func TestHTTP2ErrorCounter_Increment_AccumulatesPerErrType(t *testing.T) {
	c := core.NewHTTP2ErrorCounter()
	for i := uint64(1); i <= 10; i++ {
		got := c.Increment("conn_error_protocol")
		assert.Equal(t, i, got)
	}
}

// TestHTTP2ErrorCounter_Increment_SeparatesErrTypes:
// 서로 다른 errType 은 독립적으로 카운트되어야 함.
func TestHTTP2ErrorCounter_Increment_SeparatesErrTypes(t *testing.T) {
	c := core.NewHTTP2ErrorCounter()

	c.Increment("type_a")
	c.Increment("type_a")
	c.Increment("type_b")

	snap := c.Snapshot()
	assert.Equal(t, uint64(2), snap["type_a"])
	assert.Equal(t, uint64(1), snap["type_b"])
}

// TestHTTP2ErrorCounter_Snapshot_EmptyCounter:
// 한 번도 Increment 안 된 카운터의 Snapshot 은 빈 map 이어야 함 (nil 아님).
func TestHTTP2ErrorCounter_Snapshot_EmptyCounter(t *testing.T) {
	c := core.NewHTTP2ErrorCounter()
	snap := c.Snapshot()
	require.NotNil(t, snap)
	assert.Empty(t, snap)
}

// TestHTTP2ErrorCounter_Snapshot_IndependentFromInternal:
// 반환된 snapshot map 을 수정해도 카운터 내부 상태에 영향을 주지 않아야 함.
func TestHTTP2ErrorCounter_Snapshot_IndependentFromInternal(t *testing.T) {
	c := core.NewHTTP2ErrorCounter()
	c.Increment("foo")

	snap := c.Snapshot()
	snap["foo"] = 999
	snap["new_key"] = 1

	snap2 := c.Snapshot()
	assert.Equal(t, uint64(1), snap2["foo"], "snapshot 수정이 카운터에 반영되면 안 됨")
	assert.NotContains(t, snap2, "new_key", "snapshot 에 추가한 키가 카운터에 반영되면 안 됨")
}

// TestHTTP2ErrorCounter_Concurrent_SafeIncrement:
// 다중 goroutine 동시 Increment 가 race 없이 정확한 합계를 보장해야 함.
// `-race` 플래그로 race detector 검증.
func TestHTTP2ErrorCounter_Concurrent_SafeIncrement(t *testing.T) {
	c := core.NewHTTP2ErrorCounter()

	const goroutines = 50
	const incrementsPerGoroutine = 100

	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < incrementsPerGoroutine; j++ {
				c.Increment("concurrent_test")
			}
		}()
	}
	wg.Wait()

	snap := c.Snapshot()
	assert.Equal(t, uint64(goroutines*incrementsPerGoroutine), snap["concurrent_test"],
		"동시 Increment 의 총합이 정확해야 함 (race 없음)")
}

// TestHTTP2ErrorCounter_SortedSnapshot_Deterministic:
// SortedSnapshot 은 errType 사전순으로 결정적 순서를 반환해야 함.
func TestHTTP2ErrorCounter_SortedSnapshot_Deterministic(t *testing.T) {
	c := core.NewHTTP2ErrorCounter()
	// 의도적으로 사전순과 다른 입력 순서
	c.Increment("zebra")
	c.Increment("alpha")
	c.Increment("alpha")
	c.Increment("middle")

	got := c.SortedSnapshot()
	require.Len(t, got, 3)
	assert.Equal(t, "alpha", got[0].ErrorType)
	assert.Equal(t, uint64(2), got[0].Count)
	assert.Equal(t, "middle", got[1].ErrorType)
	assert.Equal(t, uint64(1), got[1].Count)
	assert.Equal(t, "zebra", got[2].ErrorType)
	assert.Equal(t, uint64(1), got[2].Count)
}

// TestDefaultHTTP2ErrorCounter_NotNil:
// 패키지 전역 기본 카운터가 초기화되어 있어야 함 (nil dereference 방지).
func TestDefaultHTTP2ErrorCounter_NotNil(t *testing.T) {
	require.NotNil(t, core.DefaultHTTP2ErrorCounter)
	// 기본 카운터에 카운트를 추가해도 다른 테스트에 영향 없도록 readonly 체크만
	snap := core.DefaultHTTP2ErrorCounter.Snapshot()
	assert.NotNil(t, snap)
}
