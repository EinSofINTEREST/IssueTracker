package publisher_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/processor/fetcher/core"
	"issuetracker/internal/publisher"
	"issuetracker/pkg/logger"
	"issuetracker/pkg/queue"
	pkgredis "issuetracker/pkg/redis"
)

// retryMockProducer 는 queue.Producer 를 만족하는 testify mock 입니다.
// 다른 publisher_test 파일의 producer mock 과 이름이 겹치지 않도록 retry 전용 prefix.
type retryMockProducer struct{ mock.Mock }

func (m *retryMockProducer) Publish(ctx context.Context, msg queue.Message) error {
	args := m.Called(ctx, msg)
	return args.Error(0)
}

func (m *retryMockProducer) PublishBatch(ctx context.Context, msgs []queue.Message) error {
	args := m.Called(ctx, msgs)
	return args.Error(0)
}

func (m *retryMockProducer) Close() error {
	args := m.Called()
	return args.Error(0)
}

// retryTestJob 은 RetryScheduler 테스트 전용 fixture 입니다.
func retryTestJob(priority core.Priority) *core.CrawlJob {
	return &core.CrawlJob{
		ID:          "retry-test-1",
		CrawlerName: "cnn",
		Target: core.Target{
			URL:  "https://edition.cnn.com/article/1",
			Type: core.TargetTypeArticle,
		},
		Priority:    priority,
		RetryCount:  2,
		MaxRetries:  3,
		Timeout:     30 * time.Second,
		ScheduledAt: time.Now().Add(10 * time.Second),
	}
}

// TestKafkaImmediateRetryScheduler_PublishesToPriorityTopic 는 job.Priority 에 따라
// 올바른 crawl 토픽으로 publish 되고 retry-count / last-error 헤더가 부착됨을 검증.
func TestKafkaImmediateRetryScheduler_PublishesToPriorityTopic(t *testing.T) {
	cases := []struct {
		name      string
		priority  core.Priority
		wantTopic string
	}{
		{"high → crawl.high", core.PriorityHigh, queue.TopicCrawlHigh},
		{"normal → crawl.normal", core.PriorityNormal, queue.TopicCrawlNormal},
		{"low → crawl.low", core.PriorityLow, queue.TopicCrawlLow},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			producer := new(retryMockProducer)
			sched := publisher.NewKafkaImmediateRetryScheduler(producer)

			job := retryTestJob(tc.priority)
			lastErr := errors.New("upstream 503")

			producer.On("Publish", mock.Anything, mock.MatchedBy(func(m queue.Message) bool {
				return m.Topic == tc.wantTopic &&
					string(m.Key) == job.ID &&
					m.Headers["retry-count"] == "2" &&
					m.Headers["last-error"] == "upstream 503" &&
					len(m.Value) > 0
			})).Return(nil)

			require.NoError(t, sched.Enqueue(context.Background(), job, lastErr))
			producer.AssertExpectations(t)
		})
	}
}

// TestKafkaImmediateRetryScheduler_PublishError_Wrapped 는 producer 가 에러를 반환할 때
// jobID 를 포함한 wrap 된 에러가 반환되는지 검증.
func TestKafkaImmediateRetryScheduler_PublishError_Wrapped(t *testing.T) {
	producer := new(retryMockProducer)
	sched := publisher.NewKafkaImmediateRetryScheduler(producer)

	job := retryTestJob(core.PriorityNormal)
	publishErr := errors.New("kafka unavailable")
	producer.On("Publish", mock.Anything, mock.Anything).Return(publishErr)

	err := sched.Enqueue(context.Background(), job, errors.New("any"))
	require.Error(t, err)
	assert.ErrorIs(t, err, publishErr, "원본 에러가 unwrap 가능해야 함")
	assert.Contains(t, err.Error(), job.ID, "wrap 메시지에 jobID 포함")
}

// TestKafkaImmediateRetryScheduler_NilLastErr_OmitsHeader 는 lastErr=nil 시
// last-error 헤더가 누락됨을 검증 (운영 보호 — nil deref 회피).
func TestKafkaImmediateRetryScheduler_NilLastErr_OmitsHeader(t *testing.T) {
	producer := new(retryMockProducer)
	sched := publisher.NewKafkaImmediateRetryScheduler(producer)

	job := retryTestJob(core.PriorityNormal)

	producer.On("Publish", mock.Anything, mock.MatchedBy(func(m queue.Message) bool {
		_, hasLastErr := m.Headers["last-error"]
		return !hasLastErr && m.Headers["retry-count"] == "2"
	})).Return(nil)

	require.NoError(t, sched.Enqueue(context.Background(), job, nil))
	producer.AssertExpectations(t)
}

// ─────────────────────────────────────────────────────────────────────────────
// RedisDelayedRetryScheduler 테스트 — fakeRetryQueue 로 in-memory ZSET emulate
// ─────────────────────────────────────────────────────────────────────────────

// fakeRetryQueue 는 retryQueueClient 인터페이스를 만족하는 in-memory 더블입니다.
// 진짜 Redis 가 없어도 Run 루프와 Enqueue/Peek/Ack 흐름을 결정적으로 검증합니다.
//
// peek-publish-ack 패턴: PeekDueRetries 는 항목을 ZSET 에 남겨두고
// AckRetry 호출 시에만 제거합니다 — 진짜 Redis 와 동일 시맨틱.
type fakeRetryQueue struct {
	mu               sync.Mutex
	items            []fakeRetryItem
	enqueueErr       error
	peekErr          error
	ackErr           error
	enqueueCallCount atomic.Int32
	peekCallCount    atomic.Int32
	ackCallCount     atomic.Int32
}

type fakeRetryItem struct {
	jobID       string
	payload     []byte
	scheduledAt time.Time
}

func newFakeRetryQueue() *fakeRetryQueue { return &fakeRetryQueue{} }

func (f *fakeRetryQueue) EnqueueRetry(_ context.Context, jobID string, payload []byte, scheduledAt time.Time) error {
	f.enqueueCallCount.Add(1)
	if f.enqueueErr != nil {
		return f.enqueueErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, it := range f.items {
		if it.jobID == jobID {
			f.items[i] = fakeRetryItem{jobID: jobID, payload: payload, scheduledAt: scheduledAt}
			f.sortLocked()
			return nil
		}
	}
	f.items = append(f.items, fakeRetryItem{jobID: jobID, payload: payload, scheduledAt: scheduledAt})
	f.sortLocked()
	return nil
}

func (f *fakeRetryQueue) PeekDueRetries(_ context.Context, now time.Time, limit int) ([]pkgredis.DueRetry, error) {
	f.peekCallCount.Add(1)
	if f.peekErr != nil {
		return nil, f.peekErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	out := make([]pkgredis.DueRetry, 0)
	for _, it := range f.items {
		if it.scheduledAt.After(now) {
			continue
		}
		if len(out) >= limit {
			break
		}
		out = append(out, pkgredis.DueRetry{JobID: it.jobID, Payload: it.payload})
	}
	return out, nil
}

// fakeRetryQueueCapturingCtx 는 fakeRetryQueue 에 EnqueueRetry 호출 시 ctx.Err() 를
// 캡처하는 기능을 추가한 변형입니다. drain context 적용 여부 검증 전용.
type fakeRetryQueueCapturingCtx struct {
	*fakeRetryQueue
	mu               sync.Mutex
	lastCtxErrAtCall error
	captured         bool
}

func newFakeRetryQueueCapturingCtx() *fakeRetryQueueCapturingCtx {
	return &fakeRetryQueueCapturingCtx{fakeRetryQueue: newFakeRetryQueue()}
}

func (f *fakeRetryQueueCapturingCtx) EnqueueRetry(ctx context.Context, jobID string, payload []byte, scheduledAt time.Time) error {
	f.mu.Lock()
	f.lastCtxErrAtCall = ctx.Err()
	f.captured = true
	f.mu.Unlock()
	return f.fakeRetryQueue.EnqueueRetry(ctx, jobID, payload, scheduledAt)
}

func (f *fakeRetryQueueCapturingCtx) lastEnqueueCtxErr() (error, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastCtxErrAtCall, f.captured
}

func (f *fakeRetryQueue) AckRetry(_ context.Context, jobID string) error {
	f.ackCallCount.Add(1)
	if f.ackErr != nil {
		return f.ackErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, it := range f.items {
		if it.jobID == jobID {
			f.items = append(f.items[:i], f.items[i+1:]...)
			return nil
		}
	}
	return nil
}

func (f *fakeRetryQueue) sortLocked() {
	for i := 1; i < len(f.items); i++ {
		for j := i; j > 0 && f.items[j-1].scheduledAt.After(f.items[j].scheduledAt); j-- {
			f.items[j], f.items[j-1] = f.items[j-1], f.items[j]
		}
	}
}

func (f *fakeRetryQueue) snapshot() []fakeRetryItem {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]fakeRetryItem, len(f.items))
	copy(out, f.items)
	return out
}

func retrySchedTestLogger() *logger.Logger { return logger.New(logger.DefaultConfig()) }

// TestRedisDelayedRetryScheduler_Enqueue_StoresJobAndLastErr 는 Enqueue 가 jobID/payload/
// ScheduledAt 을 정확히 store 하는지 + payload 가 job + last-err JSON 임을 검증.
func TestRedisDelayedRetryScheduler_Enqueue_StoresJobAndLastErr(t *testing.T) {
	q := newFakeRetryQueue()
	prod := new(retryMockProducer)
	sched := publisher.NewRedisDelayedRetryScheduler(q, prod, publisher.DefaultRedisRetrySchedulerConfig(), retrySchedTestLogger())

	job := retryTestJob(core.PriorityNormal)
	require.NoError(t, sched.Enqueue(context.Background(), job, errors.New("upstream 503")))

	snap := q.snapshot()
	require.Len(t, snap, 1)
	assert.Equal(t, job.ID, snap[0].jobID)
	assert.WithinDuration(t, job.ScheduledAt, snap[0].scheduledAt, time.Second)

	var entry struct {
		JobBytes []byte `json:"job"`
		LastErr  string `json:"last_err"`
	}
	require.NoError(t, json.Unmarshal(snap[0].payload, &entry))
	assert.Equal(t, "upstream 503", entry.LastErr)
	assert.NotEmpty(t, entry.JobBytes)
}

// TestRedisDelayedRetryScheduler_Run_PublishesDueItems 는 Run 루프가 due 항목을
// Kafka 에 발행하고 priority → topic 매핑/headers 가 올바른지 검증.
func TestRedisDelayedRetryScheduler_Run_PublishesDueItems(t *testing.T) {
	q := newFakeRetryQueue()
	prod := new(retryMockProducer)
	cfg := publisher.DefaultRedisRetrySchedulerConfig()
	cfg.PollInterval = 10 * time.Millisecond
	sched := publisher.NewRedisDelayedRetryScheduler(q, prod, cfg, retrySchedTestLogger())

	job := retryTestJob(core.PriorityHigh)
	job.ScheduledAt = time.Now().Add(-time.Second)
	require.NoError(t, sched.Enqueue(context.Background(), job, errors.New("err")))

	prod.On("Publish", mock.Anything, mock.MatchedBy(func(m queue.Message) bool {
		return m.Topic == queue.TopicCrawlHigh &&
			string(m.Key) == job.ID &&
			m.Headers["retry-count"] == "2" &&
			m.Headers["last-error"] == "err"
	})).Return(nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { sched.Run(ctx); close(done) }()

	require.Eventually(t, func() bool {
		return len(q.snapshot()) == 0
	}, time.Second, 5*time.Millisecond, "due 항목이 publish 후 큐에서 제거되어야 함")

	cancel()
	<-done
	prod.AssertExpectations(t)
}

// TestRedisDelayedRetryScheduler_Run_FutureItemsNotPublished 는 ScheduledAt 이 미래인
// 항목은 Run 이 publish 하지 않음을 검증.
func TestRedisDelayedRetryScheduler_Run_FutureItemsNotPublished(t *testing.T) {
	q := newFakeRetryQueue()
	prod := new(retryMockProducer)
	cfg := publisher.DefaultRedisRetrySchedulerConfig()
	cfg.PollInterval = 10 * time.Millisecond
	sched := publisher.NewRedisDelayedRetryScheduler(q, prod, cfg, retrySchedTestLogger())

	job := retryTestJob(core.PriorityNormal)
	job.ScheduledAt = time.Now().Add(10 * time.Second)
	require.NoError(t, sched.Enqueue(context.Background(), job, errors.New("err")))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { sched.Run(ctx); close(done) }()

	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	assert.Len(t, q.snapshot(), 1, "미래 항목은 큐에 그대로 남아 있어야 함")
	prod.AssertNotCalled(t, "Publish", mock.Anything, mock.Anything)
}

// TestRedisDelayedRetryScheduler_AckOnlyAfterSuccessfulPublish 는 peek-publish-ack
// 패턴의 핵심 보증을 검증: publish 성공 시에만 AckRetry 호출, 실패 시 ack 없이
// 항목이 큐에 남아 다음 폴 사이클에 재peek 가능 (at-least-once).
func TestRedisDelayedRetryScheduler_AckOnlyAfterSuccessfulPublish(t *testing.T) {
	q := newFakeRetryQueue()
	prod := new(retryMockProducer)
	cfg := publisher.DefaultRedisRetrySchedulerConfig()
	cfg.PollInterval = 10 * time.Millisecond
	cfg.RepublishFailureBackoff = time.Hour
	sched := publisher.NewRedisDelayedRetryScheduler(q, prod, cfg, retrySchedTestLogger())

	job := retryTestJob(core.PriorityNormal)
	job.ScheduledAt = time.Now().Add(-time.Second)
	require.NoError(t, sched.Enqueue(context.Background(), job, errors.New("e")))

	publishCalls := atomic.Int32{}
	prod.On("Publish", mock.Anything, mock.Anything).Return(errors.New("kafka transient")).Once().Run(func(_ mock.Arguments) {
		publishCalls.Add(1)
	})
	prod.On("Publish", mock.Anything, mock.Anything).Return(nil).Run(func(_ mock.Arguments) {
		publishCalls.Add(1)
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { sched.Run(ctx); close(done) }()

	require.Eventually(t, func() bool {
		return publishCalls.Load() >= 1
	}, time.Second, 5*time.Millisecond, "최소 1회 publish 시도")

	cancel()
	<-done

	assert.Equal(t, int32(0), q.ackCallCount.Load(),
		"publish 실패 시 ack 호출되지 않음 — 항목이 ZSET 에 남아 at-least-once 보장")
}

// TestRedisDelayedRetryScheduler_AckCalledAfterPublishSuccess 는 publish 성공 시
// AckRetry 가 1회 호출되어 ZSET 에서 제거됨을 검증.
func TestRedisDelayedRetryScheduler_AckCalledAfterPublishSuccess(t *testing.T) {
	q := newFakeRetryQueue()
	prod := new(retryMockProducer)
	cfg := publisher.DefaultRedisRetrySchedulerConfig()
	cfg.PollInterval = 10 * time.Millisecond
	sched := publisher.NewRedisDelayedRetryScheduler(q, prod, cfg, retrySchedTestLogger())

	job := retryTestJob(core.PriorityNormal)
	job.ScheduledAt = time.Now().Add(-time.Second)
	require.NoError(t, sched.Enqueue(context.Background(), job, errors.New("e")))

	prod.On("Publish", mock.Anything, mock.Anything).Return(nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { sched.Run(ctx); close(done) }()

	require.Eventually(t, func() bool {
		return q.ackCallCount.Load() >= 1 && len(q.snapshot()) == 0
	}, time.Second, 5*time.Millisecond, "publish 성공 후 ack 1회 + 큐에서 제거")

	cancel()
	<-done
}

// TestRedisDelayedRetryScheduler_Run_PublishFailure_ReEnqueues 는 Kafka publish 실패 시
// 항목이 짧은 backoff 로 재 enqueue 됨을 검증.
func TestRedisDelayedRetryScheduler_Run_PublishFailure_ReEnqueues(t *testing.T) {
	q := newFakeRetryQueue()
	prod := new(retryMockProducer)
	cfg := publisher.DefaultRedisRetrySchedulerConfig()
	cfg.PollInterval = 10 * time.Millisecond
	cfg.RepublishFailureBackoff = 5 * time.Second
	sched := publisher.NewRedisDelayedRetryScheduler(q, prod, cfg, retrySchedTestLogger())

	job := retryTestJob(core.PriorityNormal)
	job.ScheduledAt = time.Now().Add(-time.Second)
	require.NoError(t, sched.Enqueue(context.Background(), job, errors.New("e")))

	prod.On("Publish", mock.Anything, mock.Anything).Return(errors.New("kafka down"))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { sched.Run(ctx); close(done) }()

	require.Eventually(t, func() bool {
		return q.enqueueCallCount.Load() >= 2
	}, time.Second, 5*time.Millisecond, "publish 실패 후 재 enqueue 발생해야 함")

	cancel()
	<-done

	snap := q.snapshot()
	require.Len(t, snap, 1, "publish 실패한 항목이 다시 큐에 보관됨")
	assert.True(t, snap[0].scheduledAt.After(time.Now()), "재 enqueue 의 ScheduledAt 이 미래로 설정")
}

// TestRedisDelayedRetryScheduler_Run_PeekError_LogsAndContinues 는 Peek 실패 시 다음
// 폴 사이클로 계속 진행됨을 검증 (panic 없이 회복).
func TestRedisDelayedRetryScheduler_Run_PeekError_LogsAndContinues(t *testing.T) {
	q := newFakeRetryQueue()
	q.peekErr = errors.New("redis transient")
	prod := new(retryMockProducer)
	cfg := publisher.DefaultRedisRetrySchedulerConfig()
	cfg.PollInterval = 10 * time.Millisecond
	sched := publisher.NewRedisDelayedRetryScheduler(q, prod, cfg, retrySchedTestLogger())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { sched.Run(ctx); close(done) }()

	require.Eventually(t, func() bool {
		return q.peekCallCount.Load() >= 3
	}, time.Second, 5*time.Millisecond)

	cancel()
	<-done
	prod.AssertNotCalled(t, "Publish", mock.Anything, mock.Anything)
}

// TestRedisDelayedRetryScheduler_RepublishOnShutdown_UsesDrainCtx 는 ctx 가 canceled
// 인 상태에서 publish 실패 → reschedule 경로가 drain context 로 EnqueueRetry 를
// 한 번 더 시도해 backoff 를 보존함을 검증.
func TestRedisDelayedRetryScheduler_RepublishOnShutdown_UsesDrainCtx(t *testing.T) {
	q := newFakeRetryQueueCapturingCtx()
	prod := new(retryMockProducer)
	cfg := publisher.DefaultRedisRetrySchedulerConfig()
	cfg.PollInterval = 10 * time.Millisecond
	cfg.RepublishFailureBackoff = 5 * time.Second
	sched := publisher.NewRedisDelayedRetryScheduler(q, prod, cfg, retrySchedTestLogger())

	job := retryTestJob(core.PriorityNormal)
	job.ScheduledAt = time.Now().Add(-time.Second)
	require.NoError(t, sched.Enqueue(context.Background(), job, errors.New("e")))
	require.Equal(t, int32(1), q.enqueueCallCount.Load(), "초기 enqueue 1회 — ctx 정상")

	ctx, cancel := context.WithCancel(context.Background())

	prod.On("Publish", mock.Anything, mock.Anything).Run(func(_ mock.Arguments) {
		cancel()
	}).Return(context.Canceled)

	sched.Start(ctx)
	require.Eventually(t, func() bool {
		return q.enqueueCallCount.Load() >= 2
	}, time.Second, 5*time.Millisecond, "shutdown 윈도우에서도 reschedule 시도")
	sched.Stop()

	ctxErr, captured := q.lastEnqueueCtxErr()
	require.True(t, captured, "EnqueueRetry 가 최소 1회 호출됨")
	assert.NoError(t, ctxErr,
		"shutdown 중 reschedule 은 drain context 로 호출되어 호출 시점 ctx.Err()=nil")
}

// TestRedisDelayedRetryScheduler_StartStop_NoWaitGroupPanic 는 Start → ctx cancel → Stop
// 흐름이 race-free 하고 panic 없음을 검증.
func TestRedisDelayedRetryScheduler_StartStop_NoWaitGroupPanic(t *testing.T) {
	q := newFakeRetryQueue()
	prod := new(retryMockProducer)
	cfg := publisher.DefaultRedisRetrySchedulerConfig()
	cfg.PollInterval = 10 * time.Millisecond
	sched := publisher.NewRedisDelayedRetryScheduler(q, prod, cfg, retrySchedTestLogger())

	ctx, cancel := context.WithCancel(context.Background())
	sched.Start(ctx)

	cancel()

	stopped := make(chan struct{})
	go func() {
		sched.Stop()
		close(stopped)
	}()

	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("Stop 이 1초 내 반환해야 함 — wg.Wait deadlock 의심")
	}
}

// TestRedisDelayedRetryScheduler_DefaultConfigBoundary 는 cfg 의 0/음수 값이 default 로
// 보정됨을 검증.
func TestRedisDelayedRetryScheduler_DefaultConfigBoundary(t *testing.T) {
	q := newFakeRetryQueue()
	prod := new(retryMockProducer)
	sched := publisher.NewRedisDelayedRetryScheduler(q, prod, publisher.RedisRetrySchedulerConfig{}, retrySchedTestLogger())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() { sched.Run(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run 이 ctx cancel 후 즉시 종료되어야 함")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Heartbeat 압축 (이슈 #370)
// ─────────────────────────────────────────────────────────────────────────────

type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *safeBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *safeBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

func captureDebugLogger(buf *safeBuffer) *logger.Logger {
	return logger.New(logger.Config{
		Level:      logger.LevelDebug,
		Output:     buf,
		TimeFormat: time.RFC3339,
	})
}

func countLines(buf *safeBuffer, substr string) int {
	n := 0
	for _, line := range strings.Split(buf.String(), "\n") {
		if strings.Contains(line, substr) {
			n++
		}
	}
	return n
}

// TestRedisDelayedRetryScheduler_IdleHeartbeat_CompressedEveryN 는
// HeartbeatEveryNIdleTicks=N 설정 시 N tick 마다 1회만 heartbeat 로그가 나오는지 검증.
func TestRedisDelayedRetryScheduler_IdleHeartbeat_CompressedEveryN(t *testing.T) {
	q := newFakeRetryQueue()
	prod := new(retryMockProducer)
	buf := &safeBuffer{}

	cfg := publisher.DefaultRedisRetrySchedulerConfig()
	cfg.PollInterval = 10 * time.Millisecond
	cfg.HeartbeatEveryNIdleTicks = 5
	sched := publisher.NewRedisDelayedRetryScheduler(q, prod, cfg, captureDebugLogger(buf))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { sched.Run(ctx); close(done) }()
	defer func() {
		cancel()
		<-done
	}()

	require.Eventually(t, func() bool {
		return countLines(buf, "retry pipeline idle heartbeat") >= 2
	}, 3*time.Second, 5*time.Millisecond, "최소 2회 heartbeat emit 되어야 함")

	peekCount := int(q.peekCallCount.Load())
	heartbeats := countLines(buf, "retry pipeline idle heartbeat")
	require.Greater(t, peekCount, heartbeats,
		"peek 호출 횟수가 heartbeat 보다 많아야 함 (압축 동작)")
	assert.LessOrEqual(t, heartbeats*2, peekCount,
		"heartbeat 가 peek 의 절반 이하 (N=5 압축 확인) — heartbeats=%d peeks=%d",
		heartbeats, peekCount)
	assert.Equal(t, 0, countLines(buf, "retry peek returned no due items"),
		"구 메시지는 더 이상 emit 되지 않아야 함")
}

// TestRedisDelayedRetryScheduler_IdleHeartbeat_LegacyEveryTick 는
// HeartbeatEveryNIdleTicks=0 시 legacy 동작 (매 tick 1줄) 이 유지되는지 검증.
func TestRedisDelayedRetryScheduler_IdleHeartbeat_LegacyEveryTick(t *testing.T) {
	q := newFakeRetryQueue()
	prod := new(retryMockProducer)
	buf := &safeBuffer{}

	cfg := publisher.DefaultRedisRetrySchedulerConfig()
	cfg.PollInterval = 10 * time.Millisecond
	cfg.HeartbeatEveryNIdleTicks = 0
	sched := publisher.NewRedisDelayedRetryScheduler(q, prod, cfg, captureDebugLogger(buf))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { sched.Run(ctx); close(done) }()
	defer func() {
		cancel()
		<-done
	}()

	require.Eventually(t, func() bool {
		return countLines(buf, "retry pipeline idle heartbeat") >= 5
	}, 3*time.Second, 5*time.Millisecond, "legacy 모드는 매 tick heartbeat — 5회 도달")

	peekCount := int(q.peekCallCount.Load())
	heartbeats := countLines(buf, "retry pipeline idle heartbeat")
	assert.InDelta(t, peekCount, heartbeats, 1,
		"legacy 모드는 heartbeat 가 peek 와 거의 1:1 — heartbeats=%d peeks=%d",
		heartbeats, peekCount)
}

// TestRedisDelayedRetryScheduler_IdleTicksResetOnDue 는 due 항목 처리 시 idleTicks 가
// 0 으로 reset 되고 previous_idle_ticks 필드로 직전 idle 지속이 노출됨을 검증.
func TestRedisDelayedRetryScheduler_IdleTicksResetOnDue(t *testing.T) {
	q := newFakeRetryQueue()
	prod := new(retryMockProducer)
	buf := &safeBuffer{}

	cfg := publisher.DefaultRedisRetrySchedulerConfig()
	cfg.PollInterval = 10 * time.Millisecond
	cfg.HeartbeatEveryNIdleTicks = 100
	sched := publisher.NewRedisDelayedRetryScheduler(q, prod, cfg, captureDebugLogger(buf))

	prod.On("Publish", mock.Anything, mock.Anything).Return(nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { sched.Run(ctx); close(done) }()

	time.Sleep(55 * time.Millisecond)

	job := retryTestJob(core.PriorityNormal)
	job.ScheduledAt = time.Now().Add(-time.Second)
	require.NoError(t, sched.Enqueue(context.Background(), job, errors.New("err")))

	require.Eventually(t, func() bool {
		return countLines(buf, "retry peek returned due items") >= 1
	}, time.Second, 5*time.Millisecond, "due 항목이 처리되어야 함")

	cancel()
	<-done

	require.Contains(t, buf.String(), "previous_idle_ticks", "due 로그에 previous_idle_ticks 필드 노출")
	assert.Regexp(t, `"previous_idle_ticks":[1-9][0-9]*`, buf.String(),
		"previous_idle_ticks 가 양수여야 함 (직전 idle window 존재)")
}
