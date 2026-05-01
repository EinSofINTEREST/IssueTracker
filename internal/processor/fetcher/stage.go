// Package fetcher 는 fetcher 단계의 processor.Stage 래퍼를 제공합니다 (이슈 #206).
//
// 디렉토리 정렬상 본 파일은 internal/processor/fetcher/ 의 entry-point — 하위 패키지
// (core / handler / domain / implementation / rate_limiter / worker) 의 wiring 결과를
// processor.Stage 인터페이스로 노출.
package fetcher

import (
	"context"

	"issuetracker/internal/processor"
	"issuetracker/internal/processor/fetcher/worker"
)

// stageName 은 fetcher 단계의 식별자입니다 (locks.StageFetcher 와 일치).
const stageName = "fetcher"

// Stage 는 PoolManager 를 processor.Stage 인터페이스로 wrapping 합니다.
//
// 본 wrapper 는 lifecycle 만 담당 — Pool 의 모든 wiring (resolver, retry scheduler 주입 등) 은
// 호출자 (cmd/issuetracker/main.go) 책임으로 유지하여 dependency injection 일관.
type Stage struct {
	manager *worker.PoolManager
}

// NewStage 는 wired PoolManager 를 받아 fetcher.Stage 를 반환합니다.
// manager 가 nil 이면 panic — wiring 누락 즉시 가시화.
func NewStage(manager *worker.PoolManager) *Stage {
	if manager == nil {
		panic("fetcher: NewStage requires non-nil PoolManager")
	}
	return &Stage{manager: manager}
}

// Name 은 stage 식별자 ("fetcher") 를 반환합니다.
func (s *Stage) Name() string { return stageName }

// Start 는 PoolManager 의 worker pool 을 기동합니다.
func (s *Stage) Start(ctx context.Context) {
	s.manager.Start(ctx)
}

// Stop 은 PoolManager 의 graceful shutdown 을 수행합니다.
func (s *Stage) Stop(ctx context.Context) error {
	return s.manager.Stop(ctx)
}

// 컴파일 타임 인터페이스 만족 검증.
var _ processor.Stage = (*Stage)(nil)
