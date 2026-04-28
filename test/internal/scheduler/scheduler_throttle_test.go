package scheduler_test

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/crawler/core"
	"issuetracker/internal/scheduler"
	"issuetracker/pkg/queue"
)

// ─────────────────────────────────────────────────────────────────────────────
// Mock Throttler
// ─────────────────────────────────────────────────────────────────────────────

// stubThrottler 는 항상 고정된 결정을 반환하고 호출 횟수를 기록합니다.
type stubThrottler struct {
	throttle bool
	calls    atomic.Int32
}

func (s *stubThrottler) ShouldThrottle(_ context.Context, _ *core.CrawlJob) bool {
	s.calls.Add(1)
	return s.throttle
}

// ─────────────────────────────────────────────────────────────────────────────
// 테스트
// ─────────────────────────────────────────────────────────────────────────────

// TestScheduler_Throttler_BlocksWhenThrottled:
// SetThrottler 로 항상 true 반환 throttler 를 설정하면 emit 이 호출되지 않아야 함.
// Throttler 호출 자체는 발생 (>=1) 했는지 확인하여 "스케줄러가 한 번도 안 돈 것"
// 과 구분.
func TestScheduler_Throttler_BlocksWhenThrottled(t *testing.T) {
	pub := &gateMockEmitter{}
	throttler := &stubThrottler{throttle: true}

	entry := scheduler.ScheduleEntry{
		CrawlerName: "cnn",
		URL:         "https://edition.cnn.com/health",
		TargetType:  core.TargetTypeCategory,
		Interval:    50 * time.Millisecond,
		Priority:    core.PriorityNormal,
		Timeout:     5 * time.Second,
	}

	sched := scheduler.New([]scheduler.ScheduleEntry{entry}, pub, gateTestLogger(), 3)
	sched.SetThrottler(throttler)

	ctx, cancel := context.WithCancel(context.Background())
	sched.Start(ctx)
	require.Eventually(t, func() bool { return throttler.calls.Load() >= 1 },
		time.Second, 10*time.Millisecond,
		"스케줄러가 publish 시도 시 throttler 가 1회 이상 호출되어야 함")
	cancel()
	sched.Stop()

	assert.Equal(t, 0, pub.callCount(), "throttle = true 결정 시 emit 호출 안 됨")
	assert.GreaterOrEqual(t, int(throttler.calls.Load()), 1, "throttler 가 호출되었음")
}

// TestScheduler_Throttler_AllowsWhenNotThrottled:
// throttle = false 결정 시 emit 정상 호출.
func TestScheduler_Throttler_AllowsWhenNotThrottled(t *testing.T) {
	pub := &gateMockEmitter{}
	throttler := &stubThrottler{throttle: false}

	entry := scheduler.ScheduleEntry{
		CrawlerName: "cnn",
		URL:         "https://edition.cnn.com/health",
		TargetType:  core.TargetTypeCategory,
		Interval:    50 * time.Millisecond,
		Priority:    core.PriorityNormal,
		Timeout:     5 * time.Second,
	}

	sched := scheduler.New([]scheduler.ScheduleEntry{entry}, pub, gateTestLogger(), 3)
	sched.SetThrottler(throttler)

	ctx, cancel := context.WithCancel(context.Background())
	sched.Start(ctx)
	require.Eventually(t, func() bool { return pub.callCount() >= 1 },
		time.Second, 10*time.Millisecond)
	cancel()
	sched.Stop()

	assert.GreaterOrEqual(t, int(throttler.calls.Load()), 1)
}

// TestScheduler_NoThrottler_LegacyBehavior:
// SetThrottler 미호출 시 모든 publish 진행 (기존 동작 보존).
func TestScheduler_NoThrottler_LegacyBehavior(t *testing.T) {
	pub := &gateMockEmitter{}

	entry := scheduler.ScheduleEntry{
		CrawlerName: "cnn",
		URL:         "https://edition.cnn.com/health",
		TargetType:  core.TargetTypeCategory,
		Interval:    50 * time.Millisecond,
		Priority:    core.PriorityNormal,
		Timeout:     5 * time.Second,
	}

	sched := scheduler.New([]scheduler.ScheduleEntry{entry}, pub, gateTestLogger(), 3)
	// SetThrottler 미호출 — throttle 비활성

	ctx, cancel := context.WithCancel(context.Background())
	sched.Start(ctx)
	require.Eventually(t, func() bool { return pub.callCount() >= 1 },
		time.Second, 10*time.Millisecond)
	cancel()
	sched.Stop()
}

// TestScheduler_Throttler_NilUnsetsPrevious:
// SetThrottler(nil) 호출 시 이전 throttler 가 제거되고 publish 가 정상 진행되어야 함.
func TestScheduler_Throttler_NilUnsetsPrevious(t *testing.T) {
	pub := &gateMockEmitter{}
	throttler := &stubThrottler{throttle: true}

	entry := scheduler.ScheduleEntry{
		CrawlerName: "cnn",
		URL:         "https://edition.cnn.com/health",
		TargetType:  core.TargetTypeCategory,
		Interval:    50 * time.Millisecond,
		Priority:    core.PriorityNormal,
		Timeout:     5 * time.Second,
	}

	sched := scheduler.New([]scheduler.ScheduleEntry{entry}, pub, gateTestLogger(), 3)
	sched.SetThrottler(throttler)
	sched.SetThrottler(nil) // 이전 throttler 해제

	ctx, cancel := context.WithCancel(context.Background())
	sched.Start(ctx)
	require.Eventually(t, func() bool { return pub.callCount() >= 1 },
		time.Second, 10*time.Millisecond,
		"SetThrottler(nil) 후 publish 진행되어야 함")
	cancel()
	sched.Stop()

	assert.Equal(t, int32(0), throttler.calls.Load(), "해제된 throttler 는 호출되지 않음")
}

// TestScheduler_BacklogThrottler_Integration:
// 실제 BacklogThrottler 와 mock BacklogChecker 를 결합하여 end-to-end 흐름 검증.
// lag > maxBacklog 시 emit 차단되고 WARN 로그가 발생함을 확인.
func TestScheduler_BacklogThrottler_Integration(t *testing.T) {
	pub := &gateMockEmitter{}
	checker := &mockBacklogChecker{lag: 5_000}
	logBuf := &safeBuffer{}

	throttler := scheduler.NewBacklogThrottler(checker, queue.GroupCrawlerWorkers, 100, 0, captureLogger(logBuf))

	entry := scheduler.ScheduleEntry{
		CrawlerName: "cnn",
		URL:         "https://edition.cnn.com/health",
		TargetType:  core.TargetTypeCategory,
		Interval:    50 * time.Millisecond,
		Priority:    core.PriorityNormal,
		Timeout:     5 * time.Second,
	}

	sched := scheduler.New([]scheduler.ScheduleEntry{entry}, pub, gateTestLogger(), 3)
	sched.SetThrottler(throttler)

	ctx, cancel := context.WithCancel(context.Background())
	sched.Start(ctx)
	require.Eventually(t, func() bool {
		return strings.Contains(logBuf.String(), "kafka backlog exceeds threshold")
	}, time.Second, 10*time.Millisecond,
		"backlog 임계값 초과 WARN 로그 발생해야 함")
	cancel()
	sched.Stop()

	assert.Equal(t, 0, pub.callCount(), "throttle 시 emit 차단")
	assert.GreaterOrEqual(t, int(checker.called.Load()), 1, "checker 가 1회 이상 호출됨")
}
