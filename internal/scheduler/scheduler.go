package scheduler

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"issuetracker/internal/crawler/core"
	"issuetracker/pkg/logger"
)

// Scheduler는 등록된 ScheduleEntry 목록을 기반으로 주기적으로 시드 CrawlJob을 생성하고
// Emitter를 통해 Kafka crawl 토픽에 발행합니다.
// 체이닝 Job(크롤된 페이지에서 발견된 URL)은 internal/publisher 패키지가 담당합니다.
type Scheduler struct {
	entries    []ScheduleEntry
	emitter    Emitter
	log        *logger.Logger
	wg         sync.WaitGroup
	maxRetries int
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
