package redis_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pkgredis "issuetracker/pkg/redis"
)

// jobBufferCleanup 은 테스트 간 격리를 위해 buffer 의 모든 항목을 drain 합니다.
func jobBufferCleanup(t *testing.T, client *pkgredis.Client, label string) {
	t.Helper()
	ctx := context.Background()
	for {
		drained, err := client.DrainJobs(ctx, label, 1000)
		require.NoError(t, err)
		if len(drained) == 0 {
			return
		}
	}
}

// TestEnqueueDrainJob_BasicFIFO 는 enqueue 순서대로 drain 되는지 (FIFO) 검증합니다.
func TestEnqueueDrainJob_BasicFIFO(t *testing.T) {
	client := newTestClient(t)
	label := "test-fifo"
	jobBufferCleanup(t, client, label)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		payload := []byte("payload-" + string(rune('a'+i)))
		require.NoError(t, client.EnqueueJob(ctx, label, payload, 0))
	}

	drained, err := client.DrainJobs(ctx, label, 5)
	require.NoError(t, err)
	require.Len(t, drained, 5)

	// FIFO 검증 — 첫 enqueue (payload-a) 가 첫 drain 위치에 등장.
	for i, p := range drained {
		expected := "payload-" + string(rune('a'+i))
		assert.Equal(t, expected, string(p), "FIFO 순서 — index %d", i)
	}

	// 추가 drain 은 빈 결과.
	more, err := client.DrainJobs(ctx, label, 1)
	require.NoError(t, err)
	assert.Empty(t, more)
}

// TestEnqueueJob_MaxLenLTrim 은 MaxLen 초과 시 oldest 가 제거되는지 검증합니다.
func TestEnqueueJob_MaxLenLTrim(t *testing.T) {
	client := newTestClient(t)
	label := "test-maxlen"
	jobBufferCleanup(t, client, label)
	ctx := context.Background()

	// MaxLen=3 — 5개 enqueue 시 가장 오래된 2개가 LTRIM 으로 제거되어 마지막 3개만 잔존.
	for i := 0; i < 5; i++ {
		payload := []byte("p-" + string(rune('a'+i)))
		require.NoError(t, client.EnqueueJob(ctx, label, payload, 3))
	}

	n, err := client.JobBufferLen(ctx, label)
	require.NoError(t, err)
	assert.Equal(t, int64(3), n, "MaxLen=3 으로 LTRIM 적용")

	drained, err := client.DrainJobs(ctx, label, 10)
	require.NoError(t, err)
	require.Len(t, drained, 3)
	// FIFO drain — tail (= 가장 오래된 잔존 = p-c) 부터.
	assert.Equal(t, "p-c", string(drained[0]))
	assert.Equal(t, "p-d", string(drained[1]))
	assert.Equal(t, "p-e", string(drained[2]))
}

// TestDrainJobs_EmptyBuffer 는 빈 buffer 에서 drain 시 빈 슬라이스 + nil error 반환을 검증합니다.
func TestDrainJobs_EmptyBuffer(t *testing.T) {
	client := newTestClient(t)
	label := "test-empty"
	jobBufferCleanup(t, client, label)
	ctx := context.Background()

	drained, err := client.DrainJobs(ctx, label, 10)
	require.NoError(t, err)
	assert.Empty(t, drained)
}

// TestEnqueueJob_EmptyValidation 은 빈 label / payload 가 error 를 반환하는지 검증합니다.
func TestEnqueueJob_EmptyValidation(t *testing.T) {
	client := newTestClient(t)
	ctx := context.Background()

	assert.Error(t, client.EnqueueJob(ctx, "", []byte("x"), 0))
	assert.Error(t, client.EnqueueJob(ctx, "ok", nil, 0))
	assert.Error(t, client.EnqueueJob(ctx, "ok", []byte{}, 0))
}

// TestJobBufferLen 은 enqueue 후 len 이 정확한지 검증합니다.
func TestJobBufferLen(t *testing.T) {
	client := newTestClient(t)
	label := "test-len"
	jobBufferCleanup(t, client, label)
	ctx := context.Background()

	n, err := client.JobBufferLen(ctx, label)
	require.NoError(t, err)
	assert.Equal(t, int64(0), n)

	for i := 0; i < 7; i++ {
		require.NoError(t, client.EnqueueJob(ctx, label, []byte("x"), 0))
	}

	n, err = client.JobBufferLen(ctx, label)
	require.NoError(t, err)
	assert.Equal(t, int64(7), n)
}
