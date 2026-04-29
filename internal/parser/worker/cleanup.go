package worker

import (
	"context"
	"sync"
	"time"

	"issuetracker/internal/storage/service"
	"issuetracker/pkg/logger"
)

const (
	// DefaultCleanupInterval: cleanup 루프 주기.
	DefaultCleanupInterval = 30 * time.Minute

	// DefaultStaleRawTTL: parser worker 가 처리하지 못한 채 남은 raw_contents row 의 보존 기간.
	//
	// 정책 의도 (이슈 #134):
	//   - 정상 흐름에서는 parser worker 가 즉시 Delete 하므로 raw 가 누적되지 않음
	//   - 잔존하는 row 는 (1) parser crash, (2) rule.Error 로 잔존, (3) LLM 재처리 대기 윈도우
	//   - LLM 자동 rule 생성 (이슈 #149) 후 cleanup 이전에 reprocess 가능해야 의미 있음
	//   - 기본 1시간 — LLM rule 생성 + 재처리에 충분, 폭주 시 디스크 보호
	DefaultStaleRawTTL = 1 * time.Hour
)

// CleanupConfig 는 RawContentCleaner 의 동작을 제어합니다.
type CleanupConfig struct {
	// Interval: 루프 polling 주기. 0 이면 DefaultCleanupInterval 사용.
	Interval time.Duration
	// StaleTTL: cutoff = now - StaleTTL. 0 이면 DefaultStaleRawTTL 사용.
	StaleTTL time.Duration
}

// RawContentCleaner 는 parser worker 가 처리하지 못한 채 잔존한 raw_contents row 를
// 주기적으로 정리하는 안전망 goroutine 입니다 (이슈 #134).
//
// 정상 흐름에서는 parser worker 가 처리 직후 raw 를 삭제하므로 본 cleaner 의 동작은 거의 없음.
// row 가 남는 케이스:
//   - parser worker crash (Delete 호출 전)
//   - rule.Error 로 raw 잔존 (LLM 재처리 윈도우 — 이슈 #149)
//   - 일시적 DB 에러로 Delete 실패
//
// StaleTTL 보다 오래된 row 는 LLM 재처리에 더 이상 의미 없다고 판단하여 정리.
type RawContentCleaner struct {
	rawSvc service.RawContentService
	cfg    CleanupConfig
	log    *logger.Logger
	wg     sync.WaitGroup
}

// NewRawContentCleaner 는 RawContentCleaner 를 생성합니다.
// cfg 의 0 값 필드는 default 로 보정.
func NewRawContentCleaner(rawSvc service.RawContentService, cfg CleanupConfig, log *logger.Logger) *RawContentCleaner {
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultCleanupInterval
	}
	if cfg.StaleTTL <= 0 {
		cfg.StaleTTL = DefaultStaleRawTTL
	}
	return &RawContentCleaner{
		rawSvc: rawSvc,
		cfg:    cfg,
		log:    log,
	}
}

// Start 는 cleanup 루프를 별도 goroutine 으로 시작합니다 (non-blocking).
func (c *RawContentCleaner) Start(ctx context.Context) {
	c.wg.Add(1)
	go c.run(ctx)
	c.log.WithFields(map[string]interface{}{
		"interval_ms": c.cfg.Interval.Milliseconds(),
		"ttl_ms":      c.cfg.StaleTTL.Milliseconds(),
	}).Info("raw content cleaner started")
}

// Stop 은 goroutine 종료 대기 (ctx cancel 후 호출).
func (c *RawContentCleaner) Stop() {
	c.wg.Wait()
	c.log.Info("raw content cleaner stopped")
}

func (c *RawContentCleaner) run(ctx context.Context) {
	defer c.wg.Done()
	ticker := time.NewTicker(c.cfg.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.purgeOnce(ctx)
		}
	}
}

func (c *RawContentCleaner) purgeOnce(ctx context.Context) {
	cutoff := time.Now().Add(-c.cfg.StaleTTL)
	n, err := c.rawSvc.PurgeOlderThan(ctx, cutoff)
	if err != nil {
		// ctx cancel 로 인한 에러는 정상 종료 흐름 — DEBUG 강등
		if ctx.Err() != nil {
			c.log.WithError(err).Debug("raw content purge interrupted by shutdown")
			return
		}
		c.log.WithError(err).Warn("raw content purge failed")
		return
	}
	if n > 0 {
		c.log.WithFields(map[string]interface{}{
			"deleted_count": n,
			"cutoff":        cutoff.Format(time.RFC3339),
		}).Info("raw content cleanup removed stale rows")
	}
}
