// ZSetIntake.handleOne 의 분기 검증 (이슈 #523).
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
	"issuetracker/internal/processor/validate/worker"
	"issuetracker/pkg/logger"
	"issuetracker/pkg/queue"
)

type stubPusher struct {
	mu      sync.Mutex
	calls   []stubPushCall
	failErr error
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
		return s.failErr
	}
	s.calls = append(s.calls, stubPushCall{Priority: priority, ID: id, Payload: append([]byte(nil), payload...)})
	return nil
}

func (s *stubPusher) Calls() []stubPushCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]stubPushCall(nil), s.calls...)
}

type stubConsumer struct {
	commits int32
	closed  bool
}

func (c *stubConsumer) FetchMessage(_ context.Context) (*queue.Message, error) {
	return nil, errors.New("not used in handleOne tests")
}

func (c *stubConsumer) CommitMessages(_ context.Context, _ ...*queue.Message) error {
	atomic.AddInt32(&c.commits, 1)
	return nil
}

func (c *stubConsumer) Close() error {
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

func makeIntakeMsg(t *testing.T, ref core.ContentRef, headers map[string]string) *queue.Message {
	t.Helper()
	refBytes, err := json.Marshal(ref)
	require.NoError(t, err)
	pm := core.ProcessingMessage{ID: ref.ID, Data: refBytes}
	b, err := json.Marshal(pm)
	require.NoError(t, err)
	return &queue.Message{Value: b, Headers: headers}
}

func TestZSetIntake_HandleOne_Success_PushesAndCommits(t *testing.T) {
	pusher := &stubPusher{}
	intake, cons := newIntake(t, pusher)

	msg := makeIntakeMsg(t,
		core.ContentRef{ID: "ref-1", URL: "https://example.com/", SourceInfo: core.SourceInfo{Name: "src"}},
		map[string]string{"priority": "1"},
	)
	intake.HandleOneForTest(context.Background(), msg)

	calls := pusher.Calls()
	require.Len(t, calls, 1)
	assert.Equal(t, "ref-1", calls[0].ID)
	assert.Equal(t, 1, calls[0].Priority)
	assert.Equal(t, int32(1), cons.CommitCount())
}

func TestZSetIntake_HandleOne_ProcessingMessageUnmarshalFailure_Commits(t *testing.T) {
	pusher := &stubPusher{}
	intake, cons := newIntake(t, pusher)
	msg := &queue.Message{Value: []byte("{not-json"), Headers: map[string]string{}}
	intake.HandleOneForTest(context.Background(), msg)
	assert.Empty(t, pusher.Calls())
	assert.Equal(t, int32(1), cons.CommitCount())
}

func TestZSetIntake_HandleOne_ContentRefUnmarshalFailure_Commits(t *testing.T) {
	pusher := &stubPusher{}
	intake, cons := newIntake(t, pusher)
	// Data 가 유효 JSON 이지만 ContentRef 스키마 외 형식 (배열) — unmarshal 실패.
	pm := core.ProcessingMessage{ID: "x", Data: json.RawMessage(`[1,2,3]`)}
	b, err := json.Marshal(pm)
	require.NoError(t, err)
	msg := &queue.Message{Value: b, Headers: map[string]string{}}
	intake.HandleOneForTest(context.Background(), msg)
	assert.Empty(t, pusher.Calls())
	assert.Equal(t, int32(1), cons.CommitCount())
}

func TestZSetIntake_HandleOne_EmptyID_Commits(t *testing.T) {
	pusher := &stubPusher{}
	intake, cons := newIntake(t, pusher)
	msg := makeIntakeMsg(t, core.ContentRef{URL: "https://example.com/"}, nil)
	intake.HandleOneForTest(context.Background(), msg)
	assert.Empty(t, pusher.Calls())
	assert.Equal(t, int32(1), cons.CommitCount())
}

func TestZSetIntake_HandleOne_PushFailure_SkipsCommit(t *testing.T) {
	pusher := &stubPusher{failErr: errors.New("push failed")}
	intake, cons := newIntake(t, pusher)
	msg := makeIntakeMsg(t,
		core.ContentRef{ID: "ref-2", URL: "https://example.com/", SourceInfo: core.SourceInfo{Name: "s"}},
		map[string]string{"priority": "2"},
	)
	intake.HandleOneForTest(context.Background(), msg)
	assert.Equal(t, int32(0), cons.CommitCount(), "push 실패 시 commit skip — Kafka redeliver")
}

func TestZSetIntake_HandleOne_HeaderMissing_DefaultsNormalPriority(t *testing.T) {
	pusher := &stubPusher{}
	intake, _ := newIntake(t, pusher)
	msg := makeIntakeMsg(t,
		core.ContentRef{ID: "ref-3", URL: "https://example.com/", SourceInfo: core.SourceInfo{Name: "x"}},
		nil,
	)
	intake.HandleOneForTest(context.Background(), msg)
	calls := pusher.Calls()
	require.Len(t, calls, 1)
	assert.Equal(t, 2, calls[0].Priority)
}
