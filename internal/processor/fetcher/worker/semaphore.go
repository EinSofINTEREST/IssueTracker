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
	// 매칭 Acquire 없이 호출 시 ErrReleaseWithoutAcquire — 호출자가 contract 위반을 log/audit.
	// (production panic 금지 정책 — 이슈 #208 / 04-error-handling.md.)
	Release() error

	// Capacity 는 동시 보유 가능한 최대 슬롯 수를 반환합니다.
	Capacity() int
}

// ErrReleaseWithoutAcquire 는 Release() 가 매칭 Acquire 없이 호출됐을 때 반환됩니다.
//
// production panic 금지 (이슈 #208) 에 따라 panic 대신 sentinel error — 호출자가 sentry / log 로
// contract 위반 알림. 정상 흐름에서는 발생 안 함 (defer Release 패턴 사용).
var ErrReleaseWithoutAcquire = errors.New("worker: Semaphore.Release called without matching Acquire")

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
// 매칭 Acquire 없이 호출되면 ErrReleaseWithoutAcquire 반환 — 호출자가 log 로 contract 위반
// 알림. production panic 금지 (이슈 #208) 에 따라 panic 대신 error.
func (s *chanSemaphore) Release() error {
	select {
	case <-s.slots:
		return nil
	default:
		return ErrReleaseWithoutAcquire
	}
}

// Capacity 는 동시 보유 가능한 최대 슬롯 수를 반환합니다.
func (s *chanSemaphore) Capacity() int { return s.capacity }
