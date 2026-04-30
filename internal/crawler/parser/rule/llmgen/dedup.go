package llmgen

import (
	"sync"

	"issuetracker/internal/storage"
)

// inflightKey 는 in-flight LLM 요청을 식별하는 (host, target_type) 튜플입니다.
type inflightKey struct {
	host       string
	targetType storage.TargetType
}

// inflightSet 은 동일 (host, type) 에 대해 동시에 진행 중인 LLM 호출을 1회로 제한합니다.
//
// inflightSet provides best-effort in-process dedup. 새 host 에 대해 100개 article URL 이
// 동시에 들어와도 LLM 호출은 1회만 발생하도록 보장 (이슈 #149 의 안전망).
//
// **Best-effort 한계**: 본 set 은 단일 process 내 상태 — 여러 instance 가 같은 host 를
// 동시에 처리하면 instance 수만큼 호출 발생 가능. 분산 dedup 은 후속 PR (Redis lock).
//
// 모든 메소드는 goroutine-safe.
type inflightSet struct {
	mu      sync.Mutex
	pending map[inflightKey]struct{}
}

// newInflightSet 은 빈 inflightSet 을 반환합니다.
func newInflightSet() *inflightSet {
	return &inflightSet{pending: make(map[inflightKey]struct{})}
}

// tryAcquire 는 (host, type) 의 LLM 호출 슬롯을 획득합니다.
//
// 반환값 acquired=true: 호출자가 LLM 호출을 진행 + 종료 후 release 호출 책임.
// 반환값 acquired=false: 다른 goroutine 이 이미 동일 key 를 처리 중 — 호출자는 skip.
func (s *inflightSet) tryAcquire(host string, t storage.TargetType) bool {
	key := inflightKey{host: host, targetType: t}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.pending[key]; exists {
		return false
	}
	s.pending[key] = struct{}{}
	return true
}

// release 는 acquire 한 (host, type) 슬롯을 해제합니다.
// tryAcquire 가 false 를 반환한 호출자는 release 를 호출하면 안 됩니다 (다른 owner 의 슬롯 침범).
func (s *inflightSet) release(host string, t storage.TargetType) {
	key := inflightKey{host: host, targetType: t}
	s.mu.Lock()
	delete(s.pending, key)
	s.mu.Unlock()
}
