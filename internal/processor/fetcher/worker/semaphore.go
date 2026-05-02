package worker

import (
	"context"
	"errors"
)

// Semaphore 는 N 개의 동시 진입을 허용하는 counting semaphore 입니다 (이슈 #218).
//
// chromedp worker pool 이 Chrome 인스턴스의 동시 navigation 수를 제한하기 위해 사용 — 같은
// Chrome 의 URLLoader / ResourceScheduler 가 ERR_INSUFFICIENT_RESOURCES 로 거부되는 빈도를
// 직접 차단. capacity 는 운영 시 Chrome 의 동시 처리 한계 (보통 4-6) 에 맞춰 조정.
//
// 동시 사용 안전 — channel 기반 구현이라 lock-free.
type Semaphore interface {
	// Acquire 는 슬롯 1개를 점유합니다. 슬롯 부족 시 대기. ctx 가 cancel 되면 ctx.Err() 반환.
	// 성공 시 nil — 호출자는 작업 완료 후 반드시 Release 호출 (defer 권장).
	Acquire(ctx context.Context) error

	// Release 는 슬롯 1개를 반납합니다. Acquire 와 1:1 매핑.
	Release()

	// Capacity 는 동시 보유 가능한 최대 슬롯 수를 반환합니다.
	Capacity() int
}

// chanSemaphore 는 buffered channel 기반 Semaphore 구현입니다.
type chanSemaphore struct {
	slots    chan struct{}
	capacity int
}

// NewSemaphore 는 capacity 만큼의 동시 진입을 허용하는 Semaphore 를 생성합니다.
//
// capacity <= 0 이면 error (이슈 #208 정책).
func NewSemaphore(capacity int) (Semaphore, error) {
	if capacity <= 0 {
		return nil, errors.New("worker: NewSemaphore requires positive capacity")
	}
	return &chanSemaphore{
		slots:    make(chan struct{}, capacity),
		capacity: capacity,
	}, nil
}

// Acquire 는 ctx 만료 전까지 슬롯을 대기합니다.
func (s *chanSemaphore) Acquire(ctx context.Context) error {
	select {
	case s.slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Release 는 슬롯 1개를 반납합니다 (Acquire 후 1:1 매핑).
//
// 슬롯 반납 시 점유 중이지 않으면 panic — 호출자가 Acquire 와 1:1 매칭 보장 책임.
// (Go 의 nil send-on-closed-channel 같은 panic 보다 일찍 발견 가능.)
func (s *chanSemaphore) Release() {
	select {
	case <-s.slots:
	default:
		panic("worker: Semaphore.Release called without matching Acquire")
	}
}

// Capacity 는 동시 보유 가능한 최대 슬롯 수를 반환합니다.
func (s *chanSemaphore) Capacity() int { return s.capacity }
