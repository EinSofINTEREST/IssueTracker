package core_test

// inbox_headers_test.go — Sub C (#366) 의 inbox headers ctx helper 단위 테스트.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"issuetracker/internal/processor/fetcher/core"
)

func TestWithInboxHeaders_RoundTrip(t *testing.T) {
	ctx := context.Background()
	headers := map[string]string{
		core.HeaderCrawler:               "naver",
		core.HeaderValidateReparseCount:  "1",
		core.HeaderValidateReparseReason: "PublishedAt required",
	}
	enriched := core.WithInboxHeaders(ctx, headers)

	got := core.InboxHeadersFromContext(enriched)
	assert.Equal(t, "naver", got[core.HeaderCrawler])
	assert.Equal(t, "1", got[core.HeaderValidateReparseCount])
	assert.Equal(t, "PublishedAt required", got[core.HeaderValidateReparseReason])
}

func TestWithInboxHeaders_Nil_ReturnsSameCtx(t *testing.T) {
	ctx := context.Background()
	enriched := core.WithInboxHeaders(ctx, nil)
	assert.Equal(t, ctx, enriched, "nil headers → ctx 그대로 (None Object)")
	assert.Nil(t, core.InboxHeadersFromContext(enriched))
}

func TestWithInboxHeaders_Empty_ReturnsSameCtx(t *testing.T) {
	ctx := context.Background()
	enriched := core.WithInboxHeaders(ctx, map[string]string{})
	assert.Equal(t, ctx, enriched, "empty headers → ctx 그대로")
	assert.Nil(t, core.InboxHeadersFromContext(enriched))
}

func TestWithInboxHeaders_ShallowCopy(t *testing.T) {
	// 호출자가 원본 map 을 변경해도 ctx 가 영향받지 않는지 검증.
	ctx := context.Background()
	original := map[string]string{"key": "v1"}
	enriched := core.WithInboxHeaders(ctx, original)
	original["key"] = "v2" // 원본 변경

	got := core.InboxHeadersFromContext(enriched)
	assert.Equal(t, "v1", got["key"], "ctx 의 헤더는 원본 변경 영향 받지 않음 (shallow copy)")
}

func TestInboxHeadersFromContext_Absent_ReturnsNil(t *testing.T) {
	ctx := context.Background()
	assert.Nil(t, core.InboxHeadersFromContext(ctx))
}

func TestPropagateInboxHeaders_WhitelistedKeysOnly(t *testing.T) {
	ctx := core.WithInboxHeaders(context.Background(), map[string]string{
		core.HeaderValidateReparseCount:  "2",
		core.HeaderValidateReparseReason: "PublishedAt required",
		"x-trace-id":                     "trace-xyz",
		core.HeaderCrawler:               "naver",   // 화이트리스트 외 — 전파 X
		core.HeaderTargetType:            "article", // 화이트리스트 외 — 전파 X
	})
	outgoing := map[string]string{
		"source":  "test",
		"country": "KR",
	}
	core.PropagateInboxHeaders(ctx, outgoing)

	assert.Equal(t, "2", outgoing[core.HeaderValidateReparseCount])
	assert.Equal(t, "PublishedAt required", outgoing[core.HeaderValidateReparseReason])
	assert.Equal(t, "trace-xyz", outgoing["x-trace-id"])
	_, hasCrawler := outgoing[core.HeaderCrawler]
	assert.False(t, hasCrawler, "화이트리스트 외 키 propagation X")
	_, hasTargetType := outgoing[core.HeaderTargetType]
	assert.False(t, hasTargetType, "화이트리스트 외 키 propagation X")
}

func TestPropagateInboxHeaders_DoesNotOverwriteExisting(t *testing.T) {
	// 호출자의 명시적 설정이 inbox 값보다 우선 — Rule 2 (의도적 설정).
	ctx := core.WithInboxHeaders(context.Background(), map[string]string{
		core.HeaderValidateReparseCount: "5",
	})
	outgoing := map[string]string{
		core.HeaderValidateReparseCount: "999", // 호출자가 명시적 설정
	}
	core.PropagateInboxHeaders(ctx, outgoing)
	assert.Equal(t, "999", outgoing[core.HeaderValidateReparseCount], "기존 outgoing 키 덮어쓰기 X")
}

func TestPropagateInboxHeaders_NilCtx_NoOp(t *testing.T) {
	ctx := context.Background()
	outgoing := map[string]string{"existing": "v"}
	core.PropagateInboxHeaders(ctx, outgoing)
	assert.Equal(t, map[string]string{"existing": "v"}, outgoing, "inbox 부재 시 outgoing 변경 X")
}

func TestPropagateInboxHeaders_NilOutgoing_NoOp(t *testing.T) {
	// outgoing 이 nil 이면 nil map 에 write 시도 panic 회피.
	ctx := core.WithInboxHeaders(context.Background(), map[string]string{"x-trace-id": "t"})
	assert.NotPanics(t, func() {
		core.PropagateInboxHeaders(ctx, nil)
	})
}
