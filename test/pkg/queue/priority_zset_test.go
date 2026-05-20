// PriorityZSetQueue + PriorityZSetConsumer 통합 테스트 (이슈 #522, 메타 #515 Phase 2).
//
// 로컬 Redis 가 없으면 skip — REDIS_HOST/PORT env 로 주소 조정 가능.
// 테스트 간 격리: 각 테스트가 고유 ZSetKey/EntryKeyPrefix 사용 (시각 기반 prefix).
package queue_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	storagecfg "issuetracker/pkg/config/storage"
	"issuetracker/pkg/queue"
	pkgredis "issuetracker/pkg/redis"
)

// newPriorityZSetTestQueue 는 격리된 ZSetKey 로 PriorityZSetQueue 를 생성합니다.
// Redis 미가용 시 t.Skip — 통합 테스트.
func newPriorityZSetTestQueue(t *testing.T, suffix string) (*queue.PriorityZSetQueue, *pkgredis.Client) {
	t.Helper()
	cfg, err := storagecfg.LoadRedis()
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	client, err := pkgredis.New(ctx, cfg)
	if err != nil {
		t.Skipf("Redis not available (%v) — skipping integration test", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	// unique prefix per test — 시각 기반.
	prefix := fmt.Sprintf("test:priority-zset:%s:%d:", suffix, time.Now().UnixNano())
	q, err := queue.NewPriorityZSetQueue(client.Raw(), queue.PriorityZSetConfig{
		ZSetKey:        prefix + "queue",
		EntryKeyPrefix: prefix + "entry:",
		MaxSize:        100,
		EntryTTL:       1 * time.Minute,
	})
	require.NoError(t, err)

	// 테스트 종료 시 ZSET + 모든 entry cleanup.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		client.Raw().Del(ctx, prefix+"queue")
		// entries 는 wildcard SCAN + DEL — 운영에선 권장 안 되지만 테스트 cleanup 한정.
		// iter.Err() 검증 (Copilot #3274731503) — cleanup 실패가 silent 하지 않도록.
		iter := client.Raw().Scan(ctx, 0, prefix+"entry:*", 0).Iterator()
		for iter.Next(ctx) {
			client.Raw().Del(ctx, iter.Val())
		}
		if err := iter.Err(); err != nil {
			t.Logf("cleanup SCAN iterator error (ignored): %v", err)
		}
	})
	return q, client
}

func TestNewPriorityZSetQueue_NilRedis_ReturnsError(t *testing.T) {
	_, err := queue.NewPriorityZSetQueue(nil, queue.PriorityZSetConfig{
		ZSetKey: "x", EntryKeyPrefix: "y:",
	})
	assert.Error(t, err)
}

func TestNewPriorityZSetQueue_EmptyKey_ReturnsError(t *testing.T) {
	// rdb 는 nil 이 아니어도, key 빈 문자열은 ErrPriorityZSetInvalidConfig.
	// 실제 client 미가용시 nil 검사가 먼저 발동하므로 Skip 처리 필요 — 본 케이스는 unit only.
	cfg, err := storagecfg.LoadRedis()
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	client, err := pkgredis.New(ctx, cfg)
	if err != nil {
		t.Skipf("Redis not available — skipping")
	}
	t.Cleanup(func() { _ = client.Close() })

	_, err = queue.NewPriorityZSetQueue(client.Raw(), queue.PriorityZSetConfig{
		ZSetKey: "", EntryKeyPrefix: "x:",
	})
	assert.ErrorIs(t, err, queue.ErrPriorityZSetInvalidConfig)
}

func TestPriorityZSetQueue_PushPop_BasicRoundtrip(t *testing.T) {
	q, _ := newPriorityZSetTestQueue(t, "roundtrip")
	ctx := context.Background()

	payload := []byte(`{"id":"x","url":"https://example.com"}`)
	require.NoError(t, q.Push(ctx, 2, "x", payload))

	res, err := q.Pop(ctx, 2*time.Second)
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, "x", res.ID)
	assert.Equal(t, 2, res.Priority)
	assert.Equal(t, payload, res.Payload)
}

func TestPriorityZSetQueue_HighPriorityPoppedFirst(t *testing.T) {
	// 다른 priority 메시지를 normal → low → high 순서로 push 해도 pop 은 high 가 먼저.
	q, _ := newPriorityZSetTestQueue(t, "priority-order")
	ctx := context.Background()

	require.NoError(t, q.Push(ctx, 2, "normal-1", []byte("normal")))
	require.NoError(t, q.Push(ctx, 3, "low-1", []byte("low")))
	require.NoError(t, q.Push(ctx, 1, "high-1", []byte("high")))

	first, err := q.Pop(ctx, 2*time.Second)
	require.NoError(t, err)
	require.NotNil(t, first)
	assert.Equal(t, "high-1", first.ID, "high priority should pop first")

	second, err := q.Pop(ctx, 2*time.Second)
	require.NoError(t, err)
	require.NotNil(t, second)
	assert.Equal(t, "normal-1", second.ID, "normal priority should pop second")

	third, err := q.Pop(ctx, 2*time.Second)
	require.NoError(t, err)
	require.NotNil(t, third)
	assert.Equal(t, "low-1", third.ID, "low priority should pop last")
}

func TestPriorityZSetQueue_SamePriority_FIFO(t *testing.T) {
	// 같은 priority 안에서는 push 시점 순서 (FIFO).
	q, _ := newPriorityZSetTestQueue(t, "fifo")
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		id := fmt.Sprintf("normal-%d", i)
		require.NoError(t, q.Push(ctx, 2, id, []byte(id)))
		// 동일 priority 안에서 timestamp 가 다르도록 작은 sleep.
		time.Sleep(2 * time.Millisecond)
	}

	for i := 0; i < 3; i++ {
		res, err := q.Pop(ctx, 2*time.Second)
		require.NoError(t, err)
		require.NotNil(t, res)
		assert.Equal(t, fmt.Sprintf("normal-%d", i), res.ID, "FIFO order within same priority (i=%d)", i)
	}
}

func TestPriorityZSetQueue_Pop_EmptyQueue_Timeout(t *testing.T) {
	q, _ := newPriorityZSetTestQueue(t, "empty-timeout")
	ctx := context.Background()

	res, err := q.Pop(ctx, 200*time.Millisecond)
	require.NoError(t, err)
	assert.Nil(t, res, "empty queue should return (nil, nil) after timeout")
}

func TestPriorityZSetQueue_Pop_CtxCancel_ReturnsErr(t *testing.T) {
	q, _ := newPriorityZSetTestQueue(t, "cancel")
	ctx, cancel := context.WithCancel(context.Background())

	// pop goroutine 시작 후 즉시 cancel.
	type popResult struct {
		res *queue.PopResult
		err error
	}
	ch := make(chan popResult, 1)
	go func() {
		res, err := q.Pop(ctx, 5*time.Second)
		ch <- popResult{res, err}
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case r := <-ch:
		assert.Error(t, r.err)
	case <-time.After(2 * time.Second):
		t.Fatal("Pop did not return after ctx cancel")
	}
}

func TestPriorityZSetQueue_Push_EmptyID_ReturnsError(t *testing.T) {
	q, _ := newPriorityZSetTestQueue(t, "empty-id")
	err := q.Push(context.Background(), 2, "", []byte("x"))
	assert.Error(t, err)
}

func TestPriorityZSetQueue_Push_EmptyPayload_ReturnsError(t *testing.T) {
	q, _ := newPriorityZSetTestQueue(t, "empty-payload")
	err := q.Push(context.Background(), 2, "id-1", []byte{})
	assert.Error(t, err)
}

func TestPriorityZSetQueue_Len(t *testing.T) {
	q, _ := newPriorityZSetTestQueue(t, "len")
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		require.NoError(t, q.Push(ctx, 2, fmt.Sprintf("id-%d", i), []byte("p")))
	}

	n, err := q.Len(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(5), n)

	// 1건 pop 후 4.
	_, err = q.Pop(ctx, 1*time.Second)
	require.NoError(t, err)
	n, err = q.Len(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(4), n)
}

func TestPriorityZSetQueue_DuplicateID_OverwritesPayload(t *testing.T) {
	q, _ := newPriorityZSetTestQueue(t, "dup-id")
	ctx := context.Background()

	require.NoError(t, q.Push(ctx, 2, "same-id", []byte("first")))
	require.NoError(t, q.Push(ctx, 2, "same-id", []byte("second")))

	n, err := q.Len(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), n, "ZADD with same member should not duplicate")

	res, err := q.Pop(ctx, 1*time.Second)
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, []byte("second"), res.Payload, "second push should overwrite payload")
}

func TestPriorityZSetQueue_Push_InvalidPriority_NormalizesToNormal(t *testing.T) {
	q, _ := newPriorityZSetTestQueue(t, "invalid-priority")
	ctx := context.Background()

	// priority=0 (invalid) — should be treated as normal (2).
	require.NoError(t, q.Push(ctx, 0, "weird", []byte("p")))
	res, err := q.Pop(ctx, 1*time.Second)
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, 2, res.Priority, "out-of-range priority should normalize to normal")
}

func TestPriorityZSetConsumer_FetchMessage_BasicRoundtrip(t *testing.T) {
	// PriorityZSetConsumer 가 Consumer 인터페이스를 만족 + Push → FetchMessage 가 동일 payload 반환.
	q, _ := newPriorityZSetTestQueue(t, "consumer-roundtrip")
	c := queue.NewPriorityZSetConsumer(q, "parser:zset", 500*time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	payload := []byte(`{"id":"y","url":"https://test"}`)
	require.NoError(t, q.Push(ctx, 1, "y", payload))

	msg, err := c.FetchMessage(ctx)
	require.NoError(t, err)
	require.NotNil(t, msg)
	assert.Equal(t, "parser:zset", msg.Topic)
	assert.Equal(t, []byte("y"), msg.Key)
	assert.Equal(t, payload, msg.Value)
	assert.Equal(t, "1", msg.Headers["priority"], "priority header should reflect pop score")
}

func TestPriorityZSetConsumer_CommitMessages_NoOp(t *testing.T) {
	// CommitMessages 는 no-op — 호출이 에러를 발생시키지 않아야 함.
	q, _ := newPriorityZSetTestQueue(t, "consumer-commit")
	c := queue.NewPriorityZSetConsumer(q, "parser:zset", 500*time.Millisecond)
	assert.NoError(t, c.CommitMessages(context.Background()))
	assert.NoError(t, c.CommitMessages(context.Background(), &queue.Message{}))
}

func TestPriorityZSetConsumer_Close_NoOp(t *testing.T) {
	q, _ := newPriorityZSetTestQueue(t, "consumer-close")
	c := queue.NewPriorityZSetConsumer(q, "parser:zset", 500*time.Millisecond)
	assert.NoError(t, c.Close())
}

func TestPriorityZSetQueue_Pop_EntryExpiredBeforeGet_ReturnsNilPayload(t *testing.T) {
	// entry STRING 이 TTL 만료된 상태 — Pop 은 (id, score, nil payload) 반환.
	// 직접 ZADD 만 하고 SET 은 생략하여 시뮬레이션.
	q, client := newPriorityZSetTestQueue(t, "entry-expired")
	ctx := context.Background()

	prefix := "test:priority-zset:entry-expired:"
	// queue key 정확히 알 수 없으니 직접 push 후 SCAN
	require.NoError(t, q.Push(ctx, 2, "id-1", []byte("payload")))

	// entry STRING 직접 삭제 — TTL 만료 시뮬레이션.
	iter := client.Raw().Scan(ctx, 0, prefix+"*:entry:*", 0).Iterator()
	for iter.Next(ctx) {
		client.Raw().Del(ctx, iter.Val())
	}
	if err := iter.Err(); err != nil {
		t.Logf("scan err (ignored): %v", err)
	}

	res, err := q.Pop(ctx, 1*time.Second)
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, "id-1", res.ID)
	assert.Nil(t, res.Payload, "entry 만료 시 Payload=nil")
}

func TestPriorityZSetConsumer_FetchMessage_CtxCancel(t *testing.T) {
	// Empty queue + cancel — FetchMessage 가 ctx.Err() 반환.
	q, _ := newPriorityZSetTestQueue(t, "consumer-cancel")
	c := queue.NewPriorityZSetConsumer(q, "parser:zset", 500*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	type res struct {
		msg *queue.Message
		err error
	}
	ch := make(chan res, 1)
	go func() {
		m, err := c.FetchMessage(ctx)
		ch <- res{m, err}
	}()
	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case r := <-ch:
		assert.Error(t, r.err)
	case <-time.After(2 * time.Second):
		t.Fatal("FetchMessage did not return after ctx cancel")
	}
}
