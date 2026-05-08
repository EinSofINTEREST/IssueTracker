// Package scheduler 의 resolver — DB 기반 ScheduleEntry source 를 5분 TTL cache 로 흡수.
//
// fetcher_rules 의 rule.Resolver / rate_limiter 의 SourceConfigResolver 와 동일 패턴 —
// sync.Map TTL cache + singleflight + 운영 변경 즉시 반영용 Invalidate API.

package scheduler

import (
	"context"
	"errors"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"issuetracker/internal/processor/fetcher/core"
	"issuetracker/internal/storage"
	"issuetracker/pkg/logger"
)

// DefaultEntryCacheTTL 는 entries 캐시의 기본 유효기간.
//
// 운영자가 scheduler_entries UPDATE 후 적용까지 최대 지연. rule.Resolver 와 동일 5분.
const DefaultEntryCacheTTL = 5 * time.Minute

// EntryResolver 는 운영 중 동적으로 갱신되는 ScheduleEntry 목록을 반환합니다.
//
// 모든 구현체는 goroutine-safe — Scheduler 의 Refresh 가 주기적으로 호출.
type EntryResolver interface {
	// Resolve 는 현재 시점의 enabled=TRUE entries 를 반환합니다.
	//
	// cache hit 시 즉시 반환. miss / 만료 시 DB hit. DB 일시 장애는 fail-open: 마지막 성공
	// snapshot (또는 빈 슬라이스) 반환 + warn 로그 — Scheduler 가 stall 되지 않도록.
	Resolve(ctx context.Context) ([]ScheduleEntry, error)

	// Invalidate 는 cache 를 즉시 만료시킵니다 (운영자 변경 즉시 반영용).
	Invalidate()
}

// dbEntryResolver 는 SchedulerEntryRepository 를 source-of-truth 로 사용하는 Resolver 입니다.
//
// singleflight 로 thundering herd 방지 (동일 Refresh 중 여러 호출자가 들어와도 DB 1회).
type dbEntryResolver struct {
	repo   storage.SchedulerEntryRepository
	cfg    EntryConverter
	log    *logger.Logger
	ttl    time.Duration
	mu     sync.RWMutex
	cached []ScheduleEntry
	at     time.Time
	flight singleflight.Group
}

// EntryConverter 는 SchedulerEntryRecord → ScheduleEntry 로 변환합니다.
//
// CrawlerName 매핑 등 도메인 결정 (host 기반 vs source_name 기반) 을 호출자가 주입.
// 기본 구현은 NewDefaultEntryConverter — host 기반 CrawlerName + storage record 의 priority 를
// core.Priority 로 매핑.
type EntryConverter func(rec *storage.SchedulerEntryRecord, jobTimeout time.Duration) ScheduleEntry

// NewDefaultEntryConverter 는 host 기반 CrawlerName + priority 매핑 기본 구현입니다.
//
// jobTimeout 은 전역 SCHEDULER_JOB_TIMEOUT 사용 — entries 마다 다르게 두고 싶으면 metadata
// JSONB 에서 추출하는 별도 converter 를 주입.
func NewDefaultEntryConverter(jobTimeout time.Duration) EntryConverter {
	return func(rec *storage.SchedulerEntryRecord, _ time.Duration) ScheduleEntry {
		host := hostOf(rec.URL)
		return ScheduleEntry{
			CrawlerName: host,
			URL:         rec.URL,
			TargetType:  core.TargetType(rec.TargetType),
			Interval:    rec.Interval,
			Priority:    intToPriority(rec.Priority),
			Timeout:     jobTimeout,
		}
	}
}

// intToPriority 는 storage.priority (1/2/3) 를 core.Priority 로 매핑합니다.
//
// 미인식 값은 PriorityNormal (2) — 정책 안전한 default.
func intToPriority(p int) core.Priority {
	switch p {
	case 1:
		return core.PriorityHigh
	case 3:
		return core.PriorityLow
	default:
		return core.PriorityNormal
	}
}

// NewEntryResolver 는 DB 기반 EntryResolver 를 생성합니다.
//
// repo / converter / log 모두 비-nil 필수.
// ttl 이 0 이하면 DefaultEntryCacheTTL.
func NewEntryResolver(repo storage.SchedulerEntryRepository, converter EntryConverter, log *logger.Logger, ttl time.Duration) (EntryResolver, error) {
	if repo == nil {
		return nil, errors.New("scheduler: NewEntryResolver requires non-nil repo")
	}
	if converter == nil {
		return nil, errors.New("scheduler: NewEntryResolver requires non-nil converter")
	}
	if log == nil {
		return nil, errors.New("scheduler: NewEntryResolver requires non-nil log")
	}
	if ttl <= 0 {
		ttl = DefaultEntryCacheTTL
	}
	return &dbEntryResolver{repo: repo, cfg: converter, log: log, ttl: ttl}, nil
}

// Resolve 는 enabled=TRUE entries 를 반환합니다 (5분 TTL cache).
func (r *dbEntryResolver) Resolve(ctx context.Context) ([]ScheduleEntry, error) {
	r.mu.RLock()
	if time.Since(r.at) < r.ttl && r.cached != nil {
		entries := r.cached
		r.mu.RUnlock()
		return entries, nil
	}
	r.mu.RUnlock()

	v, _, _ := r.flight.Do("entries", func() (interface{}, error) {
		// double-check: singleflight leader 진입 사이 다른 leader 가 cache 채웠을 수 있음.
		r.mu.RLock()
		if time.Since(r.at) < r.ttl && r.cached != nil {
			entries := r.cached
			r.mu.RUnlock()
			return entries, nil
		}
		r.mu.RUnlock()

		recs, err := r.repo.ListEnabled(ctx, "")
		if err != nil {
			r.log.WithError(err).Warn("scheduler entries reload failed — keeping last snapshot")
			r.mu.RLock()
			defer r.mu.RUnlock()
			if r.cached != nil {
				return r.cached, nil
			}
			return []ScheduleEntry{}, nil
		}

		entries := make([]ScheduleEntry, 0, len(recs))
		for _, rec := range recs {
			entries = append(entries, r.cfg(rec, 0))
		}

		r.mu.Lock()
		r.cached = entries
		r.at = time.Now()
		r.mu.Unlock()

		return entries, nil
	})
	return v.([]ScheduleEntry), nil
}

// Invalidate 는 cache 를 즉시 만료시킵니다.
func (r *dbEntryResolver) Invalidate() {
	r.mu.Lock()
	r.at = time.Time{}
	r.cached = nil
	r.mu.Unlock()
}
