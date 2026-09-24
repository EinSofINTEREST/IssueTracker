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

// ─────────────────────────────────────────────────────────────────────────────
// 헤더 보존 (이슈 #561)
//
// ZSET 경유 메시지가 priority 하나만 갖게 되면 BuildRetryJob 이 target_type 부재로
// category 를 article 로 떨어뜨리고, parseGateSkipCount 가 0 을 반환해 이슈 #540 의
// 재큐 예산이 무력화된다. 아래 테스트가 그 회귀를 막는다.
// ─────────────────────────────────────────────────────────────────────────────

func TestPushWithHeaders_HeadersSurvivePop(t *testing.T) {
	q, _ := newPriorityZSetTestQueue(t, "hdr-pop")
	ctx := context.Background()

	headers := map[string]string{
		"target_type":     "category",
		"crawler":         "yna",
		"timeout_ms":      "30000",
		"gate_skip_count": "2",
	}
	require.NoError(t, q.PushWithHeaders(ctx, 1, "cat-1", []byte(`{"id":"cat-1"}`), headers))

	res, err := q.Pop(ctx, 2*time.Second)
	require.NoError(t, err)
	require.NotNil(t, res)

	assert.Equal(t, "cat-1", res.ID)
	assert.Equal(t, headers, res.Headers, "push 한 헤더가 그대로 복원되어야 한다")
}

func TestPop_WithoutHeaders_ReturnsNilHeaders(t *testing.T) {
	q, _ := newPriorityZSetTestQueue(t, "hdr-absent")
	ctx := context.Background()

	// 구 버전이 남긴 entry 와 동일한 형태 — 헤더 키가 아예 없다.
	require.NoError(t, q.Push(ctx, 2, "legacy-1", []byte("payload")))

	res, err := q.Pop(ctx, 2*time.Second)
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Nil(t, res.Headers, "헤더 부재는 정상 경로 — 에러가 아니라 nil")
}

func TestPushWithHeaders_RepushWithoutHeaders_ClearsStale(t *testing.T) {
	q, _ := newPriorityZSetTestQueue(t, "hdr-stale")
	ctx := context.Background()

	require.NoError(t, q.PushWithHeaders(ctx, 2, "same", []byte("first"),
		map[string]string{"target_type": "category"}))
	// 같은 id 로 헤더 없이 재push — 이전 사이클 헤더가 되살아나면 안 된다.
	require.NoError(t, q.Push(ctx, 2, "same", []byte("second")))

	res, err := q.Pop(ctx, 2*time.Second)
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, []byte("second"), res.Payload)
	assert.Nil(t, res.Headers, "헤더 없이 재push 하면 이전 헤더가 남아서는 안 된다")
}

func TestFetchMessage_RestoresHeaders_PriorityFromScore(t *testing.T) {
	q, _ := newPriorityZSetTestQueue(t, "hdr-fetch")
	ctx := context.Background()

	// push 헤더의 priority 는 일부러 틀린 값 — score 가 단일 출처여야 한다.
	require.NoError(t, q.PushWithHeaders(ctx, 1, "c-1", []byte(`{"id":"c-1"}`),
		map[string]string{"target_type": "category", "priority": "3"}))

	c := queue.NewPriorityZSetConsumer(q, "parser.zset", 2*time.Second)
	msg, err := c.FetchMessage(ctx)
	require.NoError(t, err)
	require.NotNil(t, msg)

	assert.Equal(t, "category", msg.Headers["target_type"], "target_type 이 복원되어야 한다")
	assert.Equal(t, "1", msg.Headers["priority"], "priority 는 ZSET score 기준으로 덮어써야 한다")
}

// TestPop_StaleHeaderFromOtherCycle_Discarded 는 payload 와 짝이 맞지 않는 헤더를
// pop 이 버리는지 검증합니다 (이슈 #561, Copilot 피드백).
//
// 롤링 업그레이드 중 구 버전의 Push 는 :hdr 를 건드리지 않으므로, 같은 id 를 구 버전이
// 재push 하면 payload 만 갱신되고 이전 사이클 헤더가 남는다. 그 상태로 헤더를 복원하면
// target_type / gate_skip_count 가 잘못 붙는다.
func TestPop_StaleHeaderFromOtherCycle_Discarded(t *testing.T) {
	q, client := newPriorityZSetTestQueue(t, "hdr-stale-cycle")
	ctx := context.Background()

	// 신 버전이 헤더와 함께 push.
	require.NoError(t, q.PushWithHeaders(ctx, 2, "dup", []byte(`{"cycle":1}`),
		map[string]string{"target_type": "category"}))

	// 구 버전의 Push 를 흉내낸다 — payload 만 덮어쓰고 :hdr 는 그대로 둔다.
	// prefix 가 시각 기반이라 SCAN 으로 entry 키를 찾는다 (기존 테스트와 동일 방식).
	var entryKey string
	iter := client.Raw().Scan(ctx, 0, "test:priority-zset:hdr-stale-cycle:*:entry:dup", 0).Iterator()
	for iter.Next(ctx) {
		entryKey = iter.Val()
	}
	require.NoError(t, iter.Err())
	require.NotEmpty(t, entryKey, "entry 키를 찾지 못했다")
	require.NoError(t, client.Raw().Set(ctx, entryKey, []byte(`{"cycle":2}`), time.Minute).Err())

	res, err := q.Pop(ctx, 2*time.Second)
	require.NoError(t, err)
	require.NotNil(t, res)

	assert.Equal(t, []byte(`{"cycle":2}`), res.Payload)
	assert.Nil(t, res.Headers,
		"다른 사이클 payload 에 붙은 헤더는 버려야 한다 — 롤링 업그레이드 자가 검출")
}
