package scheduler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
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

// safeBuffer 는 mutex 로 보호된 bytes.Buffer 입니다.
// logger 가 goroutine 에서 Write 하고 테스트가 main 에서 String() 으로 읽는 동안
// race 가 발생하지 않도록 보호합니다 (bytes.Buffer 는 thread-safe 하지 않음).
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *safeBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *safeBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// captureLogger 는 출력을 buf 로 캡쳐하는 logger 를 반환합니다.
func captureLogger(buf *safeBuffer) *logger.Logger {
	cfg := logger.DefaultConfig()
	cfg.Output = buf
	cfg.Level = logger.LevelDebug
	return logger.New(cfg)
}

// hasBlockLog 는 buf 에 'blocked by url guard' WARN 로그가 1건 이상 존재하는지
// 검사합니다 (JSON 라인별 파싱).
func hasBlockLog(buf *safeBuffer, url string) bool {
	for _, line := range strings.Split(buf.String(), "\n") {
		if line == "" {
			continue
		}
		var e map[string]interface{}
		if json.Unmarshal([]byte(line), &e) != nil {
			continue
		}
		if e["message"] == "blocked by url guard" && e["url"] == url {
			return true
		}
	}
	return false
}

// TestScheduler_Gate_BlocksRSSEntry:
// SetGate 로 Default() 가드 설정 시 RSS URL entry 가 emit 되지 않고,
// Gate 가 차단 WARN 로그를 남기는지 확인 — 단순 callCount==0 만 확인하면
// 스케줄러가 한 번도 안 돌았는지 차단됐는지 구분 불가하므로 로그 신호로 검증.
func TestScheduler_Gate_BlocksRSSEntry(t *testing.T) {
	pub := &gateMockEmitter{}
	gateLogBuf := &safeBuffer{}
	entry := scheduler.ScheduleEntry{
		CrawlerName: "cnn",
		URL:         "https://rss.cnn.com/rss/cnn_health.rss",
		TargetType:  core.TargetTypeFeed,
		Interval:    50 * time.Millisecond,
		Priority:    core.PriorityNormal,
		Timeout:     5 * time.Second,
	}

	sched := scheduler.New([]scheduler.ScheduleEntry{entry}, pub, gateTestLogger(), 3)
	// Gate 의 logger 를 캡쳐 — 차단 시 'blocked by url guard' 로그가 buf 에 기록됨
	sched.SetGate(urlguard.NewGate(urlguard.Default(), captureLogger(gateLogBuf)))

	ctx, cancel := context.WithCancel(context.Background())
	sched.Start(ctx)
	// 가드 차단 로그가 발생할 때까지 대기 (실행 자체가 안 일어났으면 영원히 false → timeout 으로 fail)
	require.Eventually(t, func() bool {
		return hasBlockLog(gateLogBuf, "https://rss.cnn.com/rss/cnn_health.rss")
	}, time.Second, 10*time.Millisecond,
		"스케줄러가 publish 시도 후 가드가 RSS URL 을 차단해야 함 (차단 로그 1건 이상)")
	cancel()
	sched.Stop()

	assert.Equal(t, 0, pub.callCount(), "차단된 entry 는 inner emit 호출 안 됨")
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
