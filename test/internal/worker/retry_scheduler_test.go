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
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/crawler/core"
	"issuetracker/internal/crawler/worker"
	"issuetracker/pkg/logger"
	"issuetracker/pkg/queue"
	pkgredis "issuetracker/pkg/redis"
)

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
			producer := new(mockProducer)
			sched := worker.NewKafkaImmediateRetryScheduler(producer)

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
	producer := new(mockProducer)
	sched := worker.NewKafkaImmediateRetryScheduler(producer)

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
	producer := new(mockProducer)
	sched := worker.NewKafkaImmediateRetryScheduler(producer)

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
// 진짜 Redis 가 없어도 Run 루프와 Enqueue/Pop 흐름을 결정적으로 검증합니다.
type fakeRetryQueue struct {
	mu               sync.Mutex
	items            []fakeRetryItem // ScheduledAt 정렬 유지
	enqueueErr       error
	popErr           error
	enqueueCallCount atomic.Int32
	popCallCount     atomic.Int32
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
	// 동일 jobID 가 있으면 덮어씀 (실제 ZADD 동작과 일치)
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

func (f *fakeRetryQueue) PopDueRetries(_ context.Context, now time.Time, limit int) ([]pkgredis.DueRetry, error) {
	f.popCallCount.Add(1)
	if f.popErr != nil {
		return nil, f.popErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	out := make([]pkgredis.DueRetry, 0)
	remaining := make([]fakeRetryItem, 0, len(f.items))
	for _, it := range f.items {
		if !it.scheduledAt.After(now) && len(out) < limit {
			out = append(out, pkgredis.DueRetry{JobID: it.jobID, Payload: it.payload})
			continue
		}
		remaining = append(remaining, it)
	}
	f.items = remaining
	return out, nil
}

func (f *fakeRetryQueue) sortLocked() {
	// 단순 삽입 정렬 — 테스트용 (n 이 작음)
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
	prod := new(mockProducer)
	sched := worker.NewRedisDelayedRetryScheduler(q, prod, worker.DefaultRedisRetrySchedulerConfig(), retrySchedTestLogger())

	job := retryTestJob(core.PriorityNormal)
	require.NoError(t, sched.Enqueue(context.Background(), job, errors.New("upstream 503")))

	snap := q.snapshot()
	require.Len(t, snap, 1)
	assert.Equal(t, job.ID, snap[0].jobID)
	assert.WithinDuration(t, job.ScheduledAt, snap[0].scheduledAt, time.Second)

	// payload 의 last_err 디코드 검증
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
	prod := new(mockProducer)
	cfg := worker.DefaultRedisRetrySchedulerConfig()
	cfg.PollInterval = 10 * time.Millisecond
	sched := worker.NewRedisDelayedRetryScheduler(q, prod, cfg, retrySchedTestLogger())

	job := retryTestJob(core.PriorityHigh)
	job.ScheduledAt = time.Now().Add(-time.Second) // due
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
	prod := new(mockProducer)
	cfg := worker.DefaultRedisRetrySchedulerConfig()
	cfg.PollInterval = 10 * time.Millisecond
	sched := worker.NewRedisDelayedRetryScheduler(q, prod, cfg, retrySchedTestLogger())

	job := retryTestJob(core.PriorityNormal)
	job.ScheduledAt = time.Now().Add(10 * time.Second) // 미래
	require.NoError(t, sched.Enqueue(context.Background(), job, errors.New("err")))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { sched.Run(ctx); close(done) }()

	// 폴링이 몇 번 일어날 시간 대기
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	assert.Len(t, q.snapshot(), 1, "미래 항목은 큐에 그대로 남아 있어야 함")
	prod.AssertNotCalled(t, "Publish", mock.Anything, mock.Anything)
}

// TestRedisDelayedRetryScheduler_Run_PublishFailure_ReEnqueues 는 Kafka publish 실패 시
// 항목이 짧은 backoff 로 재 enqueue 됨을 검증.
func TestRedisDelayedRetryScheduler_Run_PublishFailure_ReEnqueues(t *testing.T) {
	q := newFakeRetryQueue()
	prod := new(mockProducer)
	cfg := worker.DefaultRedisRetrySchedulerConfig()
	cfg.PollInterval = 10 * time.Millisecond
	cfg.RepublishFailureBackoff = 5 * time.Second // 다시 due 가 되지 않을 만큼 충분히 길게
	sched := worker.NewRedisDelayedRetryScheduler(q, prod, cfg, retrySchedTestLogger())

	job := retryTestJob(core.PriorityNormal)
	job.ScheduledAt = time.Now().Add(-time.Second)
	require.NoError(t, sched.Enqueue(context.Background(), job, errors.New("e")))

	prod.On("Publish", mock.Anything, mock.Anything).Return(errors.New("kafka down"))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { sched.Run(ctx); close(done) }()

	// 첫 publish 실패 후 재 enqueue 가 일어날 때까지 대기 (enqueue >=2 = 최초 + 재)
	require.Eventually(t, func() bool {
		return q.enqueueCallCount.Load() >= 2
	}, time.Second, 5*time.Millisecond, "publish 실패 후 재 enqueue 발생해야 함")

	cancel()
	<-done

	snap := q.snapshot()
	require.Len(t, snap, 1, "publish 실패한 항목이 다시 큐에 보관됨")
	assert.True(t, snap[0].scheduledAt.After(time.Now()), "재 enqueue 의 ScheduledAt 이 미래로 설정")
}

// TestRedisDelayedRetryScheduler_Run_PopError_LogsAndContinues 는 Pop 실패 시 다음
// 폴 사이클로 계속 진행됨을 검증 (panic 없이 회복).
func TestRedisDelayedRetryScheduler_Run_PopError_LogsAndContinues(t *testing.T) {
	q := newFakeRetryQueue()
	q.popErr = errors.New("redis transient")
	prod := new(mockProducer)
	cfg := worker.DefaultRedisRetrySchedulerConfig()
	cfg.PollInterval = 10 * time.Millisecond
	sched := worker.NewRedisDelayedRetryScheduler(q, prod, cfg, retrySchedTestLogger())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { sched.Run(ctx); close(done) }()

	require.Eventually(t, func() bool {
		return q.popCallCount.Load() >= 3 // 여러 폴 사이클 진행 = 패닉/조기 종료 없음
	}, time.Second, 5*time.Millisecond)

	cancel()
	<-done
	prod.AssertNotCalled(t, "Publish", mock.Anything, mock.Anything)
}

// TestRedisDelayedRetryScheduler_DefaultConfigBoundary 는 cfg 의 0/음수 값이 default 로
// 보정됨을 검증 (NewRedisDelayedRetryScheduler 의 fail-safe).
func TestRedisDelayedRetryScheduler_DefaultConfigBoundary(t *testing.T) {
	q := newFakeRetryQueue()
	prod := new(mockProducer)
	// 모든 필드 0 — default 로 보정되어야 함
	sched := worker.NewRedisDelayedRetryScheduler(q, prod, worker.RedisRetrySchedulerConfig{}, retrySchedTestLogger())

	// Run 이 정상 시작되어 폴 1회 이상 호출되어야 함 (default PollInterval=1s 면 너무 길어
	// 테스트 timing 이 위험하므로, ctx 즉시 cancel 후 panic/start 여부만 확인)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 즉시 취소
	done := make(chan struct{})
	go func() { sched.Run(ctx); close(done) }()
	select {
	case <-done:
		// 정상 종료
	case <-time.After(time.Second):
		t.Fatal("Run 이 ctx cancel 후 즉시 종료되어야 함")
	}
}
