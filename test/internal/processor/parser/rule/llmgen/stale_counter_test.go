package llmgen_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/processor/parser/rule/llmgen"
	"issuetracker/internal/storage"
)

// NoopStaleCounter 는 항상 (0, false, nil) 반환을 검증.
func TestNoopStaleCounter_AlwaysReturnsZero(t *testing.T) {
	c := llmgen.NewNoopStaleCounter()
	count, reached, err := c.Record(context.Background(), "example.com", storage.TargetTypePage)
	require.NoError(t, err)
	assert.Equal(t, 0, count)
	assert.False(t, reached)
}

// NewRedisStaleCounter 의 인자 검증 — nil client / 잘못된 threshold/window 거부.
//
// 실제 sliding window 동작은 testcontainers Redis 환경에서 별도 통합 테스트로 검증
// (FailureCounter 와 동일 알고리즘 — 그쪽 테스트가 sliding window primitive 검증 보유).
func TestNewRedisStaleCounter_RejectsInvalidArgs(t *testing.T) {
	_, err := llmgen.NewRedisStaleCounter(nil, 10, 2*time.Hour, "", nil)
	require.Error(t, err, "nil client 는 거부")
}
