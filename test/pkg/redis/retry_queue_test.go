package redis_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pkgredis "issuetracker/pkg/redis"
)

// retryQueueCleanup 은 테스트 간 격리를 위해 retry 키를 모두 삭제합니다.
// 본 테스트 파일은 newTestClient 가 노출하는 단일 Redis 인스턴스를 공유하므로
// 각 테스트 시작 전에 호출해 stale 데이터를 제거해야 합니다.
func retryQueueCleanup(t *testing.T, client *pkgredis.Client) {
	t.Helper()
	ctx := context.Background()
	// PendingRetryCount 로 ZSET 존재 여부 확인 — 0 이면 entry 키도 없을 가능성 높지만
	// 기존 테스트가 남긴 entry STRING 도 정리하기 위해 ZRange 로 jobID 목록을 받고 DEL.
	for {
		due, err := client.PopDueRetries(ctx, time.Now().Add(time.Hour), 100)
		require.NoError(t, err)
		if len(due) == 0 {
			return
		}
	}
}

// TestEnqueueRetry_PopDue_BasicRoundtrip 는 등록 → 만기 후 pop 의 기본 흐름을 검증합니다.
func TestEnqueueRetry_PopDue_BasicRoundtrip(t *testing.T) {
	client := newTestClient(t)
	retryQueueCleanup(t, client)
	ctx := context.Background()

	jobID := "test-job-roundtrip-" + time.Now().Format(time.RFC3339Nano)
	payload := []byte(`{"foo":"bar"}`)
	scheduledAt := time.Now().Add(-time.Second) // 이미 만기

	require.NoError(t, client.EnqueueRetry(ctx, jobID, payload, scheduledAt))

	due, err := client.PopDueRetries(ctx, time.Now(), 10)
	require.NoError(t, err)
	require.Len(t, due, 1)
	assert.Equal(t, jobID, due[0].JobID)
	assert.Equal(t, payload, due[0].Payload)

	// 두 번째 pop 은 비어있어야 함 (이미 ZREM/DEL)
	due2, err := client.PopDueRetries(ctx, time.Now(), 10)
	require.NoError(t, err)
	assert.Empty(t, due2, "pop 후 ZSET/entry 모두 정리되어야 함")
}

// TestPopDueRetries_FuturScheduled_NotReturned 는 score 가 미래인 항목은 반환되지 않음을 검증.
func TestPopDueRetries_FutureScheduled_NotReturned(t *testing.T) {
	client := newTestClient(t)
	retryQueueCleanup(t, client)
	ctx := context.Background()

	jobID := "test-job-future-" + time.Now().Format(time.RFC3339Nano)
	require.NoError(t, client.EnqueueRetry(ctx, jobID, []byte("x"), time.Now().Add(10*time.Second)))

	due, err := client.PopDueRetries(ctx, time.Now(), 10)
	require.NoError(t, err)
	assert.Empty(t, due, "미래 scheduled 항목은 pop 대상 아님")

	// 단, count 에는 잡혀야 함
	n, err := client.PendingRetryCount(ctx)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, n, int64(1))

	// 후속 정리
	retryQueueCleanup(t, client)
}

// TestPopDueRetries_OrderByScheduledAt 는 score 순 (가장 오래된 먼저) 으로 반환됨을 검증.
func TestPopDueRetries_OrderByScheduledAt(t *testing.T) {
	client := newTestClient(t)
	retryQueueCleanup(t, client)
	ctx := context.Background()

	now := time.Now()
	prefix := "test-job-order-" + now.Format(time.RFC3339Nano) + "-"

	// 의도적으로 역순 enqueue — pop 결과가 score 순이어야 함
	require.NoError(t, client.EnqueueRetry(ctx, prefix+"c", []byte("c"), now.Add(-1*time.Second)))
	require.NoError(t, client.EnqueueRetry(ctx, prefix+"a", []byte("a"), now.Add(-3*time.Second)))
	require.NoError(t, client.EnqueueRetry(ctx, prefix+"b", []byte("b"), now.Add(-2*time.Second)))

	due, err := client.PopDueRetries(ctx, now, 10)
	require.NoError(t, err)
	require.Len(t, due, 3)
	assert.Equal(t, prefix+"a", due[0].JobID, "가장 오래된 score 가 먼저")
	assert.Equal(t, prefix+"b", due[1].JobID)
	assert.Equal(t, prefix+"c", due[2].JobID)
}

// TestPopDueRetries_LimitRespected 는 limit 인자가 반환 개수를 제한함을 검증.
func TestPopDueRetries_LimitRespected(t *testing.T) {
	client := newTestClient(t)
	retryQueueCleanup(t, client)
	ctx := context.Background()

	now := time.Now()
	prefix := "test-job-limit-" + now.Format(time.RFC3339Nano) + "-"
	for i := 0; i < 5; i++ {
		require.NoError(t, client.EnqueueRetry(ctx, prefix+string(rune('0'+i)), []byte("x"), now.Add(-time.Second)))
	}

	due, err := client.PopDueRetries(ctx, now, 2)
	require.NoError(t, err)
	assert.Len(t, due, 2, "limit=2 면 정확히 2건만 반환")

	// 나머지 3건 정리
	retryQueueCleanup(t, client)
}

// TestPopDueRetries_LimitZero_ReturnsEmpty 는 limit<=0 가 즉시 빈 결과를 반환함을 검증.
func TestPopDueRetries_LimitZero_ReturnsEmpty(t *testing.T) {
	client := newTestClient(t)
	ctx := context.Background()

	due, err := client.PopDueRetries(ctx, time.Now(), 0)
	require.NoError(t, err)
	assert.Empty(t, due)

	due2, err := client.PopDueRetries(ctx, time.Now(), -5)
	require.NoError(t, err)
	assert.Empty(t, due2)
}

// TestEnqueueRetry_Reschedule_OverwritesScore 는 같은 jobID 로 재호출 시 score 가
// 갱신됨을 검증 (가장 최근 호출이 우선).
func TestEnqueueRetry_Reschedule_OverwritesScore(t *testing.T) {
	client := newTestClient(t)
	retryQueueCleanup(t, client)
	ctx := context.Background()

	jobID := "test-job-reschedule-" + time.Now().Format(time.RFC3339Nano)
	now := time.Now()

	// 첫 번째: 미래 (pop 대상 아님)
	require.NoError(t, client.EnqueueRetry(ctx, jobID, []byte("v1"), now.Add(10*time.Second)))
	due, err := client.PopDueRetries(ctx, now, 10)
	require.NoError(t, err)
	assert.Empty(t, due, "첫 enqueue 는 미래 scheduled — pop 대상 아님")

	// 두 번째: 과거로 재스케줄 + payload 교체
	require.NoError(t, client.EnqueueRetry(ctx, jobID, []byte("v2"), now.Add(-time.Second)))
	due, err = client.PopDueRetries(ctx, now, 10)
	require.NoError(t, err)
	require.Len(t, due, 1)
	assert.Equal(t, []byte("v2"), due[0].Payload, "재호출 payload 가 우선")
}

// TestPopDueRetries_StaleEntryGone_SkipsAndCleansZSet 는 entry STRING 만 만료/삭제되어
// ZSET 에는 jobID 가 남은 stale 상태에서 silent skip + ZSET 정합 회복을 검증.
func TestPopDueRetries_StaleEntryGone_SkipsAndCleansZSet(t *testing.T) {
	client := newTestClient(t)
	retryQueueCleanup(t, client)
	ctx := context.Background()

	jobID := "test-job-stale-" + time.Now().Format(time.RFC3339Nano)
	require.NoError(t, client.EnqueueRetry(ctx, jobID, []byte("x"), time.Now().Add(-time.Second)))

	// entry STRING 만 직접 삭제하여 stale 상태 인위적으로 조성
	require.NoError(t, client.DeleteRetryEntryForTest(ctx, jobID))

	due, err := client.PopDueRetries(ctx, time.Now(), 10)
	require.NoError(t, err)
	assert.Empty(t, due, "stale jobID 는 silent skip — 호출자에게 노출 안 함")

	// ZSET 도 정리되었는지 확인 (count==0)
	n, err := client.PendingRetryCount(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(0), n, "ZSET 에서도 stale jobID 가 제거되어 정합 회복")
}
