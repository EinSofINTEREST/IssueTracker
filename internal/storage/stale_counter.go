package storage

import "context"

// StaleCounter 는 stale rule 발생을 (host, target_type) 단위 sliding window 로 카운팅합니다.
//
// FailureCounter (chromedp 자동 전환용) 와 별개의 keyspace / 임계값 보유 — chromedp 가 먼저
// 시도된 후에도 실패가 지속되면 LLM 재학습이 트리거되도록 보수적 정책 적용.
//
// thresholdReached=true 시 호출자가 LLM 재학습을 enqueue.
// 모든 구현체는 goroutine-safe.
type StaleCounter interface {
	// Record 는 (host, target_type) 의 stale parse failure 1건을 누적합니다.
	// 반환: (count 누적값, thresholdReached 임계 도달 여부, err 카운팅 실패).
	Record(ctx context.Context, host string, targetType TargetType) (count int, thresholdReached bool, err error)
}

// noopStaleCounter 는 stale 카운팅 비활성 시 사용되는 noop 구현입니다.
type noopStaleCounter struct{}

// NewNoopStaleCounter 는 항상 (0, false, nil) 을 반환하는 StaleCounter 를 반환합니다.
func NewNoopStaleCounter() StaleCounter { return noopStaleCounter{} }

func (noopStaleCounter) Record(_ context.Context, _ string, _ TargetType) (int, bool, error) {
	return 0, false, nil
}
