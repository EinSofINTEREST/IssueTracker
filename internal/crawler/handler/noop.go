package handler

import (
	"context"

	"issuetracker/internal/crawler/core"
	"issuetracker/pkg/logger"
)

// noopHandler는 실제 크롤러 구현 전 사용되는 fallback 핸들러입니다.
// job을 수신하면 로그만 남기고 nil을 반환합니다.
type noopHandler struct {
	log *logger.Logger
}

func (h *noopHandler) Handle(_ context.Context, job *core.CrawlJob) ([]*core.Content, error) {
	h.log.WithFields(map[string]interface{}{
		"job_id":  job.ID,
		"crawler": job.CrawlerName,
		"url":     job.Target.URL,
	}).Info("job received (no handler registered yet)")

	return nil, nil
}
