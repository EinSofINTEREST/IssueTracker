package scheduler

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"issuetracker/internal/processor/fetcher/core"
	"issuetracker/pkg/categories"
	"issuetracker/pkg/logger"
	"issuetracker/pkg/urlguard"
)

// Scheduler는 등록된 ScheduleEntry 목록을 기반으로 주기적으로 시드 CrawlJob을 생성하고
// Emitter를 통해 Kafka crawl 토픽에 발행합니다.
// 체이닝 Job(크롤된 페이지에서 발견된 URL)은 internal/publisher 패키지가 담당합니다.
//
// URL 가드:
//   - SetGate 로 urlguard.Gate 를 설정하면 publish 직전에 entry.URL 검사
//   - 차단된 URL 은 Emit 호출 없이 silent drop + WARN 로그
//   - 미설정 시 가드 비활성 (기존 동작 유지)
//   - atomic.Pointer 로 race-safe 한 lock-free 설정/조회 — Start 이후 변경에도 race 없음
//
// Backlog throttle:
//   - SetThrottler 로 Throttler 를 설정하면 emit 직전에 ShouldThrottle 검사
//   - throttle 결정 시 emit 호출 없이 silent drop (구현체가 WARN 로그 책임)
//   - 미설정 시 throttle 비활성 (기존 동작 유지)
//   - URL 가드와 직렬 적용: gate(질적 차단) → throttle(양적 차단) → emit
//
// 동적 Refresh (이슈 #328):
//   - SetEntryResolver + StartRefreshLoop 로 DB 기반 entries 운영 중 변경 반영
//   - 각 entry 마다 per-entry context.CancelFunc 보관 — Refresh 시 diff 후 신규는 spawn,
//     사라진 / 변경된 entry 는 cancel + (변경의 경우) respawn
//   - SetEntryResolver 미사용 시 기존 정적 entries (생성자 인자) 가 유지되며 Refresh 비활성
type Scheduler struct {
	emitter    Emitter
	gate       atomic.Pointer[urlguard.Gate]
	throttler  atomic.Pointer[throttlerRef]
	log        *logger.Logger
	wg         sync.WaitGroup
	maxRetries int

	// entries 관리 (정적 + 동적 모두 지원).
	mu            sync.Mutex
	running       map[string]*entryHandle // key = entryKey(crawler+url)
	resolver      EntryResolver
	staticEntries []ScheduleEntry // 생성자 인자 — resolver 가 nil 일 때 사용

	// ctx 는 Start 시점에 저장되는 long-lived parent context.
	//
	// spawnEntryLocked 는 Refresh 의 인자 ctx (DB lookup 용 short-lived) 가 아니라 본 ctx 를
	// 사용해 entry goroutine 의 lifecycle 이 Refresh 호출자의 짧은 ctx 에 묶이지 않도록 분리
	// (gemini High 반영). mu 보호 — Start 가 1회 set, spawnEntryLocked 가 read.
	ctx context.Context

	stopRefreshCh chan struct{}
	refreshOnce   sync.Once
}

// entryHandle 은 단일 entry goroutine 의 lifecycle 정보입니다.
//
// snapshot: 현재 적용 중인 entry 값 (interval 변경 감지용)
// cancel: goroutine 종료 신호
type entryHandle struct {
	snapshot ScheduleEntry
	cancel   context.CancelFunc
}

// entryKey 는 동일 entry 를 식별하는 키 (CrawlerName + URL).
// scheduler_entries 의 자연키 (category, source_name, url) 와는 다름 — Scheduler 내부는
// CrawlerName + URL 기반으로 dedup (같은 URL 의 중복 spawn 회피).
func entryKey(e ScheduleEntry) string {
	return e.CrawlerName + "|" + e.URL
}

// throttlerRef 는 atomic.Pointer 가 인터페이스 값을 직접 저장하지 못하므로
// Throttler 인터페이스를 감싸 atomic 교체를 지원하는 wrapper 입니다.
type throttlerRef struct {
	t Throttler
}

// New는 새 Scheduler를 생성합니다.
//
// entries 는 정적 시드 — SetEntryResolver 로 동적 reload 활성화 시 무시됩니다 (resolver 가
// 부팅 직후 ListEnabled 로 첫 snapshot 을 채워 동일 역할 수행).
func New(
	entries []ScheduleEntry,
	emitter Emitter,
	log *logger.Logger,
	maxRetries int,
) *Scheduler {
	return &Scheduler{
		emitter:       emitter,
		log:           log,
		maxRetries:    maxRetries,
		running:       make(map[string]*entryHandle),
		staticEntries: entries,
	}
}

// SetEntryResolver 는 동적 reload 를 활성화합니다 (이슈 #328).
//
// 본 메소드 호출 후 Start 시점에 resolver.Resolve 로 entries 를 가져옵니다 (생성자 인자
// 정적 entries 는 무시). StartRefreshLoop 호출 시 주기적으로 diff 갱신.
//
// Start 이전에 호출해야 하며, goroutine-safe 하지 않으므로 초기화 시 1회만 호출.
func (s *Scheduler) SetEntryResolver(r EntryResolver) {
	s.resolver = r
}

// Start는 각 ScheduleEntry에 대한 goroutine을 시작합니다.
// 각 goroutine은 즉시 1회 실행 후 Interval마다 반복합니다.
//
// resolver 가 등록되어 있으면 Resolve 결과로, 아니면 생성자 인자 정적 entries 로 구동.
// ctx 는 entry goroutine 의 long-lived parent — Refresh 가 호출되는 short-lived ctx 와 분리.
func (s *Scheduler) Start(ctx context.Context) {
	entries := s.initialEntries(ctx)
	s.log.WithField("entry_count", len(entries)).Info("scheduler starting")

	s.mu.Lock()
	s.ctx = ctx
	for _, entry := range entries {
		s.spawnEntryLocked(entry)
	}
	s.mu.Unlock()
}

// initialEntries 는 Start 시점에 사용할 entries 를 반환합니다 — resolver 우선.
func (s *Scheduler) initialEntries(ctx context.Context) []ScheduleEntry {
	if s.resolver == nil {
		return s.staticEntries
	}
	entries, err := s.resolver.Resolve(ctx)
	if err != nil {
		s.log.WithError(err).Warn("scheduler initial resolve failed, falling back to static entries")
		return s.staticEntries
	}
	return entries
}

// Stop은 모든 goroutine이 종료될 때까지 대기합니다.
func (s *Scheduler) Stop() {
	s.refreshOnce.Do(func() {
		if s.stopRefreshCh != nil {
			close(s.stopRefreshCh)
		}
	})
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

// spawnEntryLocked 는 mu 보유 상태에서 호출 — 새 entry goroutine 을 시작하고 running map 에
// 등록합니다. interval 이 0 이하인 entry 는 spawn 안 함 (run 내부에서 거부될 거지만 사전 차단).
//
// parent 는 s.ctx (Start 시점에 저장된 long-lived ctx) 를 사용 — Refresh 호출자의 short-lived
// ctx 가 entry goroutine 을 조기 종료시키지 않도록 분리 (gemini High 반영).
func (s *Scheduler) spawnEntryLocked(entry ScheduleEntry) {
	if entry.Interval <= 0 {
		s.log.WithFields(map[string]interface{}{
			"crawler":  entry.CrawlerName,
			"url":      entry.URL,
			"interval": entry.Interval,
		}).Error("invalid schedule interval, skipping entry")
		return
	}
	parent := s.ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	s.running[entryKey(entry)] = &entryHandle{snapshot: entry, cancel: cancel}
	s.wg.Add(1)
	go s.run(ctx, entry)
}

// StartRefreshLoop 는 주기적으로 resolver 를 호출해 entries 를 동적 갱신합니다.
//
// resolver 가 등록되지 않았으면 noop. 운영 중 scheduler_entries UPDATE 가 다음 refresh
// 주기 (default 30s) 부터 반영. ticker 직전에 resolver.Invalidate() 를 호출하여 5분 TTL
// cache 가 30s refresh 를 차단하지 않도록 함 (CodeRabbit Major 반영) — Resolve 는 매 refresh
// 마다 DB hit, 그러나 호출 간 cache hit 은 ad-hoc Resolve (e.g. Scheduler 외부 호출자) 가
// 흡수.
//
// Stop 호출 시 본 loop 도 자동 종료.
func (s *Scheduler) StartRefreshLoop(parent context.Context, interval time.Duration) {
	if s.resolver == nil {
		s.log.Info("scheduler refresh loop disabled (no entry resolver wired)")
		return
	}
	if interval <= 0 {
		interval = 30 * time.Second
	}
	s.stopRefreshCh = make(chan struct{})
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		s.log.WithField("interval", interval.String()).Info("scheduler refresh loop started")
		for {
			select {
			case <-parent.Done():
				return
			case <-s.stopRefreshCh:
				return
			case <-ticker.C:
				// cache 우회 — refresh 가 의도된 DB hit 이 되도록.
				s.resolver.Invalidate()
				if err := s.Refresh(parent); err != nil {
					s.log.WithError(err).Warn("scheduler refresh failed (non-fatal)")
				}
			}
		}
	}()
}

// Refresh 는 resolver 로 최신 entries 를 가져와 running goroutines 와 diff 후 동기화합니다.
//
// 정책:
//   - 신규 entry → spawn
//   - 사라진 entry → cancel
//   - 기존 entry 의 interval / priority / target_type 변경 → cancel + respawn
//
// resolver 미등록 시 즉시 nil 반환 (정적 모드).
func (s *Scheduler) Refresh(parent context.Context) error {
	if s.resolver == nil {
		return nil
	}
	entries, err := s.resolver.Resolve(parent)
	if err != nil {
		return err
	}
	desired := make(map[string]ScheduleEntry, len(entries))
	for _, e := range entries {
		desired[entryKey(e)] = e
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	added, removed, changed := 0, 0, 0

	// 사라진 / 변경된 entry 제거.
	for key, h := range s.running {
		newE, ok := desired[key]
		if !ok {
			h.cancel()
			delete(s.running, key)
			removed++
			continue
		}
		if !sameEntry(h.snapshot, newE) {
			h.cancel()
			delete(s.running, key)
			s.spawnEntryLocked(newE)
			changed++
		}
	}

	// 신규 entry 추가.
	for key, e := range desired {
		if _, ok := s.running[key]; !ok {
			s.spawnEntryLocked(e)
			added++
		}
	}

	if added > 0 || removed > 0 || changed > 0 {
		s.log.WithFields(map[string]interface{}{
			"added":   added,
			"removed": removed,
			"changed": changed,
			"total":   len(s.running),
		}).Info("scheduler entries refreshed")
	}
	return nil
}

// sameEntry 는 두 entry 가 lifecycle 영향이 있는 필드 (Interval / Priority / TargetType /
// Timeout) 가 모두 동일한지 확인합니다. CrawlerName / URL 은 entryKey 로 이미 동등.
func sameEntry(a, b ScheduleEntry) bool {
	return a.Interval == b.Interval &&
		a.Priority == b.Priority &&
		a.TargetType == b.TargetType &&
		a.Timeout == b.Timeout
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

	// URL 가드: job 생성 직전에 entry.URL 검사
	// 차단 시 emit 호출 없이 silent drop (가드가 WARN 로그 자동 생성)
	if g := s.gate.Load(); g != nil {
		if !g.Allow(entry.URL, map[string]interface{}{
			"crawler": entry.CrawlerName,
			"stage":   "scheduler",
		}) {
			return
		}
	}

	// 카테고리 hint 주입 (이슈 #381) — 다운스트림 CategoryBasedResolver 가 priority
	// 결정에 사용. 빈 카테고리는 Metadata 키 자체를 생략하여 nil-safe 동작 보존.
	var meta map[string]interface{}
	if entry.Category != "" {
		meta = map[string]interface{}{categories.MetadataKey: string(entry.Category)}
	}

	job := &core.CrawlJob{
		ID:          id,
		CrawlerName: entry.CrawlerName,
		Target: core.Target{
			URL:      entry.URL,
			Type:     entry.TargetType,
			Metadata: meta,
		},
		Priority:    entry.Priority,
		ScheduledAt: time.Now(),
		Timeout:     entry.Timeout,
		MaxRetries:  s.maxRetries,
	}

	// Backlog throttle: job.Priority 에 대응하는 crawl 토픽의
	// consumer-group lag 가 임계값 초과 시 silent drop. 구현체가 WARN 로그 책임.
	if r := s.throttler.Load(); r != nil && r.t.ShouldThrottle(ctx, job) {
		return
	}

	if err := s.emitter.Emit(ctx, job); err != nil {
		// ErrEmitSkipped: PipelineGuard 가 cycle 진행 중 판단으로 skip — non-fatal.
		// "scheduled" / "failed" 양쪽 로그 모두 생략 (실제 발행 안 된 job 이 발행된 것처럼 보이지 않게).
		if errors.Is(err, ErrEmitSkipped) {
			return
		}
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
