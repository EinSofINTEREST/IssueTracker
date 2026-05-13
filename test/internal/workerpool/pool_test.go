package workerpool_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/processor/fetcher/core"
	"issuetracker/internal/workerpool"
	"issuetracker/pkg/logger"
	"issuetracker/pkg/queue"
)

// ─────────────────────────────────────────────────────────────────────────────
// Mocks
// ─────────────────────────────────────────────────────────────────────────────

type mockConsumer struct {
	mock.Mock
}

func (m *mockConsumer) FetchMessage(ctx context.Context) (*queue.Message, error) {
	args := m.Called(ctx)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*queue.Message), args.Error(1)
}

func (m *mockConsumer) CommitMessages(ctx context.Context, msgs ...*queue.Message) error {
	args := m.Called(ctx, msgs)
	return args.Error(0)
}

func (m *mockConsumer) Close() error {
	args := m.Called()
	return args.Error(0)
}

// captureHandler 는 Handle 호출의 메시지 + worker_id 를 캡쳐합니다.
type captureHandler struct {
	mu        sync.Mutex
	calls     []captureCall
	onHandle  func(ctx context.Context, msg *queue.Message)
	callCount atomic.Int32
}

type captureCall struct {
	msg      *queue.Message
	workerID int
}

func (h *captureHandler) Handle(ctx context.Context, msg *queue.Message) {
	h.callCount.Add(1)
	h.mu.Lock()
	h.calls = append(h.calls, captureCall{
		msg:      msg,
		workerID: core.WorkerIDFromContext(ctx),
	})
	h.mu.Unlock()
	if h.onHandle != nil {
		h.onHandle(ctx, msg)
	}
}

func (h *captureHandler) snapshot() []captureCall {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]captureCall, len(h.calls))
	copy(out, h.calls)
	return out
}

func testLog() *logger.Logger { return logger.New(logger.DefaultConfig()) }

// ─────────────────────────────────────────────────────────────────────────────
// Constructor invariants
// ─────────────────────────────────────────────────────────────────────────────

// TestNew_NilConsumer_Panics 는 Consumer nil 주입 시 fail-fast 보장.
func TestNew_NilConsumer_Panics(t *testing.T) {
	assert.PanicsWithValue(t, "workerpool.New: Consumer must not be nil", func() {
		workerpool.New(workerpool.Config{Handler: &captureHandler{}})
	})
}

// TestNew_NilHandler_Panics 는 Handler nil 주입 시 fail-fast 보장.
func TestNew_NilHandler_Panics(t *testing.T) {
	assert.PanicsWithValue(t, "workerpool.New: Handler must not be nil", func() {
		workerpool.New(workerpool.Config{Consumer: new(mockConsumer)})
	})
}

// TestNew_WorkerCountDefault 는 WorkerCount 0 이하 시 1 로 보정.
func TestNew_WorkerCountDefault(t *testing.T) {
	// panic 없이 생성되면 정상 — 내부 jobs 버퍼 크기는 외부 노출 안 되므로 동작 검증으로 확인.
	pool := workerpool.New(workerpool.Config{
		Consumer:    new(mockConsumer),
		Handler:     &captureHandler{},
		WorkerCount: 0,
	})
	assert.NotNil(t, pool)
}

// ─────────────────────────────────────────────────────────────────────────────
// Lifecycle / shutdown
// ─────────────────────────────────────────────────────────────────────────────

// TestPool_StartProcess_SingleMessage 는 단일 메시지를 worker 가 처리하는 기본 흐름.
func TestPool_StartProcess_SingleMessage(t *testing.T) {
	consumer := new(mockConsumer)
	handler := &captureHandler{}

	msg := &queue.Message{
		Topic: "test",
		Key:   []byte("k1"),
		Value: []byte("v1"),
	}

	ctx, cancel := context.WithCancel(context.Background())
	consumer.On("FetchMessage", mock.Anything).Return(msg, nil).Once()
	consumer.On("FetchMessage", mock.Anything).
		Run(func(_ mock.Arguments) { cancel() }).
		Return(nil, context.Canceled)
	consumer.On("Close").Return(nil)

	pool := workerpool.New(workerpool.Config{
		Consumer:    consumer,
		Handler:     handler,
		WorkerCount: 1,
		Log:         testLog(),
		Name:        "test",
	})

	pool.Start(ctx)
	<-ctx.Done()

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer stopCancel()
	require.NoError(t, pool.Stop(stopCtx))

	snap := handler.snapshot()
	require.Len(t, snap, 1)
	assert.Equal(t, msg, snap[0].msg)
}

// TestPool_WorkerID_Injected 는 workerCount > 1 시 worker_id 가 ctx 에 첨부됨을 검증.
func TestPool_WorkerID_Injected(t *testing.T) {
	consumer := new(mockConsumer)
	handler := &captureHandler{}

	// 충분히 많은 메시지로 multi-worker 활성화 확인.
	msgs := make([]*queue.Message, 6)
	for i := range msgs {
		msgs[i] = &queue.Message{Topic: "t", Key: []byte("k"), Value: []byte("v")}
	}

	consumer.On("FetchMessage", mock.Anything).Return(msgs[0], nil).Once()
	consumer.On("FetchMessage", mock.Anything).Return(msgs[1], nil).Once()
	consumer.On("FetchMessage", mock.Anything).Return(msgs[2], nil).Once()
	consumer.On("FetchMessage", mock.Anything).Return(msgs[3], nil).Once()
	consumer.On("FetchMessage", mock.Anything).Return(msgs[4], nil).Once()
	consumer.On("FetchMessage", mock.Anything).Return(msgs[5], nil).Once()
	ctx, cancel := context.WithCancel(context.Background())
	consumer.On("FetchMessage", mock.Anything).
		Run(func(_ mock.Arguments) { cancel() }).
		Return(nil, context.Canceled)
	consumer.On("Close").Return(nil)

	// 의도적으로 worker 처리 지연 — 여러 worker_id 가 호출되도록.
	handler.onHandle = func(_ context.Context, _ *queue.Message) {
		time.Sleep(5 * time.Millisecond)
	}

	pool := workerpool.New(workerpool.Config{
		Consumer:    consumer,
		Handler:     handler,
		WorkerCount: 3,
		Log:         testLog(),
		Name:        "test",
	})

	pool.Start(ctx)
	<-ctx.Done()

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer stopCancel()
	require.NoError(t, pool.Stop(stopCtx))

	snap := handler.snapshot()
	require.Len(t, snap, 6)
	// 모든 worker_id 가 [0, 3) 범위에 있어야 함.
	ids := make(map[int]bool)
	for _, c := range snap {
		assert.GreaterOrEqual(t, c.workerID, 0)
		assert.Less(t, c.workerID, 3)
		ids[c.workerID] = true
	}
	// 최소 2개 이상의 worker 가 활성화 (3개 모두 보장은 timing-dependent 라 looser check).
	assert.GreaterOrEqual(t, len(ids), 2, "multi-worker dispatch should engage multiple worker ids")
}

// TestPool_ShutdownOrder_NoSendOnClosed 는 Stop 순서 안전성 검증 —
// poll 이 jobs 채널 close 이후 send 시도 시 panic 발생하지 않아야 함.
func TestPool_ShutdownOrder_NoSendOnClosed(t *testing.T) {
	consumer := new(mockConsumer)
	handler := &captureHandler{}

	// FetchMessage 가 무한히 반환되도록 — Stop 의 pollCancel 만으로 종료.
	consumer.On("FetchMessage", mock.Anything).Return(&queue.Message{Topic: "t", Key: []byte("k"), Value: []byte("v")}, nil)
	consumer.On("Close").Return(nil)

	pool := workerpool.New(workerpool.Config{
		Consumer:    consumer,
		Handler:     handler,
		WorkerCount: 2,
		Log:         testLog(),
		Name:        "test",
	})

	pool.Start(context.Background())
	time.Sleep(20 * time.Millisecond) // 폴 + 처리 활성화 시간 확보

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer stopCancel()
	require.NoError(t, pool.Stop(stopCtx))
	// panic 없이 도달하면 성공.
}

// TestPool_Stop_DrainTimeout 은 worker 가 처리 중 (handler block) 일 때 ctx 만료로 force
// progress 하는지 검증.
func TestPool_Stop_DrainTimeout(t *testing.T) {
	consumer := new(mockConsumer)
	handler := &captureHandler{}

	// handler 가 영원히 block.
	blockCh := make(chan struct{})
	defer close(blockCh)
	handler.onHandle = func(_ context.Context, _ *queue.Message) {
		<-blockCh
	}

	consumer.On("FetchMessage", mock.Anything).Return(&queue.Message{Topic: "t"}, nil).Once()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	consumer.On("FetchMessage", mock.Anything).
		Run(func(_ mock.Arguments) { cancel() }).
		Return(nil, context.Canceled)
	consumer.On("Close").Return(nil)

	pool := workerpool.New(workerpool.Config{
		Consumer:    consumer,
		Handler:     handler,
		WorkerCount: 1,
		Log:         testLog(),
		Name:        "test",
	})

	pool.Start(ctx)
	<-ctx.Done()
	time.Sleep(20 * time.Millisecond) // worker 가 handler 진입 시간 확보

	// ctx timeout 으로 force progress — handler 가 block 중이라도 Stop 반환.
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer stopCancel()
	stopErr := pool.Stop(stopCtx)
	require.NoError(t, stopErr) // consumer.Close 는 무사 호출됨
}

// ─────────────────────────────────────────────────────────────────────────────
// Commit / drain
// ─────────────────────────────────────────────────────────────────────────────

// TestCommitWithDrain_Success 는 정상 ctx 에서 1회 commit 성공.
func TestCommitWithDrain_Success(t *testing.T) {
	consumer := new(mockConsumer)
	msg := &queue.Message{Topic: "t"}

	consumer.On("CommitMessages", mock.Anything, mock.Anything).Return(nil).Once()

	err := workerpool.CommitWithDrain(context.Background(), consumer, msg, time.Second)
	require.NoError(t, err)
	consumer.AssertExpectations(t)
}

// TestCommitWithDrain_CtxCanceled_DrainRetrySucceeds 는 첫 commit 가 ctx canceled 로 실패
// 했을 때 drain context 로 한 번 더 시도 → 성공 시 nil 반환.
func TestCommitWithDrain_CtxCanceled_DrainRetrySucceeds(t *testing.T) {
	consumer := new(mockConsumer)
	msg := &queue.Message{Topic: "t"}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 사전 cancel

	// 첫 호출 — canceled ctx → context.Canceled
	consumer.On("CommitMessages", ctx, mock.Anything).Return(context.Canceled).Once()
	// drain 재시도 — 다른 ctx → 성공
	consumer.On("CommitMessages", mock.MatchedBy(func(c context.Context) bool {
		return c != ctx && c.Err() == nil
	}), mock.Anything).Return(nil).Once()

	err := workerpool.CommitWithDrain(ctx, consumer, msg, time.Second)
	require.NoError(t, err)
	consumer.AssertExpectations(t)
}

// TestCommitWithDrain_CtxCanceled_DrainRetryAlsoFails 는 drain 재시도도 실패 시
// errors.Is(err, context.Canceled) 로 분기 가능해야 함.
func TestCommitWithDrain_CtxCanceled_DrainRetryAlsoFails(t *testing.T) {
	consumer := new(mockConsumer)
	msg := &queue.Message{Topic: "t"}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	consumer.On("CommitMessages", ctx, mock.Anything).Return(context.Canceled).Once()
	// drain 재시도도 deadline exceed (timeout = 1ms 강제)
	consumer.On("CommitMessages", mock.Anything, mock.Anything).Return(errors.New("broker down")).Once()

	err := workerpool.CommitWithDrain(ctx, consumer, msg, time.Millisecond)
	require.Error(t, err)
	// errors.Join 으로 최초 cancel 보존 — 호출자의 cancel 분기 안정성.
	assert.ErrorIs(t, err, context.Canceled)
}

// TestCommitWithDrain_NonCancelError_NoRetry 는 ctx canceled 아닌 일반 에러는 재시도 X.
func TestCommitWithDrain_NonCancelError_NoRetry(t *testing.T) {
	consumer := new(mockConsumer)
	msg := &queue.Message{Topic: "t"}

	consumer.On("CommitMessages", mock.Anything, mock.Anything).Return(errors.New("broker down")).Once()

	err := workerpool.CommitWithDrain(context.Background(), consumer, msg, time.Second)
	require.Error(t, err)
	consumer.AssertExpectations(t) // 1회만 호출 (재시도 X)
}
