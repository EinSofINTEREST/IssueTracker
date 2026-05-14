package storage_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	"issuetracker/internal/storage"
)

func TestIsQueryTimeout_DirectDeadlineExceeded(t *testing.T) {
	err := context.DeadlineExceeded
	assert.True(t, storage.IsQueryTimeout(err), "context.DeadlineExceeded 직접 비교는 true")
}

func TestIsQueryTimeout_WrappedDeadlineExceeded(t *testing.T) {
	// fmt.Errorf("%w", ...) chain — repository 가 흔히 사용하는 wrap 패턴.
	wrapped := fmt.Errorf("query foo: %w", context.DeadlineExceeded)
	assert.True(t, storage.IsQueryTimeout(wrapped), "wrap 된 DeadlineExceeded 도 검출")
}

func TestIsQueryTimeout_DoubleWrappedDeadlineExceeded(t *testing.T) {
	// 다중 wrap (service boundary 가 다시 wrap 한 케이스).
	inner := fmt.Errorf("acquire: %w", context.DeadlineExceeded)
	outer := fmt.Errorf("save content: %w", inner)
	assert.True(t, storage.IsQueryTimeout(outer), "이중 wrap 도 errors.Is chain 으로 검출")
}

func TestIsQueryTimeout_UnrelatedError(t *testing.T) {
	err := errors.New("some other error")
	assert.False(t, storage.IsQueryTimeout(err), "비관련 에러는 false")
}

func TestIsQueryTimeout_ErrNotFound(t *testing.T) {
	// 다른 sentinel 과 혼동되지 않음.
	assert.False(t, storage.IsQueryTimeout(storage.ErrNotFound))
	assert.False(t, storage.IsQueryTimeout(storage.ErrDuplicate))
	assert.False(t, storage.IsQueryTimeout(storage.ErrInvalid))
}

func TestIsQueryTimeout_Nil(t *testing.T) {
	assert.False(t, storage.IsQueryTimeout(nil))
}

func TestIsQueryTimeout_ContextCanceled(t *testing.T) {
	// context.Canceled 는 timeout 이 아님 (사용자 cancel / shutdown 신호).
	assert.False(t, storage.IsQueryTimeout(context.Canceled))
}
