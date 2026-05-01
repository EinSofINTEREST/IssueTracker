package locks_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"issuetracker/internal/locks"
)

// ─────────────────────────────────────────────────────────────────────────────
// Mock ProcessingLock — pool integration test (test/internal/worker/processing_lock_pool_test.go)
// 에서도 재사용. 본 파일에는 NoopProcessingLock + ProcessingKey 단위 테스트만 둠.
// ─────────────────────────────────────────────────────────────────────────────

type mockProcessingLock struct{ mock.Mock }

func (m *mockProcessingLock) Acquire(ctx context.Context, key string) (bool, error) {
	args := m.Called(ctx, key)
	return args.Bool(0), args.Error(1)
}

func (m *mockProcessingLock) Release(ctx context.Context, key string) error {
	args := m.Called(ctx, key)
	return args.Error(0)
}

// 컴파일 타임 인터페이스 만족 검증.
var _ locks.ProcessingLock = (*mockProcessingLock)(nil)

// TestNoopProcessingLock_AlwaysAcquires는 NoopProcessingLock 이 항상 락 획득에 성공하는지 검증합니다.
func TestNoopProcessingLock_AlwaysAcquires(t *testing.T) {
	var locker locks.NoopProcessingLock

	acquired, err := locker.Acquire(context.Background(), "any-key")
	assert.NoError(t, err)
	assert.True(t, acquired)
}

// TestNoopProcessingLock_ReleaseNoError 는 NoopProcessingLock 의 Release 가 항상 성공하는지 검증합니다.
func TestNoopProcessingLock_ReleaseNoError(t *testing.T) {
	var locker locks.NoopProcessingLock

	err := locker.Release(context.Background(), "any-key")
	assert.NoError(t, err)
}

// ProcessingKey 가 (stage, url) 페어로 일관된 키를 만들고, stage 가 다르거나 url 이 다르면 키도 다른지 검증.
func TestProcessingKey_Determinism(t *testing.T) {
	url := "https://example.com/article/1"
	a1 := locks.ProcessingKey(locks.StageFetcher, url)
	a2 := locks.ProcessingKey(locks.StageFetcher, url)
	assert.Equal(t, a1, a2, "동일 (stage, url) 은 동일 키")

	b := locks.ProcessingKey(locks.StageParser, url)
	assert.NotEqual(t, a1, b, "stage 다르면 키 다름")

	c := locks.ProcessingKey(locks.StageFetcher, "https://example.com/article/2")
	assert.NotEqual(t, a1, c, "url 다르면 키 다름")
}
