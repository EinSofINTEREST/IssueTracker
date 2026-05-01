// 본 파일은 validate 단계의 processor.Stage 래퍼를 제공합니다 (이슈 #206).
//
// validate 는 단일 worker 만 갖는 단순 stage — Worker.Start/Stop 을 그대로 위임.

package validate

import (
	"context"
	"errors"

	"issuetracker/internal/processor"
)

// stageName 은 validate 단계의 식별자입니다 (locks.StageValidator 와 일치).
const stageName = "validate"

// Stage 는 validate.Worker 를 processor.Stage 인터페이스로 wrapping 합니다.
type Stage struct {
	worker *Worker
}

// NewStage 는 wired Worker 를 받아 validate.Stage 를 반환합니다.
// worker 가 nil 이면 error — 호출자 (cmd/main) 가 boot fatal 처리 (이슈 #208).
func NewStage(worker *Worker) (*Stage, error) {
	if worker == nil {
		return nil, errors.New("validate: NewStage requires non-nil Worker")
	}
	return &Stage{worker: worker}, nil
}

// Name 은 stage 식별자 ("validate") 를 반환합니다.
func (s *Stage) Name() string { return stageName }

// Start 는 validate worker pool 을 기동합니다.
func (s *Stage) Start(ctx context.Context) {
	s.worker.Start(ctx)
}

// Stop 은 validate worker 의 graceful shutdown 을 수행합니다.
func (s *Stage) Stop(ctx context.Context) error {
	return s.worker.Stop(ctx)
}

// 컴파일 타임 인터페이스 만족 검증.
var _ processor.Stage = (*Stage)(nil)
