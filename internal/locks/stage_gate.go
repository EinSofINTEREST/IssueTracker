package locks

import (
	"context"
	"errors"

	"issuetracker/pkg/logger"
)

// StageGate 는 stage 별 worker pool 진입 시 다음을 합성하여 단일 API 로 노출합니다 (이슈 #353/#354):
//
//  1. Semaphore — \"stage 의 동시 처리 슬롯이 가용한가\"
//  2. ProcessingLock(stage, url) — \"이 URL 이 현재 stage 에서 처리 중인가\"
//
// 두 컴포넌트는 단일 책임 (Single Responsibility) 으로 분리되어 있으며 StageGate 는 composition
// 으로 합성. worker 가 \`gate.Acquire(ctx, url)\` 한 번 호출로 stage 진입 검증 + slot 점유 +
// URL lock 획득을 모두 수행하고, 반환된 release 함수로 한 번에 정리.
//
// 동작:
//   - Semaphore.Acquire(ctx) — capacity 가 차 있으면 block, ctx cancel 시 즉시 반환
//   - ProcessingLock.Acquire — 이미 처리 중이면 (release=nil, acquired=false, err=nil) → 호출자 skip
//   - Lock 에러 시 — Semaphore.Release 후 (nil, false, err)
//   - 성공 시 — (release, true, nil) 반환, release 호출 시 Lock.Release + Semaphore.Release 양쪽 정리
//
// 모든 구현체는 goroutine-safe 해야 합니다.
type StageGate interface {
	// Acquire 는 stage 진입 권한을 획득합니다.
	//
	// release: 정리 함수 — 호출자가 defer 로 호출해야 함. acquired=false 또는 err 발생 시 nil.
	//          idempotent — 두 번 호출되어도 panic 없이 안전.
	// acquired: true 면 진입 성공, false 면 lock 이 다른 worker 에 잡혀 있음 (skip 권장).
	// err: ctx cancel / 인프라 에러 등. graceful degrade 시 호출자 정책.
	Acquire(ctx context.Context, url string) (release func(), acquired bool, err error)
}

// stageGate 는 StageGate 의 기본 구현체입니다.
type stageGate struct {
	stage string
	sem   Semaphore
	lock  ProcessingLock
	log   *logger.Logger
}

// NewStageGate 는 (stage, semaphore, lock) 합성 StageGate 를 생성합니다.
//
// stage / semaphore / lock 모두 비-nil 필수. log 는 nil 이면 panic — wiring 실수 즉시 발견.
//
// stage 표준 값: StageFetcher / StageParser / StageValidator (processing_lock.go 의 상수).
func NewStageGate(stage string, sem Semaphore, lock ProcessingLock, log *logger.Logger) StageGate {
	if stage == "" {
		panic("locks: NewStageGate requires non-empty stage")
	}
	if sem == nil {
		panic("locks: NewStageGate requires non-nil semaphore")
	}
	if lock == nil {
		panic("locks: NewStageGate requires non-nil lock")
	}
	if log == nil {
		panic("locks: NewStageGate requires non-nil logger")
	}
	return &stageGate{stage: stage, sem: sem, lock: lock, log: log}
}

func (g *stageGate) Acquire(ctx context.Context, url string) (func(), bool, error) {
	// 1. Semaphore Acquire — capacity 한계 시 block, ctx 친화.
	if err := g.sem.Acquire(ctx); err != nil {
		return nil, false, err
	}

	// 2. ProcessingLock Acquire — 이미 처리 중이면 semaphore release 후 (nil, false, nil) 반환.
	key := ProcessingKey(g.stage, url)
	acquired, lockErr := g.lock.Acquire(ctx, key)
	if lockErr != nil {
		g.sem.Release()
		return nil, false, lockErr
	}
	if !acquired {
		g.sem.Release()
		return nil, false, nil
	}

	// 3. 성공 — release 함수 반환 (idempotent).
	released := false
	release := func() {
		if released {
			return
		}
		released = true
		// lock.Release 가 ctx cancel 등으로 실패해도 semaphore 는 반드시 반납.
		// release 자체는 호출자가 ctx.Err 무시하고 defer 로 호출하는 것을 전제 —
		// graceful shutdown 시 별도 drain ctx 사용 가능하도록 ctx 분리.
		drainCtx, cancel := context.WithCancel(context.Background())
		defer cancel()
		if err := g.lock.Release(drainCtx, key); err != nil {
			g.log.WithFields(map[string]interface{}{
				"stage": g.stage,
				"url":   url,
			}).WithError(err).Warn("stage gate lock release failed")
		}
		g.sem.Release()
	}
	return release, true, nil
}

// NoopStageGate 는 lock / semaphore 모두 비활성화된 no-op 구현체입니다.
//
// wiring 단계에서 Redis 미연결 / 운영자 명시적 비활성화 시 fallback. Acquire 는 항상
// (noopRelease, true, nil) — 모든 호출이 자유롭게 통과.
type NoopStageGate struct{}

// NewNoopStageGate 는 NoopStageGate 인스턴스를 반환합니다.
func NewNoopStageGate() StageGate { return NoopStageGate{} }

// Acquire 는 항상 (noopRelease, true, nil) 반환.
func (NoopStageGate) Acquire(_ context.Context, _ string) (func(), bool, error) {
	return func() {}, true, nil
}

// ErrStageGateNil 은 nil StageGate 가 의도치 않게 사용될 때 식별용.
var ErrStageGateNil = errors.New("locks: nil stage gate")

// 컴파일 타임 contract 보증.
var (
	_ StageGate = (*stageGate)(nil)
	_ StageGate = NoopStageGate{}
)
