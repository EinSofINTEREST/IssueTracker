package locks

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"time"

	"issuetracker/pkg/logger"
)

// stageGateLockReleaseTimeout 은 release 시 lock.Release 호출에 적용되는 best-effort timeout 입니다.
//
// Redis / network 지연 시 worker 의 semaphore slot 이 장기 점유되는 것을 방지 (Copilot 반영).
// 5s 면 일반적 Redis lock release 보다 훨씬 여유 — 진짜 stall 만 cap.
const stageGateLockReleaseTimeout = 5 * time.Second

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
	if isNilInterface(sem) {
		panic("locks: NewStageGate requires non-nil semaphore")
	}
	if isNilInterface(lock) {
		panic("locks: NewStageGate requires non-nil lock")
	}
	if log == nil {
		panic("locks: NewStageGate requires non-nil logger")
	}
	return &stageGate{stage: stage, sem: sem, lock: lock, log: log}
}

// isNilInterface 는 nil interface (untyped) 와 typed-nil (예: var p *T; var i I = p) 양쪽을 감지합니다.
//
// Copilot 반영 — 단순 \`v == nil\` 검사는 typed-nil 을 못 잡아 Acquire 호출 시 nil pointer
// deref 로 늦은 panic 발생. constructor 에서 reflect 로 한 번만 검사 (hot path 아님).
func isNilInterface(v interface{}) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Ptr, reflect.Interface, reflect.Chan, reflect.Func, reflect.Map, reflect.Slice:
		return rv.IsNil()
	}
	return false
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

	// 3. 성공 — release 함수 반환 (idempotent + goroutine-safe).
	var released atomic.Bool
	release := func() {
		if !released.CompareAndSwap(false, true) {
			return // 이미 release 호출됨 — 중복 lock/sem Release 회피 (gemini High / Copilot 반영).
		}
		// 순서: semaphore 먼저 반납 (다른 worker 진입 가능하게) → lock Release 는 best-effort timeout (Copilot 반영).
		// 이전: lock.Release 가 Redis 지연 시 semaphore slot 장기 점유 → worker 정체.
		g.sem.Release()

		// drainCtx — parent ctx 의 trace ID / logger fields 보존 + 부모 cancel 영향 없이 timeout cap (gemini 반영).
		drainCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), stageGateLockReleaseTimeout)
		defer cancel()
		if err := g.lock.Release(drainCtx, key); err != nil {
			g.log.WithFields(map[string]interface{}{
				"stage": g.stage,
				"url":   url,
			}).WithError(err).Warn("stage gate lock release failed")
		}
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

// BuildStageGate 는 (stage, workerCount, configuredCap, procLock, log) 입력으로 StageGate
// 를 합성하는 wiring 헬퍼입니다 (이슈 #356 — DRY).
//
// 동작:
//   - procLock 이 nil → NoopStageGate 반환 (Redis 부재 등 graceful degrade)
//   - capacity = config.CapPerStage(workerCount, configuredCap) — 0 이하면 workerCount/2 자동.
//     semaphore 최소 1 보장.
//   - 정상 wiring 시 NewStageGate(stage, NewSemaphore(cap), procLock, log) 반환
//
// fetcher / parser / validator wiring 시 동일 헬퍼 사용 — capacity 정책 일관성.
// capacity 계산은 pkg/config.CapPerStage 의 정책을 호출자가 적용한 뒤 본 헬퍼에 전달:
// 본 헬퍼는 그 값으로 직접 semaphore 를 생성합니다.
func BuildStageGate(stage string, capacity int, procLock ProcessingLock, log *logger.Logger) StageGate {
	if procLock == nil {
		return NewNoopStageGate()
	}
	if capacity < 1 {
		capacity = 1
	}
	sem := NewSemaphore(capacity)
	return NewStageGate(stage, sem, procLock, log)
}

// ErrStageGateNil 은 nil StageGate 가 의도치 않게 사용될 때 식별용.
var ErrStageGateNil = errors.New("locks: nil stage gate")

// 컴파일 타임 contract 보증.
var (
	_ StageGate = (*stageGate)(nil)
	_ StageGate = NoopStageGate{}
)
