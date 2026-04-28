package scheduler

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"issuetracker/internal/crawler/core"
	"issuetracker/pkg/logger"
	"issuetracker/pkg/urlguard"
)

// Scheduler는 등록된 ScheduleEntry 목록을 기반으로 주기적으로 시드 CrawlJob을 생성하고
// Emitter를 통해 Kafka crawl 토픽에 발행합니다.
// 체이닝 Job(크롤된 페이지에서 발견된 URL)은 internal/publisher 패키지가 담당합니다.
//
// URL 가드 (이슈 #119):
//   - SetGate 로 urlguard.Gate 를 설정하면 publish 직전에 entry.URL 검사
//   - 차단된 URL 은 Emit 호출 없이 silent drop + WARN 로그
//   - 미설정 시 가드 비활성 (기존 동작 유지)
//   - atomic.Pointer 로 race-safe 한 lock-free 설정/조회 — Start 이후 변경에도 race 없음
//
// Backlog throttle (이슈 #124):
//   - SetThrottler 로 Throttler 를 설정하면 emit 직전에 ShouldThrottle 검사
//   - throttle 결정 시 emit 호출 없이 silent drop (구현체가 WARN 로그 책임)
//   - 미설정 시 throttle 비활성 (기존 동작 유지)
//   - URL 가드와 직렬 적용: gate(질적 차단) → throttle(양적 차단) → emit
type Scheduler struct {
	entries    []ScheduleEntry
	emitter    Emitter
	gate       atomic.Pointer[urlguard.Gate]
	throttler  atomic.Pointer[throttlerRef]
	log        *logger.Logger
	wg         sync.WaitGroup
	maxRetries int
}

// throttlerRef 는 atomic.Pointer 가 인터페이스 값을 직접 저장하지 못하므로
// Throttler 인터페이스를 감싸 atomic 교체를 지원하는 wrapper 입니다.
type throttlerRef struct {
	t Throttler
}

// New는 새 Scheduler를 생성합니다.
func New(
	entries []ScheduleEntry,
	emitter Emitter,
	log *logger.Logger,
	maxRetries int,
) *Scheduler {
	return &Scheduler{
		entries:    entries,
		emitter:    emitter,
		log:        log,
		maxRetries: maxRetries,
	}
}

// Start는 각 ScheduleEntry에 대한 goroutine을 시작합니다.
// 각 goroutine은 즉시 1회 실행 후 Interval마다 반복합니다.
func (s *Scheduler) Start(ctx context.Context) {
	s.log.WithField("entry_count", len(s.entries)).Info("scheduler starting")

	for _, entry := range s.entries {
		s.wg.Add(1)
		go s.run(ctx, entry)
	}
}

// Stop은 모든 goroutine이 종료될 때까지 대기합니다.
func (s *Scheduler) Stop() {
	s.wg.Wait()
	s.log.Info("scheduler stopped")
}

// SetGate 는 publish 직전 URL 검사에 사용할 urlguard.Gate 를 설정합니다.
// 미설정(nil) 시 가드 비활성 — 모든 entry.URL 이 그대로 emit 됩니다.
//
// 동시성: atomic.Pointer 기반 lock-free 설정/조회 — Start 이후 변경에도 race-safe.
func (s *Scheduler) SetGate(g *urlguard.Gate) {
	s.gate.Store(g)
}

// SetThrottler 는 publish 직전 throttle 결정에 사용할 Throttler 를 설정합니다.
// nil 전달 시 throttle 비활성 (기존 동작 유지). atomic 으로 race-safe 한 swap 보장 —
// Start 이후 throttler 를 교체해도 publish 와 race 없음.
func (s *Scheduler) SetThrottler(t Throttler) {
	if t == nil {
		s.throttler.Store(nil)
		return
	}
	s.throttler.Store(&throttlerRef{t: t})
}

// run은 단일 ScheduleEntry에 대한 스케줄 루프입니다.
func (s *Scheduler) run(ctx context.Context, entry ScheduleEntry) {
	defer s.wg.Done()

	// Interval이 0 이하면 time.NewTicker가 panic을 발생시킵니다.
	if entry.Interval <= 0 {
		s.log.WithFields(map[string]interface{}{
			"crawler":  entry.CrawlerName,
			"url":      entry.URL,
			"interval": entry.Interval,
		}).Error("invalid schedule interval, skipping entry")
		return
	}

	s.log.WithFields(map[string]interface{}{
		"crawler":  entry.CrawlerName,
		"url":      entry.URL,
		"interval": entry.Interval.String(),
	}).Info("scheduler entry started, triggering initial crawl")

	s.publish(ctx, entry)

	ticker := time.NewTicker(entry.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.log.WithFields(map[string]interface{}{
				"crawler": entry.CrawlerName,
				"url":     entry.URL,
			}).Info("scheduler tick, triggering crawl")
			s.publish(ctx, entry)
		}
	}
}

// publish는 CrawlJob을 생성하고 Kafka crawl 토픽에 발행합니다.
func (s *Scheduler) publish(ctx context.Context, entry ScheduleEntry) {
	id, err := newJobID()
	if err != nil {
		s.log.WithFields(map[string]interface{}{
			"crawler": entry.CrawlerName,
			"url":     entry.URL,
		}).WithError(err).Error("failed to generate job id")
		return
	}

	// URL 가드 (이슈 #119): job 생성 직전에 entry.URL 검사
	// 차단 시 emit 호출 없이 silent drop (가드가 WARN 로그 자동 생성)
	if g := s.gate.Load(); g != nil {
		if !g.Allow(entry.URL, map[string]interface{}{
			"crawler": entry.CrawlerName,
			"stage":   "scheduler",
		}) {
			return
		}
	}

	job := &core.CrawlJob{
		ID:          id,
		CrawlerName: entry.CrawlerName,
		Target: core.Target{
			URL:  entry.URL,
			Type: entry.TargetType,
		},
		Priority:    entry.Priority,
		ScheduledAt: time.Now(),
		Timeout:     entry.Timeout,
		MaxRetries:  s.maxRetries,
	}

	// Backlog throttle (이슈 #124): job.Priority 에 대응하는 crawl 토픽의
	// consumer-group lag 가 임계값 초과 시 silent drop. 구현체가 WARN 로그 책임.
	if r := s.throttler.Load(); r != nil && r.t.ShouldThrottle(ctx, job) {
		return
	}

	if err := s.emitter.Emit(ctx, job); err != nil {
		s.log.WithFields(map[string]interface{}{
			"job_id":  job.ID,
			"crawler": entry.CrawlerName,
			"url":     entry.URL,
		}).WithError(err).Error("failed to publish crawl job")
		return
	}

	s.log.WithFields(map[string]interface{}{
		"job_id":   job.ID,
		"crawler":  entry.CrawlerName,
		"url":      entry.URL,
		"priority": int(entry.Priority),
	}).Info("crawl job scheduled")
}

// newJobID는 crypto/rand 기반의 고유 Job ID(32자 hex)를 생성합니다.
func newJobID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate job id: %w", err)
	}
	return hex.EncodeToString(b), nil
}
