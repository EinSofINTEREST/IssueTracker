// ZSetIntake.handleOne 의 분기 검증 (Copilot #3274731563 — mock Consumer + stub queue).
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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/processor/fetcher/core"
	"issuetracker/internal/processor/parser/worker"
	"issuetracker/pkg/logger"
	"issuetracker/pkg/queue"
)

// stubPusher 는 in-memory PriorityPusher mock. Push 호출 인자를 캡쳐 + failErr 로 실패 시뮬레이션.
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
}

func (s *stubPusher) Push(_ context.Context, priority int, id string, payload []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failErr != nil {
		err := s.failErr
		if s.failOnce {
			s.failErr = nil
		}
		return err
	}
	s.calls = append(s.calls, stubPushCall{
		Priority: priority,
		ID:       id,
		Payload:  append([]byte(nil), payload...),
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
