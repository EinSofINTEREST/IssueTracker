// ZSetIntake 의 handleOne 분기 + lifecycle 검증 (Copilot #3274731563, 이슈 #529).
//
// PriorityPusher 인터페이스 + bus.Consumer 인터페이스에 의존하므로 Redis / Kafka 없이 단위 검증.
package worker_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/processor/fetcher/core"
	"issuetracker/internal/processor/parser/worker"
	"issuetracker/pkg/logger"
	"issuetracker/pkg/queue"
)

// stubPusher 는 in-memory PriorityHeaderPusher mock. Push 호출 인자를 캡쳐 + failErr 로 실패 시뮬레이션.
type stubPusher struct {
	mu       sync.Mutex
	calls    []stubPushCall
	failErr  error
	failOnce bool
}

type stubPushCall struct {
	Priority int
	ID       string
	Payload  []byte
	Headers  map[string]string
}

func (s *stubPusher) Push(ctx context.Context, priority int, id string, payload []byte) error {
	return s.PushWithHeaders(ctx, priority, id, payload, nil)
}

func (s *stubPusher) PushWithHeaders(_ context.Context, priority int, id string, payload []byte, headers map[string]string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failErr != nil {
		err := s.failErr
		if s.failOnce {
			s.failErr = nil
		}
		return err
	}
	var hdr map[string]string
	if headers != nil {
		hdr = make(map[string]string, len(headers))
		for k, v := range headers {
			hdr[k] = v
		}
	}
	s.calls = append(s.calls, stubPushCall{
		Priority: priority,
		ID:       id,
		Payload:  append([]byte(nil), payload...),
		Headers:  hdr,
	})
	return nil
}

func (s *stubPusher) Calls() []stubPushCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]stubPushCall(nil), s.calls...)
}

// stubConsumer 는 in-memory bus.Consumer mock. CommitMessages 호출 횟수 + Close 여부 캡쳐.
type stubConsumer struct {
	mu        sync.Mutex
	commits   int32
	commitErr error
	closed    bool
}

func (c *stubConsumer) FetchMessage(_ context.Context) (*queue.Message, error) {
	return nil, errors.New("stubConsumer.FetchMessage not used in handleOne tests")
}

func (c *stubConsumer) CommitMessages(_ context.Context, _ ...*queue.Message) error {
	atomic.AddInt32(&c.commits, 1)
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.commitErr
}

func (c *stubConsumer) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	return nil
}

func (c *stubConsumer) CommitCount() int32 { return atomic.LoadInt32(&c.commits) }

// Closed 는 Close 호출 여부를 mutex 로 읽습니다 — Run goroutine 이 Close 를 호출하고
// 테스트 goroutine 이 읽으므로 race detector 대상입니다 (이슈 #529).
func (c *stubConsumer) Closed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

func newIntake(t *testing.T, pusher queue.PriorityPusher) (*worker.ZSetIntake, *stubConsumer) {
	t.Helper()
	log := logger.New(logger.Config{Level: "error"})
	cons := &stubConsumer{}
	intake := worker.NewZSetIntake(cons, pusher, log)
	require.NotNil(t, intake)
	return intake, cons
}

func makeIntakeMsg(t *testing.T, ref core.RawContentRef, headers map[string]string) *queue.Message {
	t.Helper()
	b, err := json.Marshal(ref)
	require.NoError(t, err)
	return &queue.Message{
		Value:   b,
		Headers: headers,
	}
}

func TestZSetIntake_HandleOne_Success_PushesAndCommits(t *testing.T) {
	pusher := &stubPusher{}
	intake, cons := newIntake(t, pusher)

	msg := makeIntakeMsg(t,
		core.RawContentRef{ID: "raw-1", URL: "https://example.com/", SourceInfo: core.SourceInfo{Name: "src"}},
		map[string]string{"priority": "1"},
	)
	intake.HandleOneForTest(context.Background(), msg)

	calls := pusher.Calls()
	require.Len(t, calls, 1)
	assert.Equal(t, "raw-1", calls[0].ID)
	assert.Equal(t, 1, calls[0].Priority)
	assert.Equal(t, int32(1), cons.CommitCount(), "성공 시 commit 1회")
}

func TestZSetIntake_HandleOne_Unmarshal_Failure_Commits(t *testing.T) {
	// 잘못된 JSON — push 호출 X, commit 호출 (redeliver loop 회피).
	pusher := &stubPusher{}
	intake, cons := newIntake(t, pusher)

	msg := &queue.Message{Value: []byte("{not-json"), Headers: map[string]string{}}
	intake.HandleOneForTest(context.Background(), msg)

	assert.Empty(t, pusher.Calls(), "unmarshal 실패 시 push 미호출")
	assert.Equal(t, int32(1), cons.CommitCount(), "unmarshal 실패 시 commit 호출 — redeliver 회피")
}

func TestZSetIntake_HandleOne_EmptyID_Commits(t *testing.T) {
	// 빈 RawContentRef.ID — push 호출 X, commit 호출.
	pusher := &stubPusher{}
	intake, cons := newIntake(t, pusher)

	msg := makeIntakeMsg(t, core.RawContentRef{URL: "https://example.com/"}, map[string]string{})
	intake.HandleOneForTest(context.Background(), msg)

	assert.Empty(t, pusher.Calls(), "빈 ID 시 push 미호출")
	assert.Equal(t, int32(1), cons.CommitCount(), "빈 ID 시 commit 호출")
}

func TestZSetIntake_HandleOne_PushFailure_SkipsCommit(t *testing.T) {
	// push 실패 → commit 호출 안 함 (Kafka 가 redeliver).
	pusher := &stubPusher{failErr: errors.New("push failed")}
	intake, cons := newIntake(t, pusher)

	msg := makeIntakeMsg(t,
		core.RawContentRef{ID: "raw-2", URL: "https://example.com/", SourceInfo: core.SourceInfo{Name: "s"}},
		map[string]string{"priority": "2"},
	)
	intake.HandleOneForTest(context.Background(), msg)

	assert.Equal(t, int32(0), cons.CommitCount(), "push 실패 시 commit skip — Kafka redeliver")
}

func TestZSetIntake_HandleOne_HeaderMissing_DefaultsNormalPriority(t *testing.T) {
	pusher := &stubPusher{}
	intake, _ := newIntake(t, pusher)

	msg := makeIntakeMsg(t,
		core.RawContentRef{ID: "raw-3", URL: "https://example.com/", SourceInfo: core.SourceInfo{Name: "x"}},
		nil,
	)
	intake.HandleOneForTest(context.Background(), msg)

	calls := pusher.Calls()
	require.Len(t, calls, 1)
	assert.Equal(t, 2, calls[0].Priority, "priority header 없으면 normal (2) default")
}

// ─────────────────────────────────────────────────────────────────────────────
// lifecycle (이슈 #529)
//
// Stage.Start 가 goroutine 을 분기해 놓고 Stop 이 그 종료를 기다리지 않으면,
// Run 의 defer consumer.Close() 가 프로세스 종료 전에 실행될 보장이 없다.
// ─────────────────────────────────────────────────────────────────────────────

func TestZSetIntake_Stop_WaitsForRunToExit(t *testing.T) {
	intake, cons := newIntake(t, &stubPusher{})

	runCtx, cancelRun := context.WithCancel(context.Background())
	intake.Start(runCtx)

	// run ctx 를 끊으면 Run 이 종료되고, Stop 은 그 종료를 확인한 뒤 반환해야 한다.
	cancelRun()

	stopCtx, cancelStop := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelStop()
	require.NoError(t, intake.Stop(stopCtx))

	assert.True(t, cons.Closed(), "Stop 반환 시점에는 consumer.Close 가 이미 실행돼 있어야 한다")
}

func TestZSetIntake_Stop_ContextExpiredWhileRunning_ReturnsCtxErr(t *testing.T) {
	intake, _ := newIntake(t, &stubPusher{})

	// run ctx 를 끊지 않아 Run 이 계속 돈다 — Stop 은 자신의 ctx 만료로 반환해야 한다.
	runCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()
	intake.Start(runCtx)

	stopCtx, cancelStop := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancelStop()

	err := intake.Stop(stopCtx)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded,
		"shutdown timeout 을 호출자가 인지할 수 있도록 에러를 삼키지 않아야 한다")
}

func TestZSetIntake_Stop_WithoutStart_ReturnsImmediately(t *testing.T) {
	intake, _ := newIntake(t, &stubPusher{})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	assert.NoError(t, intake.Stop(ctx), "Start 하지 않았으면 Stop 은 즉시 nil")
}
