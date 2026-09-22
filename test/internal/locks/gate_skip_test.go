package locks_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	"issuetracker/internal/locks"
)

// 재큐 지연은 ProcessingLock TTL 보다 짧아야 한다.
// 더 길면 stale 락이 이미 만료된 뒤에야 재시도가 도착해 복구가 불필요하게 늦어지고,
// TTL 과 같거나 길면 "락 만료를 기다린다" 는 의도가 흐려진다.
func TestGateSkipRetryDelay_ShorterThanProcessingLockTTL(t *testing.T) {
	assert.Less(t, locks.GateSkipRetryDelay, locks.DefaultProcessingLockTTL)
	assert.Equal(t, locks.DefaultProcessingLockTTL/2, locks.GateSkipRetryDelay)
}

// 0 이면 즉시 재큐되어 살아있는 락 홀더와 경쟁하며 무한 재큐가 된다.
func TestGateSkipRetryDelay_IsPositive(t *testing.T) {
	assert.Positive(t, locks.GateSkipRetryDelay)
}

// 재큐 사유가 실패(에러)와 구분 가능해야 운영자가 DLQ / 재시도 로그에서 원인을 나눌 수 있다.
func TestErrStageGateHeld_IsIdentifiable(t *testing.T) {
	wrapped := errors.New("outer: " + locks.ErrStageGateHeld.Error())

	assert.True(t, errors.Is(locks.ErrStageGateHeld, locks.ErrStageGateHeld))
	assert.False(t, errors.Is(wrapped, locks.ErrStageGateHeld),
		"문자열만 같은 에러는 동일시되면 안 됨")
	assert.Contains(t, locks.ErrStageGateHeld.Error(), "stage gate held")
}
