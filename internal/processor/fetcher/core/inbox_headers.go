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
