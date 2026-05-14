// Package enrich 는 enrich 단계의 processor.Stage 래퍼를 제공합니다 (이슈 #445/#446).
//
// Stage 인터페이스는 fetcher / parser / validate 와 동일한 패턴 (Name / Start / Stop).
// 본 sub-issue (#446) 는 스켈레톤 — 실제 enrichment 로직은 후속 sub-issue 가 worker 내부에 채움.
package enrich

import (
	"context"
	"errors"

	"issuetracker/internal/processor"
	"issuetracker/internal/processor/enrich/worker"
)

// Stage 는 enrich worker 를 processor.Stage 인터페이스로 wrapping 합니다.
type Stage struct {
	worker *worker.Worker
}

// NewStage 는 wired Worker 를 받아 enrich.Stage 를 반환합니다.
// worker 가 nil 이면 error — 호출자 (cmd/main) 가 boot fatal 처리.
func NewStage(w *worker.Worker) (*Stage, error) {
	if w == nil {
		return nil, errors.New("enrich: NewStage requires non-nil Worker")
	}
	return &Stage{worker: w}, nil
}

// Name 은 stage 식별자 ("enricher") 를 반환합니다.
func (s *Stage) Name() string { return worker.StageName }

// Start 는 enrich worker pool 을 기동합니다.
func (s *Stage) Start(ctx context.Context) {
	s.worker.Start(ctx)
}

// Stop 은 enrich worker 의 graceful shutdown 을 수행합니다.
func (s *Stage) Stop(ctx context.Context) error {
	return s.worker.Stop(ctx)
}

// 컴파일 타임 인터페이스 만족 검증.
var _ processor.Stage = (*Stage)(nil)
