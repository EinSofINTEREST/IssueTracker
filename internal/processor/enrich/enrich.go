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
	intake *worker.ZSetIntake // nil 허용 — ZSET 인입 모드일 때만 (이슈 #524)
}

// NewStage 는 wired Worker 를 받아 enrich.Stage 를 반환합니다.
// worker 가 nil 이면 error — 호출자 (cmd/main) 가 boot fatal 처리.
func NewStage(w *worker.Worker) (*Stage, error) {
	if w == nil {
		return nil, errors.New("enrich: NewStage requires non-nil Worker")
	}
	return &Stage{worker: w}, nil
}

// SetZSetIntake 는 Kafka → ZSET 인입 단계 컴포넌트를 주입합니다 (이슈 #524 / 메타 #515 Phase 2).
//
// nil 주입 시 ZSET 모드 비활성 — Worker 가 일반 Kafka consumer 로 동작하는 경로.
// Start 호출 전 wiring 단계에서 1회 설정.
func (s *Stage) SetZSetIntake(intake *worker.ZSetIntake) {
	s.intake = intake
}

// Name 은 stage 식별자 ("enricher") 를 반환합니다.
func (s *Stage) Name() string { return worker.StageName }

// Start 는 enrich worker pool 을 기동합니다.
//
// 이슈 #524 — ZSET 인입 모드 활성 시 별도 goroutine 에서 Kafka → ZSET intake 동시 운용.
// ctx cancel 시 자연 종료 (별도 Stop 메소드 불필요).
func (s *Stage) Start(ctx context.Context) {
	s.worker.Start(ctx)
	if s.intake != nil {
		go s.intake.Run(ctx)
	}
}

// Stop 은 enrich worker 의 graceful shutdown 을 수행합니다.
func (s *Stage) Stop(ctx context.Context) error {
	return s.worker.Stop(ctx)
}

// 컴파일 타임 인터페이스 만족 검증.
var _ processor.Stage = (*Stage)(nil)
