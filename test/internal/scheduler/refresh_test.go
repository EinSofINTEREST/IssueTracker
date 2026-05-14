package scheduler_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/scheduler"
	"issuetracker/internal/storage/model"
	"issuetracker/pkg/logger"
)

// staticEntryResolver 는 미리 정한 entries 를 반환하는 stub — Refresh diff 검증용.
type staticEntryResolver struct {
	entries []scheduler.ScheduleEntry
}

func (r *staticEntryResolver) Resolve(_ context.Context) ([]scheduler.ScheduleEntry, error) {
	return r.entries, nil
}
func (r *staticEntryResolver) Invalidate() {}

// helper — 짧은 interval 의 entry 생성.
func makeEntry(host, url string, interval time.Duration) scheduler.ScheduleEntry {
	return scheduler.ScheduleEntry{
		CrawlerName: host,
		URL:         url,
		TargetType:  "category",
		Interval:    interval,
		Priority:    2,
		Timeout:     30 * time.Second,
	}
}

func TestScheduler_Refresh_AddsNewEntry(t *testing.T) {
	pub := &mockPublisher{}
	resolver := &staticEntryResolver{
		entries: []scheduler.ScheduleEntry{
			makeEntry("a.com", "https://a.com", 100*time.Millisecond),
		},
	}
	sched := scheduler.New(nil, pub, logger.New(logger.DefaultConfig()), 1)
	sched.SetEntryResolver(resolver)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sched.Start(ctx)
	time.Sleep(50 * time.Millisecond)

	// 신규 entry 추가.
	resolver.entries = append(resolver.entries, makeEntry("b.com", "https://b.com", 100*time.Millisecond))
	require.NoError(t, sched.Refresh(ctx))

	time.Sleep(50 * time.Millisecond)
	cancel()
	sched.Stop()

	urls := make(map[string]bool)
	for _, j := range pub.snapshot() {
		urls[j.Target.URL] = true
	}
	assert.True(t, urls["https://a.com"], "기존 entry 가 emit 됨")
	assert.True(t, urls["https://b.com"], "신규 entry 도 Refresh 후 emit 됨")
}

func TestScheduler_Refresh_RemovesEntry(t *testing.T) {
	pub := &mockPublisher{}
	resolver := &staticEntryResolver{
		entries: []scheduler.ScheduleEntry{
			makeEntry("a.com", "https://a.com", 100*time.Millisecond),
			makeEntry("b.com", "https://b.com", 100*time.Millisecond),
		},
	}
	sched := scheduler.New(nil, pub, logger.New(logger.DefaultConfig()), 1)
	sched.SetEntryResolver(resolver)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sched.Start(ctx)
	time.Sleep(50 * time.Millisecond)

	// b.com 제거.
	resolver.entries = []scheduler.ScheduleEntry{
		makeEntry("a.com", "https://a.com", 100*time.Millisecond),
	}
	require.NoError(t, sched.Refresh(ctx))

	// 제거 후 충분히 대기 — 기존 b.com goroutine 이 cancel 됐다면 더 이상 emit 발생 안 함.
	beforeRemove := 0
	for _, j := range pub.snapshot() {
		if j.Target.URL == "https://b.com" {
			beforeRemove++
		}
	}

	time.Sleep(150 * time.Millisecond)
	cancel()
	sched.Stop()

	afterRemove := 0
	for _, j := range pub.snapshot() {
		if j.Target.URL == "https://b.com" {
			afterRemove++
		}
	}
	assert.Equal(t, beforeRemove, afterRemove,
		"제거된 entry 는 Refresh 후 더 이상 emit 안 됨")
}

func TestScheduler_Refresh_IntervalChange_RespawnsGoroutine(t *testing.T) {
	pub := &mockPublisher{}
	resolver := &staticEntryResolver{
		entries: []scheduler.ScheduleEntry{
			makeEntry("a.com", "https://a.com", time.Hour), // 매우 긴 interval — 첫 emit 1회만 발생
		},
	}
	sched := scheduler.New(nil, pub, logger.New(logger.DefaultConfig()), 1)
	sched.SetEntryResolver(resolver)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sched.Start(ctx)
	time.Sleep(50 * time.Millisecond)
	initialCount := pub.count()
	require.Equal(t, 1, initialCount, "긴 interval 의 첫 emit 1회")

	// interval 변경 — 짧은 주기로 변경 후 Refresh 시 respawn 되어 즉시 새 publish 발생.
	resolver.entries = []scheduler.ScheduleEntry{
		makeEntry("a.com", "https://a.com", 50*time.Millisecond),
	}
	require.NoError(t, sched.Refresh(ctx))
	time.Sleep(150 * time.Millisecond)
	cancel()
	sched.Stop()

	assert.Greater(t, pub.count(), initialCount,
		"interval 변경 후 respawn 으로 추가 emit 발생")
}

func TestScheduler_Refresh_NoResolver_NoOp(t *testing.T) {
	pub := &mockPublisher{}
	sched := scheduler.New(nil, pub, logger.New(logger.DefaultConfig()), 1)
	// SetEntryResolver 미호출 — resolver=nil.

	err := sched.Refresh(context.Background())
	assert.NoError(t, err, "resolver 미설정 시 Refresh 는 noop")
}

// 본 테스트 파일은 storage import 도 함께 지키기 위해 unused import 회피용 sentinel.
var _ = model.SchedulerCategoryNews
