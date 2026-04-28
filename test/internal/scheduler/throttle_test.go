package scheduler_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/crawler/core"
	"issuetracker/internal/scheduler"
	"issuetracker/pkg/logger"
	"issuetracker/pkg/queue"
)

// ─────────────────────────────────────────────────────────────────────────────
// Mock BacklogChecker
// ─────────────────────────────────────────────────────────────────────────────

// mockBacklogChecker 는 호출별 (topic, group) 인자를 기록하고 고정된 결과를
// 반환합니다. err 가 nil 이 아니면 lag 와 무관하게 에러를 우선 반환합니다.
type mockBacklogChecker struct {
	mu     sync.Mutex
	lag    int64
	err    error
	calls  []mockBacklogCall
	called atomic.Int32
}

type mockBacklogCall struct {
	Topic string
	Group string
}

func (m *mockBacklogChecker) Backlog(_ context.Context, topic, group string) (int64, error) {
	m.mu.Lock()
	m.calls = append(m.calls, mockBacklogCall{Topic: topic, Group: group})
	m.mu.Unlock()
	m.called.Add(1)
	return m.lag, m.err
}

func (m *mockBacklogChecker) lastCall() mockBacklogCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.calls) == 0 {
		return mockBacklogCall{}
	}
	return m.calls[len(m.calls)-1]
}

// ─────────────────────────────────────────────────────────────────────────────
// 헬퍼
// ─────────────────────────────────────────────────────────────────────────────

func throttleTestLogger() *logger.Logger {
	return logger.New(logger.DefaultConfig())
}

func throttleTestJob(priority core.Priority) *core.CrawlJob {
	return &core.CrawlJob{
		ID:          "job-test-1",
		CrawlerName: "test-crawler",
		Target: core.Target{
			URL:  "https://example.com/feed",
			Type: core.TargetTypeFeed,
		},
		Priority: priority,
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 테스트
// ─────────────────────────────────────────────────────────────────────────────

// TestBacklogThrottler_BelowThreshold_AllowsPublish:
// lag <= maxBacklog 이면 throttle = false (publish 진행).
func TestBacklogThrottler_BelowThreshold_AllowsPublish(t *testing.T) {
	checker := &mockBacklogChecker{lag: 99}
	throttler := scheduler.NewBacklogThrottler(checker, queue.GroupCrawlerWorkers, 100, 0, throttleTestLogger())

	got := throttler.ShouldThrottle(context.Background(), throttleTestJob(core.PriorityNormal))

	assert.False(t, got, "lag(99) <= max(100) → throttle 안 함")
	assert.Equal(t, int32(1), checker.called.Load(), "checker 1회 호출")
}

// TestBacklogThrottler_AtThreshold_AllowsPublish:
// lag == maxBacklog 경계는 통과 — strict greater-than 비교 검증.
func TestBacklogThrottler_AtThreshold_AllowsPublish(t *testing.T) {
	checker := &mockBacklogChecker{lag: 100}
	throttler := scheduler.NewBacklogThrottler(checker, queue.GroupCrawlerWorkers, 100, 0, throttleTestLogger())

	got := throttler.ShouldThrottle(context.Background(), throttleTestJob(core.PriorityNormal))

	assert.False(t, got, "lag(100) == max(100) 경계는 통과 (strict > 비교)")
}

// TestBacklogThrottler_AboveThreshold_Throttles:
// lag > maxBacklog 이면 throttle = true.
func TestBacklogThrottler_AboveThreshold_Throttles(t *testing.T) {
	checker := &mockBacklogChecker{lag: 101}
	throttler := scheduler.NewBacklogThrottler(checker, queue.GroupCrawlerWorkers, 100, 0, throttleTestLogger())

	got := throttler.ShouldThrottle(context.Background(), throttleTestJob(core.PriorityNormal))

	assert.True(t, got, "lag(101) > max(100) → throttle")
}

// TestBacklogThrottler_CheckerError_FailsOpen:
// lag 조회 실패 시 fail-open (false 반환) — 일시적 Kafka 장애가 publish 영구 차단으로
// 이어지지 않도록 보장.
func TestBacklogThrottler_CheckerError_FailsOpen(t *testing.T) {
	checker := &mockBacklogChecker{err: errors.New("kafka unavailable")}
	throttler := scheduler.NewBacklogThrottler(checker, queue.GroupCrawlerWorkers, 100, 0, throttleTestLogger())

	got := throttler.ShouldThrottle(context.Background(), throttleTestJob(core.PriorityNormal))

	assert.False(t, got, "checker 에러 시 fail-open — publish 허용")
}

// TestBacklogThrottler_Disabled_AlwaysAllows:
// maxBacklog <= 0 이면 lag 와 무관하게 항상 false. checker 호출도 생략 (불필요한 RPC 회피).
func TestBacklogThrottler_Disabled_AlwaysAllows(t *testing.T) {
	for _, max := range []int64{0, -1, -1000} {
		t.Run("", func(t *testing.T) {
			checker := &mockBacklogChecker{lag: 1_000_000} // 임계값 무관 통과 검증
			throttler := scheduler.NewBacklogThrottler(checker, queue.GroupCrawlerWorkers, max, 0, throttleTestLogger())

			got := throttler.ShouldThrottle(context.Background(), throttleTestJob(core.PriorityNormal))

			assert.False(t, got, "maxBacklog %d 이면 항상 통과", max)
			assert.Equal(t, int32(0), checker.called.Load(), "disabled 시 checker 호출 안 함 (불필요한 RPC 회피)")
		})
	}
}

// TestBacklogThrottler_PriorityToTopicMapping:
// job.Priority 에 따라 올바른 crawl 토픽이 lag 조회 인자로 전달되는지 검증.
func TestBacklogThrottler_PriorityToTopicMapping(t *testing.T) {
	cases := []struct {
		name      string
		priority  core.Priority
		wantTopic string
	}{
		{"high → crawl.high", core.PriorityHigh, queue.TopicCrawlHigh},
		{"normal → crawl.normal", core.PriorityNormal, queue.TopicCrawlNormal},
		{"low → crawl.low", core.PriorityLow, queue.TopicCrawlLow},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			checker := &mockBacklogChecker{lag: 0}
			throttler := scheduler.NewBacklogThrottler(checker, queue.GroupCrawlerWorkers, 100, 0, throttleTestLogger())

			throttler.ShouldThrottle(context.Background(), throttleTestJob(tc.priority))

			require.Equal(t, int32(1), checker.called.Load())
			call := checker.lastCall()
			assert.Equal(t, tc.wantTopic, call.Topic, "priority → topic 매핑")
			assert.Equal(t, queue.GroupCrawlerWorkers, call.Group, "group 은 항상 crawler workers 그룹")
		})
	}
}

// TestBacklogThrottler_TimeoutAppliedToCheckCtx:
// timeout 이 양수일 때 checker 에 전달된 ctx 가 timeout 적용 ctx 인지 검증.
// (구현이 timeout 을 무시하면 ctx.Deadline() 이 origin 그대로일 것)
func TestBacklogThrottler_TimeoutAppliedToCheckCtx(t *testing.T) {
	captured := &deadlineCapturingChecker{}
	throttler := scheduler.NewBacklogThrottler(captured, queue.GroupCrawlerWorkers, 100, 50*time.Millisecond, throttleTestLogger())

	throttler.ShouldThrottle(context.Background(), throttleTestJob(core.PriorityNormal))

	require.True(t, captured.hadDeadline, "timeout > 0 이면 checker ctx 에 deadline 이 설정되어야 함")
	assert.LessOrEqual(t, captured.untilDeadline, 50*time.Millisecond,
		"checker ctx 의 deadline 은 timeout 이내 — %v", captured.untilDeadline)
}

// TestBacklogThrottler_LogContains 는 throttle 시 WARN 로그에 핵심 필드가 포함되는지
// captureLogger 로 검증합니다 (gateLogBuf/safeBuffer/captureLogger 는 scheduler_gate_test.go).
func TestBacklogThrottler_LogContains(t *testing.T) {
	buf := &safeBuffer{}
	checker := &mockBacklogChecker{lag: 500}
	throttler := scheduler.NewBacklogThrottler(checker, queue.GroupCrawlerWorkers, 100, 0, captureLogger(buf))

	throttler.ShouldThrottle(context.Background(), throttleTestJob(core.PriorityNormal))

	out := buf.String()
	assert.Contains(t, out, "kafka backlog exceeds threshold", "WARN message")
	assert.Contains(t, out, queue.TopicCrawlNormal, "topic 필드")
	assert.Contains(t, out, "test-crawler", "crawler 필드")
	assert.Contains(t, out, "https://example.com/feed", "url 필드")
	// backlog/max_backlog 정수 필드는 따옴표 없이 직렬화됨
	assert.True(t, strings.Contains(out, `"backlog":500`), "backlog 값")
	assert.True(t, strings.Contains(out, `"max_backlog":100`), "max_backlog 값")
}

// ─────────────────────────────────────────────────────────────────────────────
// deadline capturing helper
// ─────────────────────────────────────────────────────────────────────────────

type deadlineCapturingChecker struct {
	hadDeadline   bool
	untilDeadline time.Duration
}

func (d *deadlineCapturingChecker) Backlog(ctx context.Context, _, _ string) (int64, error) {
	if dl, ok := ctx.Deadline(); ok {
		d.hadDeadline = true
		d.untilDeadline = time.Until(dl)
	}
	return 0, nil
}
