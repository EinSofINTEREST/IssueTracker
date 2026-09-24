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
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/processor/enrich/worker"
	"issuetracker/internal/processor/fetcher/core"
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
	Headers  map[string]string
}

func (s *stubPusher) Push(ctx context.Context, priority int, id string, payload []byte) error {
	return s.PushWithHeaders(ctx, priority, id, payload, nil)
}

func (s *stubPusher) PushWithHeaders(_ context.Context, priority int, id string, payload []byte, headers map[string]string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failErr != nil {
		return s.failErr
	}
	var hdr map[string]string
	if headers != nil {
		hdr = make(map[string]string, len(headers))
		for k, v := range headers {
			hdr[k] = v
		}
	}
	s.calls = append(s.calls, stubPushCall{Priority: priority, ID: id, Payload: append([]byte(nil), payload...), Headers: hdr})
	return nil
}

func (s *stubPusher) Calls() []stubPushCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]stubPushCall(nil), s.calls...)
}

type stubConsumer struct {
	commits int32
	// closed 는 Run goroutine 이 쓰고 테스트 goroutine 이 읽으므로 atomic (이슈 #529, -race).
	closed int32
}

func (c *stubConsumer) FetchMessage(_ context.Context) (*queue.Message, error) {
	return nil, errors.New("not used in handleOne tests")
}

func (c *stubConsumer) CommitMessages(_ context.Context, _ ...*queue.Message) error {
	atomic.AddInt32(&c.commits, 1)
	return nil
}

func (c *stubConsumer) Close() error {
	atomic.StoreInt32(&c.closed, 1)
	return nil
}

// Closed 는 Close 호출 여부를 반환합니다 (이슈 #529).
func (c *stubConsumer) Closed() bool { return atomic.LoadInt32(&c.closed) == 1 }

func (c *stubConsumer) CommitCount() int32 { return atomic.LoadInt32(&c.commits) }

func newIntake(t *testing.T, pusher queue.PriorityHeaderPusher) (*worker.ZSetIntake, *stubConsumer) {
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

// ─────────────────────────────────────────────────────────────────────────────
// lifecycle (이슈 #529)
//
// 세 stage 의 ZSetIntake 는 각 패키지에 독립 구현되어 있어 parser 테스트로는 본
// 패키지의 회귀를 잡지 못한다 (Copilot 피드백). 동일 시나리오를 여기에도 둔다.
// ─────────────────────────────────────────────────────────────────────────────

func TestZSetIntake_Stop_WaitsForRunToExit(t *testing.T) {
	intake, cons := newIntake(t, &stubPusher{})

	runCtx, cancelRun := context.WithCancel(context.Background())
	intake.Start(runCtx)
	cancelRun()

	stopCtx, cancelStop := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelStop()
	require.NoError(t, intake.Stop(stopCtx))

	assert.True(t, cons.Closed(), "Stop 반환 시점에는 consumer.Close 가 이미 실행돼 있어야 한다")
}

func TestZSetIntake_Stop_ContextExpiredWhileRunning_ReturnsCtxErr(t *testing.T) {
	intake, _ := newIntake(t, &stubPusher{})

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
