// 본 파일은 validate 단계의 processor.Stage 래퍼를 제공합니다.
//
// validate 는 단일 worker 만 갖는 단순 stage — Worker.Start/Stop 을 그대로 위임.
// 패키지 구조 (이슈 #417): stage.go (top) + worker/ (worker pool 구현 + 부속 helpers) +
// types/ (cyclic-free 인터페이스) + community/ + news/.

package validate

import (
	"context"
	"errors"

	"issuetracker/internal/processor"
	"issuetracker/internal/processor/validate/worker"
)

// Stage 는 worker.Worker 를 processor.Stage 인터페이스로 wrapping 합니다.
type Stage struct {
	worker *worker.Worker
}

// NewStage 는 wired Worker 를 받아 validate.Stage 를 반환합니다.
// worker 가 nil 이면 error — 호출자 (cmd/main) 가 boot fatal 처리.
func NewStage(w *worker.Worker) (*Stage, error) {
	if w == nil {
		return nil, errors.New("validate: NewStage requires non-nil Worker")
	}
	return &Stage{worker: w}, nil
}

// Name 은 stage 식별자 ("validate") 를 반환합니다.
func (s *Stage) Name() string { return worker.StageName }

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
