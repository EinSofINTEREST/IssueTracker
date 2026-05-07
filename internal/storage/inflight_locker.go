package storage

import (
	"context"
	"sync"
)

// InflightLocker 는 (host, targetType) 단위 중복 실행 방지 인터페이스입니다.
//
// LLM 자동 룰 학습 등 비싼 작업이 동일 호스트에 대해 동시 다발 발생하는 것을 차단.
//
// 구현체:
//   - Mem  (storage.NewMemInflightLocker): in-process map 기반 (단일 인스턴스 환경 default)
//   - Redis (redisstore.NewInflightLocker): Redis SETNX+TTL 기반 (다중 인스턴스 환경)
type InflightLocker interface {
	// TryAcquire 는 슬롯 획득을 시도합니다.
	// acquired=true: 호출자가 작업 진행 + 완료 후 Release 책임.
	// acquired=false: 다른 goroutine/인스턴스가 이미 처리 중 — skip.
	TryAcquire(ctx context.Context, host string, targetType TargetType) (acquired bool, err error)
	// Release 는 획득한 슬롯을 해제합니다.
	Release(ctx context.Context, host string, targetType TargetType) error
}

// inflightKey 는 in-process locker 의 map key 입니다.
type inflightKey struct {
	host       string
	targetType TargetType
}

// memInflightLocker 는 in-process map 기반 InflightLocker 구현입니다.
//
// 단일 인스턴스 환경에서 충분 — 다중 인스턴스 운영 시 redisstore.NewInflightLocker 권장.
type memInflightLocker struct {
	mu      sync.Mutex
	pending map[inflightKey]struct{}
}

// NewMemInflightLocker 는 in-process map 기반 InflightLocker 를 생성합니다.
func NewMemInflightLocker() InflightLocker {
	return &memInflightLocker{pending: make(map[inflightKey]struct{})}
}

func (m *memInflightLocker) TryAcquire(_ context.Context, host string, targetType TargetType) (bool, error) {
	key := inflightKey{host: host, targetType: targetType}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.pending[key]; exists {
		return false, nil
	}
	m.pending[key] = struct{}{}
	return true, nil
}

func (m *memInflightLocker) Release(_ context.Context, host string, targetType TargetType) error {
	key := inflightKey{host: host, targetType: targetType}
	m.mu.Lock()
	delete(m.pending, key)
	m.mu.Unlock()
	return nil
}
