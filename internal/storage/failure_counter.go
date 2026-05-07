package storage

import "context"

// FailureReason 은 카운터에 누적되는 실패의 분류입니다.
//
// 카운터 자체는 reason 별 분리 없이 host 단위로 누적 — reason 은 audit log / metric 차원.
type FailureReason string

const (
	// FailureReasonRuleParseFailure: rule.Error 의 parse_failure / empty_selector 류 (selector 매칭 0건 / required selector 부재).
	FailureReasonRuleParseFailure FailureReason = "rule_parse_failure"
	// FailureReasonRuleNoRule: rule.Error 의 no_rule (host 에 active rule 없음).
	// chromedp 자동 전환 카운팅 대상에서 제외 권장 — 이 경우는 LLM 자동 rule 생성의 책임 영역.
	// 운영자 분석을 위해 호출자가 선택 가능하도록 reason 정의는 유지.
	FailureReasonRuleNoRule FailureReason = "rule_no_rule"
	// FailureReasonEmptyBody: parse 자체는 성공했지만 Title / MainContent 텍스트 길이가 임계값 미달.
	// FetcherAutoUpgradeConfig.EmptyBodyTitleMin / EmptyBodyContentMin 으로 임계값 운영.
	FailureReasonEmptyBody FailureReason = "empty_body"
)

// FailureCounter 는 host 단위 fetcher 실패를 sliding window 로 카운팅합니다.
//
// 임계값 도달 시 chromedp 자동 전환 트리거 입력으로 사용됩니다.
//
// 모든 구현체는 goroutine-safe 해야 합니다 — 다중 worker 가 동시에 Record 호출 가능.
type FailureCounter interface {
	// Record 는 host 의 실패 1건을 누적하고 현재 window 내 카운트 + 임계값 도달 여부를 반환합니다.
	// reason 은 audit / metric 용 (구현체가 카운터에 분리 저장해도, 분리 안 해도 무방).
	// 카운팅 자체가 실패 (Redis 장애 등) 하면 error 반환 — 호출자가 graceful 분기.
	Record(ctx context.Context, host string, reason FailureReason) (count int, thresholdReached bool, err error)
}

// noopFailureCounter 는 카운팅 비활성 시 사용되는 noop 구현입니다.
type noopFailureCounter struct{}

// NewNoopFailureCounter 는 항상 (0, false, nil) 을 반환하는 FailureCounter 를 반환합니다.
func NewNoopFailureCounter() FailureCounter { return noopFailureCounter{} }

func (noopFailureCounter) Record(_ context.Context, _ string, _ FailureReason) (int, bool, error) {
	return 0, false, nil
}
