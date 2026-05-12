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
		"crawler":                core.HeaderCrawler,
		"validate_reparse_count": "1",
	}
	enriched := core.WithInboxHeaders(ctx, headers)

	got := core.InboxHeadersFromContext(enriched)
	assert.Equal(t, "1", got["validate_reparse_count"])
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
