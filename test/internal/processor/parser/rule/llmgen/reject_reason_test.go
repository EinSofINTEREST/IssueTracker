package llmgen_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"issuetracker/internal/processor/parser/rule/llmgen"
)

// reject_reason_test.go — Sub B (#365) — ctx-based RejectReason 메커니즘 단위 테스트.

func TestWithRejectReason_RoundTrip(t *testing.T) {
	ctx := context.Background()
	enriched := llmgen.WithRejectReason(ctx, "PublishedAt required")

	got, ok := llmgen.RejectReasonFromContext(enriched)
	assert.True(t, ok)
	assert.Equal(t, "PublishedAt required", got)
}

func TestWithRejectReason_EmptyString_ReturnsSameCtx(t *testing.T) {
	ctx := context.Background()
	enriched := llmgen.WithRejectReason(ctx, "")
	assert.Equal(t, ctx, enriched, "empty reason 시 ctx 그대로 반환 — None Object 패턴")

	got, ok := llmgen.RejectReasonFromContext(enriched)
	assert.False(t, ok)
	assert.Equal(t, "", got)
}

func TestWithRejectReason_WhitespaceOnly_ReturnsSameCtx(t *testing.T) {
	// gemini 반영 PR #368 — 공백만 포함된 reason 은 부재로 분류하여 공허한 feedback 블록 차단.
	for _, raw := range []string{"   ", "\t\n", " \n ", "\r\n\t"} {
		t.Run("raw="+raw, func(t *testing.T) {
			ctx := context.Background()
			enriched := llmgen.WithRejectReason(ctx, raw)
			assert.Equal(t, ctx, enriched, "whitespace-only reason 도 None Object 패턴")
			got, ok := llmgen.RejectReasonFromContext(enriched)
			assert.False(t, ok)
			assert.Equal(t, "", got)
		})
	}
}

func TestWithRejectReason_TrimsLeadingTrailingWhitespace(t *testing.T) {
	ctx := llmgen.WithRejectReason(context.Background(), "  PublishedAt required  \n")
	got, ok := llmgen.RejectReasonFromContext(ctx)
	assert.True(t, ok)
	assert.Equal(t, "PublishedAt required", got, "외곽 공백 trim — feedback 블록의 가독성 보장")
}

func TestRejectReasonFromContext_Absent_ReturnsFalse(t *testing.T) {
	ctx := context.Background()
	got, ok := llmgen.RejectReasonFromContext(ctx)
	assert.False(t, ok, "ctx 에 reason 없으면 false")
	assert.Equal(t, "", got)
}

func TestRejectReasonFromContext_WrongType_ReturnsFalse(t *testing.T) {
	// 다른 패키지에서 같은 key 타입을 사용하지 않도록 unexported key — 본 테스트는 우회 불가
	// (의도된 캡슐화). 부재 케이스만 검증.
	ctx := context.WithValue(context.Background(), struct{ name string }{"unrelated"}, "value")
	got, ok := llmgen.RejectReasonFromContext(ctx)
	assert.False(t, ok)
	assert.Equal(t, "", got)
}
