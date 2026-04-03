package scheduler

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	"issuetracker/internal/crawler/core"
	"issuetracker/pkg/logger"
)

// Scheduler는 등록된 ScheduleEntry 목록을 기반으로 주기적으로 시드 CrawlJob을 생성하고
// Emitter를 통해 Kafka crawl 토픽에 발행합니다.
// 체이닝 Job(크롤된 페이지에서 발견된 URL)은 internal/publisher 패키지가 담당합니다.
type Scheduler struct {
	entries []ScheduleEntry
	emitter Emitter
	log     *logger.Logger
	wg      sync.WaitGroup
}

// New는 새 Scheduler를 생성합니다.
func New(
	entries []ScheduleEntry,
	emitter Emitter,
	log *logger.Logger,
) *Scheduler {
	return &Scheduler{
		entries: entries,
		emitter: emitter,
		log:     log,
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

// run은 단일 ScheduleEntry에 대한 스케줄 루프입니다.
func (s *Scheduler) run(ctx context.Context, entry ScheduleEntry) {
	defer s.wg.Done()

	// 시작 시 즉시 1회 실행
	s.publish(ctx, entry)

	ticker := time.NewTicker(entry.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.publish(ctx, entry)
		}
	}
}

// publish는 CrawlJob을 생성하고 Kafka crawl 토픽에 발행합니다.
func (s *Scheduler) publish(ctx context.Context, entry ScheduleEntry) {
	job := &core.CrawlJob{
		ID:          newJobID(),
		CrawlerName: entry.CrawlerName,
		Target: core.Target{
			URL:  entry.URL,
			Type: entry.TargetType,
		},
		Priority:    entry.Priority,
		ScheduledAt: time.Now(),
		Timeout:     entry.Timeout,
		MaxRetries:  3,
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
func newJobID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
