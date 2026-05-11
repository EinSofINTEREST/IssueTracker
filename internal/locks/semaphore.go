package locks

import (
	"context"
	"errors"
	"sync/atomic"

	xsem "golang.org/x/sync/semaphore"
)

// Semaphore 는 동시 실행 슬롯 수 (capacity) 를 cap 하는 간단한 동시성 제어 primitive 입니다.
//
// ProcessingLock 이 \"같은 URL 의 중복 처리 차단\" 을 담당한다면, Semaphore 는 \"stage 의 동시 처리
// 슬롯이 가용한가\" 를 담당. StageGate 가 두 컴포넌트를 합성하여 worker 입장에서 단일 API 로 노출.
//
// 모든 구현체는 goroutine-safe 해야 합니다.
type Semaphore interface {
	// Acquire 는 slot 을 1개 점유합니다. capacity 가 가득 차 있으면 다른 slot 이 release 될 때까지
	// block. ctx cancel/deadline 시 즉시 ctx.Err() 반환.
	Acquire(ctx context.Context) error

	// Release 는 slot 1개를 반납합니다. Acquire 없이 Release 호출은 panic 위험 — 호출자가
	// defer 로 1:1 보장해야 함.
	Release()

	// Capacity 는 총 slot 수 (생성 시 설정값) 를 반환합니다 — 운영 진단 / 로깅용.
	Capacity() int

	// InFlight 는 현재 Acquire 되어 있는 slot 수를 반환합니다 — 운영 진단 / 로깅용.
	// goroutine-safe 한 snapshot — 호출 직후의 값일 수 있음.
	InFlight() int
}

// weightedSemaphore 는 golang.org/x/sync/semaphore.Weighted 기반 구현체입니다.
//
// weight=1 단순 사용으로 capacity = 동시 슬롯 수. ctx 친화적 Acquire + Release 시맨틱은
// x/sync/semaphore 그대로.
type weightedSemaphore struct {
	sem      *xsem.Weighted
	capacity int
	inFlight atomic.Int64
}

// NewSemaphore 는 capacity slot 의 Semaphore 를 생성합니다.
//
// capacity < 1 이면 panic — wiring 실수 즉시 발견. 호출자가 0/음수 입력을 사전에 sanitize.
func NewSemaphore(capacity int) Semaphore {
	if capacity < 1 {
		panic("locks: NewSemaphore requires capacity >= 1")
	}
	return &weightedSemaphore{
		sem:      xsem.NewWeighted(int64(capacity)),
		capacity: capacity,
	}
}

func (w *weightedSemaphore) Acquire(ctx context.Context) error {
	if err := w.sem.Acquire(ctx, 1); err != nil {
		return err
	}
	w.inFlight.Add(1)
	return nil
}

func (w *weightedSemaphore) Release() {
	if w.inFlight.Load() <= 0 {
		// double-release 또는 acquire 없이 release — 호출 패턴 버그. defensive: 카운터만 비정상 방지.
		// x/sync/semaphore 자체는 panic 발생 가능 — 호출자가 1:1 보장해야 함.
		return
	}
	w.inFlight.Add(-1)
	w.sem.Release(1)
}

func (w *weightedSemaphore) Capacity() int { return w.capacity }

func (w *weightedSemaphore) InFlight() int { return int(w.inFlight.Load()) }

// ErrSemaphoreNil 은 nil Semaphore 가 의도치 않은 곳에서 사용될 때 호출자가 errors.Is 로 식별
// 가능하도록 노출 — wiring 단계에서 NewSemaphore 가 호출되지 않은 케이스 진단용.
var ErrSemaphoreNil = errors.New("locks: nil semaphore")

// 컴파일 타임 contract 보증.
var _ Semaphore = (*weightedSemaphore)(nil)
