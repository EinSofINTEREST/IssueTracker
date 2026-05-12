package core

import "context"

// inbox_headers.go — Kafka 메시지 헤더의 stage 간 전파 (이슈 #366).
//
// fetcher / parser / validate stage 가 동일 패턴으로 incoming msg.Headers 를 ctx 에 attach
// 한 후, 발행 시점에 headers 를 base 로 복사하여 stage 간 헤더 전파 (예: validate_reparse_*,
// trace ID 등) 를 보장합니다.
//
// 헤더를 ctx 로 옮기는 이유: JobHandler.Handle / ChainHandler.publishFetchedRef 등 내부 함수가
// msg 자체를 받지 않고 job 만 받는 인터페이스 — 인터페이스 변경 없이 헤더 전파.

type inboxHeadersKey struct{}

// WithInboxHeaders 는 ctx 에 incoming msg headers 를 첨부한 새 ctx 를 반환합니다.
//
// headers 가 nil 이거나 빈 map 이면 ctx 그대로 반환 — None Object 패턴.
// 호출자는 stage 진입 직후 (pool / consumer / worker) 본 함수로 ctx 보강.
func WithInboxHeaders(ctx context.Context, headers map[string]string) context.Context {
	if len(headers) == 0 {
		return ctx
	}
	// 호출자가 msg.Headers 를 변경할 경우 ctx 가 영향받지 않도록 shallow copy.
	copied := make(map[string]string, len(headers))
	for k, v := range headers {
		copied[k] = v
	}
	return context.WithValue(ctx, inboxHeadersKey{}, copied)
}

// InboxHeadersFromContext 는 ctx 에서 incoming msg headers 를 추출합니다.
//
// 부재 시 nil 반환 — 호출자가 분기 또는 빈 map fallback 처리.
// 반환된 map 은 ctx 에 저장된 인스턴스의 참조이므로 호출자가 변경하면 안 됨 (read-only).
func InboxHeadersFromContext(ctx context.Context) map[string]string {
	v, _ := ctx.Value(inboxHeadersKey{}).(map[string]string)
	return v
}

// propagatedInboxHeaderKeys 는 stage 간 전파할 헤더 키의 화이트리스트입니다 (이슈 #366 gemini 반영).
//
// 화이트리스트 방식 채택 이유:
//   - 의도된 헤더만 전파 — Rule 2 (의도적 설정) 원칙 부합
//   - 새 헤더 추가 시 명시적 코드 변경 — 운영 가시성 + 동적 / blacklist 누락 위험 회피
//
// 포함 항목:
//   - validate_reparse_count / validate_reparse_reason — validator → parser 재학습 cycle (#363)
//   - x-trace-id / x-request-id — observability (분산 추적 메타데이터)
//
// 호출자 (publishFetchedRef / publishContents) 가 본 슬라이스를 iterate 하여 incoming 헤더 값
// 존재 시 outgoing 헤더에 복사.
var propagatedInboxHeaderKeys = []string{
	HeaderValidateReparseCount,
	HeaderValidateReparseReason,
	"x-trace-id",
	"x-request-id",
}

// PropagateInboxHeaders 는 ctx 의 inbox headers 중 화이트리스트 키를 outgoing headers map 에
// 복사합니다 (이슈 #366).
//
// 기존 outgoing 의 동일 키는 덮어쓰지 않음 — 호출자의 명시적 설정 우선 (예: target_type 을
// 호출자가 새로 설정한 경우 inbox 의 값 무시).
func PropagateInboxHeaders(ctx context.Context, outgoing map[string]string) {
	inbox := InboxHeadersFromContext(ctx)
	if inbox == nil || outgoing == nil {
		return
	}
	for _, k := range propagatedInboxHeaderKeys {
		if _, set := outgoing[k]; set {
			continue
		}
		if v := inbox[k]; v != "" {
			outgoing[k] = v
		}
	}
}
