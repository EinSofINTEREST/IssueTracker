package worker_test

// gate_skip_requeue_test.go — 이슈 #540 / PR #552 Copilot 리뷰 반영.
//
// 리뷰 지적: 기존 테스트는 지연 상수와 sentinel 식별만 검증하고, 핵심인
// "Enqueue → ScheduledAt → commit" 흐름과 enqueue 실패 시 동작을 검증하지 않았다.

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/locks"
	"issuetracker/internal/processor/fetcher/core"
	parserWorker "issuetracker/internal/processor/parser/worker"
	"issuetracker/pkg/logger"
	"issuetracker/pkg/queue"
)

// fakeRetryScheduler 는 bus.RetryScheduler 의 기록용 stub 입니다.
type fakeRetryScheduler struct {
	mu       sync.Mutex
	jobs     []*core.CrawlJob
	lastErrs []error
	failWith error
}

func (f *fakeRetryScheduler) Enqueue(_ context.Context, job *core.CrawlJob, lastErr error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failWith != nil {
		return f.failWith
	}
	f.jobs = append(f.jobs, job)
	f.lastErrs = append(f.lastErrs, lastErr)
	return nil
}

func (f *fakeRetryScheduler) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.jobs)
}

// countingConsumer 는 commit 횟수를 세는 bus.Consumer stub 입니다.
type countingConsumer struct {
	mu      sync.Mutex
	commits int
}

func (c *countingConsumer) FetchMessage(ctx context.Context) (*queue.Message, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (c *countingConsumer) CommitMessages(_ context.Context, _ ...*queue.Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.commits++
	return nil
}

func (c *countingConsumer) Close() error { return nil }

func (c *countingConsumer) commitCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.commits
}

// gate 선점 시 지연 재큐 후 commit 되어야 한다 — 미커밋으로 두면 이 offset 을 커밋할 주체가
// 없어 소실된다 (ZSET 모드에서는 pop=ack 이라 확정 소실).
func TestHandle_GateSkip_RequeuesWithDelayAndCommits(t *testing.T) {
	gate := &fakeStageGate{acquired: false}
	consumer := &countingConsumer{}
	sched := &fakeRetryScheduler{}

	pw := newGatedWorkerWithConsumer(consumer, gate, &fakeRawSvc{}, logger.New(logger.DefaultConfig()))
	pw.SetGateSkipScheduler(sched)

	msg := newMsgForURL(t, "raw-540", "https://example.com/gate-skip")
	pw.Handle(context.Background(), msg)

	require.Equal(t, 1, sched.calls(), "gate 선점 시 재큐되어야 함")
	assert.Equal(t, 1, consumer.commitCount(), "재큐 후 commit 되어야 소실되지 않음")

	job := sched.jobs[0]
	assert.Equal(t, "https://example.com/gate-skip", job.Target.URL)
	assert.WithinDuration(t, time.Now().Add(locks.GateSkipRetryDelay), job.ScheduledAt, time.Minute,
		"ProcessingLock TTL 의 절반만큼 지연되어야 함")
	assert.ErrorIs(t, sched.lastErrs[0], locks.ErrStageGateHeld,
		"재큐 사유가 실패와 구분되어야 함")
}

// 스케줄러 미주입 (Redis 부재) — 재큐할 수단이 없으므로 commit 하지 않고 redeliver 에 맡긴다.
// 재큐 없이 commit 하면 그때야말로 확정 소실이다.
func TestHandle_GateSkip_NoScheduler_DoesNotCommit(t *testing.T) {
	gate := &fakeStageGate{acquired: false}
	consumer := &countingConsumer{}

	pw := newGatedWorkerWithConsumer(consumer, gate, &fakeRawSvc{}, logger.New(logger.DefaultConfig()))

	pw.Handle(context.Background(), newMsgForURL(t, "raw-541", "https://example.com/no-sched"))

	assert.Equal(t, 0, consumer.commitCount(),
		"재큐 수단이 없으면 commit 하지 않아야 redeliver 가능")
}

// Enqueue 실패 — 재큐되지 않았으므로 commit 하면 안 된다.
func TestHandle_GateSkip_EnqueueFails_DoesNotCommit(t *testing.T) {
	gate := &fakeStageGate{acquired: false}
	consumer := &countingConsumer{}
	sched := &fakeRetryScheduler{failWith: errors.New("redis down")}

	pw := newGatedWorkerWithConsumer(consumer, gate, &fakeRawSvc{}, logger.New(logger.DefaultConfig()))
	pw.SetGateSkipScheduler(sched)

	pw.Handle(context.Background(), newMsgForURL(t, "raw-542", "https://example.com/enq-fail"))

	assert.Equal(t, 0, consumer.commitCount(),
		"재큐 실패 시 commit 하면 메시지가 사라진다")
}

// gate 를 획득한 정상 경로는 재큐하지 않는다 — gate-skip 경로가 오작동하면 모든 메시지가
// 중복 처리된다.
func TestHandle_GateAcquired_DoesNotRequeue(t *testing.T) {
	gate := &fakeStageGate{acquired: true}
	consumer := &countingConsumer{}
	sched := &fakeRetryScheduler{}

	pw := newGatedWorkerWithConsumer(consumer, gate, &fakeRawSvc{}, logger.New(logger.DefaultConfig()))
	pw.SetGateSkipScheduler(sched)

	pw.Handle(context.Background(), newMsgForURL(t, "raw-543", "https://example.com/normal"))

	assert.Equal(t, 0, sched.calls(), "정상 획득 시 gate-skip 재큐가 일어나면 안 됨")
}

// newGatedWorkerWithConsumer 는 commit 관측을 위해 consumer 를 주입한 Worker 를 만듭니다.
func newGatedWorkerWithConsumer(consumer *countingConsumer, gate locks.StageGate, rawSvc *fakeRawSvc, log *logger.Logger) *parserWorker.Worker {
	return parserWorker.NewWorker(
		consumer, // consumer — commit 관측용
		nil,      // pub
		rawSvc,   // rawSvc
		nil,      // contentSvc
		nil,      // parser
		nil,      // resolver
		nil,      // sampleSvc
		gate,     // stage gate
		nil,      // llmGen
		nil,      // failureCounter
		nil,      // rawIDTracker
		nil,      // upgrader
		0,        // emptyBodyTitleMin
		0,        // emptyBodyContentMin
		1,        // workerCount
		log,
	)
}
