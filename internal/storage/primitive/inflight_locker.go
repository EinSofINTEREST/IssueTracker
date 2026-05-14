package primitive

import (
	"context"
	"sync"

	"issuetracker/internal/storage/model"
)

// InflightLocker 는 (host, targetType) 단위 중복 실행 방지 인터페이스입니다.
//
// LLM 자동 룰 학습 등 비싼 작업이 동일 호스트에 대해 동시 다발 발생하는 것을 차단.
//
// 구현체:
//   - Mem  (primitive.NewMemInflightLocker): in-process map 기반 (단일 인스턴스 환경 default)
//   - Redis (redisstore.NewInflightLocker): Redis SETNX+TTL 기반 (다중 인스턴스 환경)
type InflightLocker interface {
	TryAcquire(ctx context.Context, host string, targetType model.TargetType) (acquired bool, err error)
	Release(ctx context.Context, host string, targetType model.TargetType) error
}

type inflightKey struct {
	host       string
	targetType model.TargetType
}

type memInflightLocker struct {
	mu      sync.Mutex
	pending map[inflightKey]struct{}
}

// NewMemInflightLocker 는 in-process map 기반 InflightLocker 를 생성합니다.
func NewMemInflightLocker() InflightLocker {
	return &memInflightLocker{pending: make(map[inflightKey]struct{})}
}

func (m *memInflightLocker) TryAcquire(_ context.Context, host string, targetType model.TargetType) (bool, error) {
	key := inflightKey{host: host, targetType: targetType}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.pending[key]; exists {
		return false, nil
	}
	m.pending[key] = struct{}{}
	return true, nil
}

func (m *memInflightLocker) Release(_ context.Context, host string, targetType model.TargetType) error {
	key := inflightKey{host: host, targetType: targetType}
	m.mu.Lock()
	delete(m.pending, key)
	m.mu.Unlock()
	return nil
}
