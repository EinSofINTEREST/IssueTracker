package scheduler_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/crawler/core"
	"issuetracker/internal/scheduler"
	"issuetracker/pkg/logger"
)

// ─────────────────────────────────────────────────────────────────────────────
// Mock Publisher
// ─────────────────────────────────────────────────────────────────────────────

type mockEmitter struct {
	mu   sync.Mutex
	jobs []*core.CrawlJob
	err  error
}

func (m *mockEmitter) Emit(_ context.Context, job *core.CrawlJob) error {
	if m.err != nil {
		return m.err
	}
	m.mu.Lock()
	m.jobs = append(m.jobs, job)
	m.mu.Unlock()
	return nil
}

func (m *mockEmitter) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.jobs)
}

type countEmitter struct {
	n atomic.Int32
}

func (c *countEmitter) Emit(_ context.Context, _ *core.CrawlJob) error {
	c.n.Add(1)
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// 헬퍼
// ─────────────────────────────────────────────────────────────────────────────

func testLogger() *logger.Logger {
	cfg := logger.DefaultConfig()
	cfg.Pretty = false
	return logger.New(cfg)
}

func testEntry(url string, interval time.Duration) scheduler.ScheduleEntry {
	return scheduler.ScheduleEntry{
		CrawlerName: "test-crawler",
		URL:         url,
		TargetType:  core.TargetTypeFeed,
		Interval:    interval,
		Priority:    core.PriorityNormal,
		Timeout:     30 * time.Second,
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 테스트
// ─────────────────────────────────────────────────────────────────────────────

func TestScheduler_PublishesJobOnStart(t *testing.T) {
	pub := &mockEmitter{}
	entry := testEntry("https://example.com/feed/start", 10*time.Minute)

	ctx, cancel := context.WithCancel(context.Background())
	sched := scheduler.New([]scheduler.ScheduleEntry{entry}, pub, testLogger())
	sched.Start(ctx)

	require.Eventually(t, func() bool {
		return pub.count() == 1
	}, 2*time.Second, 50*time.Millisecond, "시작 시 즉시 1회 발행 기대")

	cancel()
	sched.Stop()
}

func TestScheduler_PublishesRepeatedly(t *testing.T) {
	pub := &mockEmitter{}
	entry := testEntry("https://example.com/feed/repeat", 100*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	sched := scheduler.New([]scheduler.ScheduleEntry{entry}, pub, testLogger())
	sched.Start(ctx)

	// 즉시 1회 + interval 경과 후 추가 발행
	require.Eventually(t, func() bool {
		return pub.count() >= 2
	}, 2*time.Second, 50*time.Millisecond, "interval 경과 후 반복 발행 기대")

	cancel()
	sched.Stop()
}

func TestScheduler_LogsPublishError(t *testing.T) {
	pub := &mockEmitter{err: errors.New("kafka unavailable")}
	entry := testEntry("https://example.com/feed/fail", 10*time.Minute)

	ctx, cancel := context.WithCancel(context.Background())
	sched := scheduler.New([]scheduler.ScheduleEntry{entry}, pub, testLogger())
	sched.Start(ctx)

	// 발행 실패 시 패닉 없이 진행되어야 함
	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, 0, pub.count())

	cancel()
	sched.Stop()
}

func TestScheduler_StopsOnContextCancel(t *testing.T) {
	pub := &mockEmitter{}
	entry := testEntry("https://example.com/feed/cancel", 50*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	sched := scheduler.New([]scheduler.ScheduleEntry{entry}, pub, testLogger())
	sched.Start(ctx)

	time.Sleep(30 * time.Millisecond)
	cancel()

	done := make(chan struct{})
	go func() {
		sched.Stop()
		close(done)
	}()

	select {
	case <-done:
		// 정상 종료
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() timeout: context 취소 후 goroutine이 종료되지 않음")
	}
}

func TestScheduler_MultipleEntries(t *testing.T) {
	pub := &countEmitter{}
	entries := []scheduler.ScheduleEntry{
		testEntry("https://example.com/feed/a", 10*time.Minute),
		testEntry("https://example.com/feed/b", 10*time.Minute),
		testEntry("https://example.com/feed/c", 10*time.Minute),
	}

	ctx, cancel := context.WithCancel(context.Background())
	sched := scheduler.New(entries, pub, testLogger())
	sched.Start(ctx)

	// 3개 엔트리 × 즉시 1회 = 3회 발행
	require.Eventually(t, func() bool {
		return pub.n.Load() == 3
	}, 2*time.Second, 50*time.Millisecond, "3개 엔트리 각 1회 발행 기대")

	cancel()
	sched.Stop()
}

func TestScheduler_JobFieldsAreCorrect(t *testing.T) {
	pub := &mockEmitter{}
	entry := scheduler.ScheduleEntry{
		CrawlerName: "cnn",
		URL:         "https://rss.cnn.com/rss/cnn_topstories.rss",
		TargetType:  core.TargetTypeFeed,
		Interval:    10 * time.Minute,
		Priority:    core.PriorityHigh,
		Timeout:     45 * time.Second,
	}

	ctx, cancel := context.WithCancel(context.Background())
	sched := scheduler.New([]scheduler.ScheduleEntry{entry}, pub, testLogger())
	sched.Start(ctx)

	require.Eventually(t, func() bool { return pub.count() == 1 }, 2*time.Second, 50*time.Millisecond)
	cancel()
	sched.Stop()

	pub.mu.Lock()
	job := pub.jobs[0]
	pub.mu.Unlock()

	assert.NotEmpty(t, job.ID)
	assert.Equal(t, "cnn", job.CrawlerName)
	assert.Equal(t, entry.URL, job.Target.URL)
	assert.Equal(t, core.TargetTypeFeed, job.Target.Type)
	assert.Equal(t, core.PriorityHigh, job.Priority)
	assert.Equal(t, 45*time.Second, job.Timeout)
	assert.Equal(t, 3, job.MaxRetries)
	assert.False(t, job.ScheduledAt.IsZero())
}
