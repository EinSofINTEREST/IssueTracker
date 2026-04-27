package scheduler_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/crawler/core"
	"issuetracker/internal/scheduler"
	"issuetracker/pkg/logger"
	"issuetracker/pkg/urlguard"
)

// gateMockEmitter 는 Emit 호출 인자를 기록하는 테스트 더블입니다.
type gateMockEmitter struct {
	mu    sync.Mutex
	jobs  []*core.CrawlJob
	calls int
}

func (m *gateMockEmitter) Emit(_ context.Context, job *core.CrawlJob) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.jobs = append(m.jobs, job)
	m.calls++
	return nil
}

func (m *gateMockEmitter) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

func gateTestLogger() *logger.Logger {
	return logger.New(logger.DefaultConfig())
}

// TestScheduler_Gate_BlocksRSSEntry:
// SetGate 로 Default() 가드 설정 시 RSS URL entry 가 emit 되지 않아야 함.
func TestScheduler_Gate_BlocksRSSEntry(t *testing.T) {
	pub := &gateMockEmitter{}
	entry := scheduler.ScheduleEntry{
		CrawlerName: "cnn",
		URL:         "https://rss.cnn.com/rss/cnn_health.rss",
		TargetType:  core.TargetTypeFeed,
		Interval:    50 * time.Millisecond,
		Priority:    core.PriorityNormal,
		Timeout:     5 * time.Second,
	}

	sched := scheduler.New([]scheduler.ScheduleEntry{entry}, pub, gateTestLogger(), 3)
	sched.SetGate(urlguard.NewGate(urlguard.Default(), gateTestLogger()))

	ctx, cancel := context.WithCancel(context.Background())
	sched.Start(ctx)
	// 첫 즉시 실행 + 1~2회 tick 보장하는 시간만큼 대기
	time.Sleep(150 * time.Millisecond)
	cancel()
	sched.Stop()

	assert.Equal(t, 0, pub.callCount(), "RSS URL entry 는 가드에 의해 차단되어 emit 안 됨")
}

// TestScheduler_Gate_AllowsCategoryEntry:
// 카테고리 URL entry 는 가드를 통과하여 정상 emit 되어야 함.
func TestScheduler_Gate_AllowsCategoryEntry(t *testing.T) {
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
	sched.SetGate(urlguard.NewGate(urlguard.Default(), gateTestLogger()))

	ctx, cancel := context.WithCancel(context.Background())
	sched.Start(ctx)
	require.Eventually(t, func() bool { return pub.callCount() >= 1 }, time.Second, 10*time.Millisecond)
	cancel()
	sched.Stop()
}

// TestScheduler_NoGate_LegacyBehavior:
// SetGate 미호출 시 모든 URL emit (기존 동작 유지).
func TestScheduler_NoGate_LegacyBehavior(t *testing.T) {
	pub := &gateMockEmitter{}
	entry := scheduler.ScheduleEntry{
		CrawlerName: "cnn",
		URL:         "https://rss.cnn.com/rss/cnn_health.rss",
		TargetType:  core.TargetTypeFeed,
		Interval:    50 * time.Millisecond,
		Priority:    core.PriorityNormal,
		Timeout:     5 * time.Second,
	}

	sched := scheduler.New([]scheduler.ScheduleEntry{entry}, pub, gateTestLogger(), 3)
	// SetGate 호출 없음 — 가드 비활성

	ctx, cancel := context.WithCancel(context.Background())
	sched.Start(ctx)
	require.Eventually(t, func() bool { return pub.callCount() >= 1 }, time.Second, 10*time.Millisecond)
	cancel()
	sched.Stop()
}

// TestScheduler_Gate_AllowAllGuard_DelegatesAll:
// AllowAllGuard 로 명시적 비활성화 시 모든 URL emit.
func TestScheduler_Gate_AllowAllGuard_DelegatesAll(t *testing.T) {
	pub := &gateMockEmitter{}
	entry := scheduler.ScheduleEntry{
		CrawlerName: "cnn",
		URL:         "https://rss.cnn.com/rss/cnn_health.rss",
		TargetType:  core.TargetTypeFeed,
		Interval:    50 * time.Millisecond,
		Priority:    core.PriorityNormal,
		Timeout:     5 * time.Second,
	}

	sched := scheduler.New([]scheduler.ScheduleEntry{entry}, pub, gateTestLogger(), 3)
	sched.SetGate(urlguard.NewGate(urlguard.AllowAllGuard{}, gateTestLogger()))

	ctx, cancel := context.WithCancel(context.Background())
	sched.Start(ctx)
	require.Eventually(t, func() bool { return pub.callCount() >= 1 }, time.Second, 10*time.Millisecond)
	cancel()
	sched.Stop()
}
