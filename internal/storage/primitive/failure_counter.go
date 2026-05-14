// Package primitive 는 Redis-backed 단순 키-값 / 카운터 / 큐 primitive 인터페이스를 정의합니다.
//
// 구현체는 internal/storage/redis/ 에 위치합니다.
// 본 패키지는 의존성이 없으며 (context, errors 등 std 외), 호출자는 본 인터페이스를 통해
// 동작에만 의존합니다.
package primitive

import "context"

// FailureReason 은 카운터에 누적되는 실패의 분류입니다.
//
// 카운터 자체는 reason 별 분리 없이 host 단위로 누적 — reason 은 audit log / metric 차원.
type FailureReason string

const (
	FailureReasonRuleParseFailure FailureReason = "rule_parse_failure"
	FailureReasonRuleNoRule       FailureReason = "rule_no_rule"
	FailureReasonEmptyBody        FailureReason = "empty_body"
)

// FailureCounter 는 host 단위 fetcher 실패를 sliding window 로 카운팅합니다.
//
// 임계값 도달 시 chromedp 자동 전환 트리거 입력으로 사용됩니다.
// 모든 구현체는 goroutine-safe 해야 합니다.
type FailureCounter interface {
	// Record 는 host 의 실패 1건을 누적하고 현재 window 내 카운트 + 임계값 도달 여부를 반환합니다.
	Record(ctx context.Context, host string, reason FailureReason) (count int, thresholdReached bool, err error)
}

type noopFailureCounter struct{}

// NewNoopFailureCounter 는 항상 (0, false, nil) 을 반환하는 FailureCounter 를 반환합니다.
func NewNoopFailureCounter() FailureCounter { return noopFailureCounter{} }

func (noopFailureCounter) Record(_ context.Context, _ string, _ FailureReason) (int, bool, error) {
	return 0, false, nil
}
