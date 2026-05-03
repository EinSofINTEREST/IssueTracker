package worker_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"issuetracker/internal/processor/fetcher/worker"
)

// TestWorkerIDFromContext_Unset_ReturnsNegative 는 worker_id 미설정 ctx 가
// sentinel (-1) 을 반환하는지 검증합니다 (이슈 #229).
func TestWorkerIDFromContext_Unset_ReturnsNegative(t *testing.T) {
	id := worker.WorkerIDFromContext(context.Background())
	assert.Equal(t, -1, id)
}

// TestWorkerIDFromContext_NilContext_ReturnsNegative 는 nil ctx 도 panic 없이
// sentinel (-1) 을 반환하는지 검증합니다.
func TestWorkerIDFromContext_NilContext_ReturnsNegative(t *testing.T) {
	id := worker.WorkerIDFromContext(nil) //nolint:staticcheck // 의도적 nil 검증
	assert.Equal(t, -1, id)
}

// TestWithWorkerID_RoundTrip 는 WithWorkerID 로 주입한 값이 동일하게 추출되는지 검증합니다.
func TestWithWorkerID_RoundTrip(t *testing.T) {
	cases := []int{0, 1, 5, 99}
	for _, want := range cases {
		ctx := worker.WithWorkerID(context.Background(), want)
		got := worker.WorkerIDFromContext(ctx)
		assert.Equal(t, want, got)
	}
}

// TestWithWorkerID_Override 는 마지막 WithWorkerID 호출이 우선하는지 검증합니다
// (context.WithValue chain 동작).
func TestWithWorkerID_Override(t *testing.T) {
	ctx := worker.WithWorkerID(context.Background(), 0)
	ctx = worker.WithWorkerID(ctx, 7)
	assert.Equal(t, 7, worker.WorkerIDFromContext(ctx))
}
