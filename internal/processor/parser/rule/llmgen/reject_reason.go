package llmgen

import "context"

// reject_reason.go — validator → parser 재학습 cycle 의 reason 컨텍스트 전파 (이슈 #365).
//
// validator 가 reject 한 사유 (예: "PublishedAt required", "Title min_length") 를 ctx 에
// 실어 보내 claudegen 의 LLM prompt 가 본 reason 을 컨텍스트로 활용 — multi-turn agent 가
// reason 기반으로 selector 보강 또는 validity=blacklist 결정.
//
// 인터페이스 시그니처 (SelectorExtractor / EnrichedExtractor) 변경 없이 ctx value 만으로 옵션
// 의미 자연스럽게 표현 — 기존 호출자 (Gemini provider 등 reason 무시 backend) 영향 없음.

type rejectReasonKey struct{}

// WithRejectReason 은 ctx 에 validator rejection reason 을 첨부한 새 ctx 를 반환합니다.
//
// reason 빈 문자열이면 ctx 그대로 반환 — 명시적 unset 호출 케이스 회피 (None Object 패턴).
// 호출자는 ProcessMessage / Enqueue 입구에서 reparse 헤더 감지 시 본 함수로 ctx 보강.
func WithRejectReason(ctx context.Context, reason string) context.Context {
	if reason == "" {
		return ctx
	}
	return context.WithValue(ctx, rejectReasonKey{}, reason)
}

// RejectReasonFromContext 는 ctx 에서 validator rejection reason 을 추출합니다.
//
// 부재 시 ("", false) 반환 — 호출자가 분기 (예: prompt placeholder 를 빈 문자열로 채움) 가능.
// 정상 경로 (reparse 가 아닌 첫 시도) 에서는 항상 ("", false) — 옵션 의미 보존.
func RejectReasonFromContext(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(rejectReasonKey{}).(string)
	if !ok || v == "" {
		return "", false
	}
	return v, true
}
