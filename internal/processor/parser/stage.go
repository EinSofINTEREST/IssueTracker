// Package parser 는 parser 단계의 processor.Stage 래퍼를 제공합니다.
//
// 패키지 구조 (이슈 #417): stage.go (top, 본 파일) + worker/ + types/ + rule/.
// 핵심 도메인 인터페이스 (ContentParser / LinkListParser / Page / LinkItem) 는
// parser/types/ leaf sub-package 에 위치하여 cyclic dependency 회피 — rule/* 가
// parser/types 만 import 하면 되므로 parser 부모 패키지 (본 파일) 가 rule/* 를 import 해도
// 사이클 없음.
//
// 본 stage 는 단일 worker 가 아닌 **여러 background goroutine 의 묶음** 입니다:
//   - worker.Worker (Kafka consume + 파싱 + content 저장)
//   - worker.RawContentCleaner (잔존 raw_contents purge cron)
//   - llmgen.Generator (ErrNoRule 시 async LLM 호출, optional)
//   - refiner.Refiner (path_pattern 정밀화 polling, optional)
//
// 모든 component 의 lifecycle 을 본 wrapper 가 단일 Start/Stop 으로 통합.
// llmGen / refiner 는 nil 허용 (LLM 비활성 / REFINEMENT_ENABLED=false 환경 cover).
package parser

import (
	"context"
	"errors"

	"issuetracker/internal/processor"
	"issuetracker/internal/processor/parser/rule/llmgen"
	"issuetracker/internal/processor/parser/rule/refiner"
	"issuetracker/internal/processor/parser/worker"
	"issuetracker/pkg/logger"
)

// stageName 은 parser 단계의 식별자입니다 (locks.StageParser 와 일치).
const stageName = "parser"

// Stage 는 parser 단계의 모든 background goroutine 을 묶어 processor.Stage 로 노출합니다.
//
// component 의존 order (Stop 시 역순 처리):
//  1. Worker — Kafka consumer + 파싱 (필수)
//  2. RawContentCleaner — janitor (필수, Stop 은 ctx 무시)
//  3. llmgen.Generator (선택) — ErrNoRule async LLM 호출. Worker 가 유일 Enqueue source 이므로
//     Worker.Stop 후에 Stop 해야 in-flight LLM 호출 완료 보장.
//  4. refiner.Refiner (선택) — interval polling. ctx cancel 시 cycle 즉시 종료.
type Stage struct {
	worker  *worker.Worker
	cleaner *worker.RawContentCleaner
	llmGen  *llmgen.Generator // nil 허용
	refiner *refiner.Refiner  // nil 허용
	log     *logger.Logger
}

// NewStage 는 component 들을 받아 parser.Stage 를 반환합니다.
// worker / cleaner / log 는 필수 (nil 이면 error), llmGen / refiner 는 nil 허용.
func NewStage(
	w *worker.Worker,
	cleaner *worker.RawContentCleaner,
	llmGen *llmgen.Generator,
	pathRefiner *refiner.Refiner,
	log *logger.Logger,
) (*Stage, error) {
	if w == nil {
		return nil, errors.New("parser: NewStage requires non-nil Worker")
	}
	if cleaner == nil {
		return nil, errors.New("parser: NewStage requires non-nil RawContentCleaner")
	}
	if log == nil {
		return nil, errors.New("parser: NewStage requires non-nil logger")
	}
	return &Stage{
		worker:  w,
		cleaner: cleaner,
		llmGen:  llmGen,
		refiner: pathRefiner,
		log:     log,
	}, nil
}

// Name 은 stage 식별자 ("parser") 를 반환합니다.
func (s *Stage) Name() string { return stageName }

// Start 는 parser 단계의 모든 background goroutine 을 기동합니다.
//
//   - Worker: consumer pool start
//   - RawContentCleaner: cron start
//   - refiner: polling goroutine start (있으면)
//
// llmgen.Generator 는 별도 Start 메소드 없음 — Enqueue 호출 시점에 lazy 로 처리 goroutine 생성.
func (s *Stage) Start(ctx context.Context) {
	s.worker.Start(ctx)
	s.cleaner.Start(ctx)
	if s.refiner != nil {
		s.refiner.Start(ctx)
	}
}

// Stop 은 parser 단계의 graceful shutdown 을 수행합니다.
//
// 처리 순서가 중요:
//  1. Worker 먼저 — llmGen 의 유일 Enqueue source 차단
//  2. llmGen — in-flight LLM 호출 완료 대기
//  3. refiner — in-flight polling cycle 완료 대기
//  4. RawContentCleaner — janitor 마지막
//
// 첫 번째 발생한 에러를 반환 (나머지는 log 에 남김). 호출자가 ctx.WithTimeout 으로 강제 종료 시간 제어.
func (s *Stage) Stop(ctx context.Context) error {
	var firstErr error

	// 1. Worker — Enqueue source 차단. 에러는 호출자 (main) 에서 stage 별로 일괄 로깅하므로
	// 본 위치에서는 중복 로그 회피 — 단순 first-error 보존만.
	if err := s.worker.Stop(ctx); err != nil {
		firstErr = err
	}

	// 2. llmGen — 새 Enqueue 차단된 시점 이후 in-flight 완료 대기
	if s.llmGen != nil {
		s.llmGen.Stop(ctx)
	}

	// 3. refiner — in-flight polling cycle 완료 대기
	if s.refiner != nil {
		s.refiner.Stop(ctx)
	}

	// 4. RawContentCleaner — 시그니처가 ctx 를 받지 않아 별도 goroutine + select 로 caller 의
	// timeout 을 honor. janitor 라 ctx cancel 시 firstErr 만 기록하고
	// 강제 반환 — 본 stop 은 best-effort.
	cleanerStopped := make(chan struct{})
	go func() {
		s.cleaner.Stop()
		close(cleanerStopped)
	}()
	select {
	case <-cleanerStopped:
	case <-ctx.Done():
		s.log.WithError(ctx.Err()).Warn("raw content cleaner stop canceled by ctx")
		if firstErr == nil {
			firstErr = ctx.Err()
		}
	}

	return firstErr
}

// 컴파일 타임 인터페이스 만족 검증.
var _ processor.Stage = (*Stage)(nil)
