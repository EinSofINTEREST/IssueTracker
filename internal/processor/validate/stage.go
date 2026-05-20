// 본 파일은 validate 단계의 processor.Stage 래퍼를 제공합니다.
//
// validate 는 단일 worker 만 갖는 단순 stage — Worker.Start/Stop 을 그대로 위임.
// 패키지 구조 (이슈 #417): stage.go (top) + worker/ (worker pool 구현 + 부속 helpers) +
// types/ (cyclic-free 인터페이스) + domain/{community,news}/ (source-type 별 검증 — fetcher 의
// domain/* 패턴과 동일).

package validate

import (
	"context"
	"errors"

	"issuetracker/internal/processor"
	"issuetracker/internal/processor/validate/worker"
)

// Stage 는 worker.Worker 를 processor.Stage 인터페이스로 wrapping 합니다.
//
// 이슈 #523 — ZSET 인입 모드일 때 intake goroutine 을 Stage lifecycle 에 묶음.
type Stage struct {
	worker *worker.Worker
	intake *worker.ZSetIntake // nil 허용 — ZSET 모드일 때만
}

// NewStage 는 wired Worker 를 받아 validate.Stage 를 반환합니다.
// worker 가 nil 이면 error — 호출자 (cmd/main) 가 boot fatal 처리.
func NewStage(w *worker.Worker) (*Stage, error) {
	if w == nil {
		return nil, errors.New("validate: NewStage requires non-nil Worker")
	}
	return &Stage{worker: w}, nil
}

// SetZSetIntake 는 Kafka → ZSET 인입 단계 컴포넌트를 주입합니다 (이슈 #523 / 메타 #515 Phase 2).
//
// nil 주입 시 ZSET 모드 비활성 — Worker 가 일반 Kafka consumer 로 동작하는 경로.
// Start 호출 전 wiring 단계에서 1회 설정.
func (s *Stage) SetZSetIntake(intake *worker.ZSetIntake) {
	s.intake = intake
}

// Name 은 stage 식별자 ("validate") 를 반환합니다.
func (s *Stage) Name() string { return worker.StageName }

// Start 는 validate worker pool 을 기동합니다.
//
// 이슈 #523 — intake 주입 시 Kafka → ZSET intake goroutine 도 동시 기동. ctx cancel 시 자연 종료.
func (s *Stage) Start(ctx context.Context) {
	s.worker.Start(ctx)
	if s.intake != nil {
		go s.intake.Run(ctx)
	}
}

// Stop 은 validate worker 의 graceful shutdown 을 수행합니다.
func (s *Stage) Stop(ctx context.Context) error {
	return s.worker.Stop(ctx)
}

// 컴파일 타임 인터페이스 만족 검증.
var _ processor.Stage = (*Stage)(nil)
