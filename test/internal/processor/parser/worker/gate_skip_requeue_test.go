package worker_test

// gate_skip_requeue_test.go — 이슈 #540 / PR #552 Copilot 리뷰 반영.
//
// 리뷰 지적: 기존 테스트는 지연 상수와 sentinel 식별만 검증하고, 핵심인
// "Enqueue → ScheduledAt → commit" 흐름과 enqueue 실패 시 동작을 검증하지 않았다.

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/bus"
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

// Enqueue 실패 — DLQ 로 격리한 뒤에만 commit 해야 한다.
//
// ZSET 모드는 pop 이 곧 ack 이라 미커밋으로 두어도 redeliver 가 없다. 그대로 두면 소실이므로
// DLQ 로 옮기고 commit 하는 것이 유일한 보존 경로다 (CodeRabbit 피드백).
func TestHandle_GateSkip_EnqueueFails_SendsToDLQAndCommits(t *testing.T) {
	gate := &fakeStageGate{acquired: false}
	consumer := &countingConsumer{}
	sched := &fakeRetryScheduler{failWith: errors.New("redis down")}
	producer := &capturingProducer{}

	pw := newGatedWorkerWithPublisher(consumer, producer, gate, &fakeRawSvc{}, logger.New(logger.DefaultConfig()))
	pw.SetGateSkipScheduler(sched)

	pw.Handle(context.Background(), newMsgForURL(t, "raw-542", "https://example.com/enq-fail"))

	dlq := producer.messagesTo(queue.TopicDLQ)
	require.Len(t, dlq, 1, "재큐 실패 시 DLQ 로 격리되어야 함")
	assert.Equal(t, queue.TopicFetched, dlq[0].Headers["original-topic"])
	assert.Contains(t, dlq[0].Headers["error"], "gate skip requeue failed")
	assert.Equal(t, 1, consumer.commitCount(), "DLQ 발행 성공 후에만 commit")
}

// DLQ 발행까지 실패하면 commit 하면 안 된다 — Kafka 모드에서는 redeliver 로 복구된다.
func TestHandle_GateSkip_EnqueueAndDLQFail_DoesNotCommit(t *testing.T) {
	gate := &fakeStageGate{acquired: false}
	consumer := &countingConsumer{}
	sched := &fakeRetryScheduler{failWith: errors.New("redis down")}
	producer := &capturingProducer{failWith: errors.New("kafka down")}

	pw := newGatedWorkerWithPublisher(consumer, producer, gate, &fakeRawSvc{}, logger.New(logger.DefaultConfig()))
	pw.SetGateSkipScheduler(sched)

	pw.Handle(context.Background(), newMsgForURL(t, "raw-543", "https://example.com/both-fail"))

	assert.Equal(t, 0, consumer.commitCount(),
		"DLQ 발행도 실패했으면 commit 하면 안 된다")
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

// ─────────────────────────────────────────────────────────────────────────────
// 재큐 예산 (이슈 #540, CodeRabbit 피드백)
//
// BuildRetryJob 이 새 CrawlJob 을 만들어 RetryCount 가 0 부터 시작하므로, job 자체로는
// 상한이 성립하지 않는다. stage 를 건너 누적되는 헤더 카운터가 그 역할을 한다.
// ─────────────────────────────────────────────────────────────────────────────

// 상한 도달 전에는 재큐하고, 다음 사이클로 증가된 카운터를 전달해야 한다.
func TestHandle_GateSkip_CarriesIncrementedCount(t *testing.T) {
	gate := &fakeStageGate{acquired: false}
	consumer := &countingConsumer{}
	sched := &fakeRetryScheduler{}

	pw := newGatedWorkerWithConsumer(consumer, gate, &fakeRawSvc{}, logger.New(logger.DefaultConfig()))
	pw.SetGateSkipScheduler(sched)

	msg := newMsgForURL(t, "raw-budget", "https://example.com/budget")
	msg.Headers[core.HeaderGateSkipCount] = "1"

	pw.Handle(context.Background(), msg)

	require.Equal(t, 1, sched.calls(), "상한 미도달이면 재큐되어야 함")
	got := sched.jobs[0].Target.Metadata[core.HeaderGateSkipCount]
	assert.Equal(t, "2", got, "다음 사이클로 +1 된 카운터가 전달되어야 함")
}

// 상한에 닿으면 재큐를 멈추고 DLQ 로 보내야 한다.
//
// 멈추지 않으면 gate 가 계속 점유된 URL 이 무한 순환한다. 그렇다고 조용히 버리면 운영자가
// 알 수 없으므로, 종단은 DLQ 격리 + commit 이다 (CodeRabbit 피드백으로 drop → DLQ 상향).
func TestHandle_GateSkip_StopsAtLimit_SendsToDLQ(t *testing.T) {
	gate := &fakeStageGate{acquired: false}
	consumer := &countingConsumer{}
	sched := &fakeRetryScheduler{}
	producer := &capturingProducer{}

	pw := newGatedWorkerWithPublisher(consumer, producer, gate, &fakeRawSvc{}, logger.New(logger.DefaultConfig()))
	pw.SetGateSkipScheduler(sched)

	msg := newMsgForURL(t, "raw-limit", "https://example.com/limit")
	msg.Headers[core.HeaderGateSkipCount] = strconv.Itoa(locks.MaxGateSkipRequeues)

	pw.Handle(context.Background(), msg)

	assert.Equal(t, 0, sched.calls(), "상한 도달 시 재큐하면 안 됨")
	require.Len(t, producer.messagesTo(queue.TopicDLQ), 1, "상한 도달 메시지는 DLQ 로 격리")
	assert.Equal(t, 1, consumer.commitCount(), "종단 처리 후 commit 되어야 순환이 끊긴다")
}

// 헤더가 깨져 있어도 크래시하지 않고 0 으로 취급해야 한다.
func TestHandle_GateSkip_MalformedCountTreatedAsZero(t *testing.T) {
	gate := &fakeStageGate{acquired: false}
	consumer := &countingConsumer{}
	sched := &fakeRetryScheduler{}

	pw := newGatedWorkerWithConsumer(consumer, gate, &fakeRawSvc{}, logger.New(logger.DefaultConfig()))
	pw.SetGateSkipScheduler(sched)

	msg := newMsgForURL(t, "raw-bad", "https://example.com/bad")
	msg.Headers[core.HeaderGateSkipCount] = "not-a-number"

	pw.Handle(context.Background(), msg)

	require.Equal(t, 1, sched.calls())
	assert.Equal(t, "1", sched.jobs[0].Target.Metadata[core.HeaderGateSkipCount])
}

// capturingProducer 는 발행 메시지를 보관하는 queue.Producer stub 입니다.
type capturingProducer struct {
	mu       sync.Mutex
	sent     []queue.Message
	failWith error
}

func (c *capturingProducer) Publish(_ context.Context, msg queue.Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failWith != nil {
		return c.failWith
	}
	c.sent = append(c.sent, msg)
	return nil
}

func (c *capturingProducer) PublishBatch(ctx context.Context, msgs []queue.Message) error {
	for _, m := range msgs {
		if err := c.Publish(ctx, m); err != nil {
			return err
		}
	}
	return nil
}

func (c *capturingProducer) Close() error { return nil }

func (c *capturingProducer) messagesTo(topic string) []queue.Message {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []queue.Message
	for _, m := range c.sent {
		if m.Topic == topic {
			out = append(out, m)
		}
	}
	return out
}

// newGatedWorkerWithPublisher 는 DLQ 발행 관측을 위해 publisher 까지 주입합니다.
func newGatedWorkerWithPublisher(consumer *countingConsumer, producer *capturingProducer, gate locks.StageGate, rawSvc *fakeRawSvc, log *logger.Logger) *parserWorker.Worker {
	return parserWorker.NewWorker(
		consumer,
		bus.New(producer, nil, log),
		rawSvc,
		nil, nil, nil, nil,
		gate,
		nil, nil, nil, nil,
		0, 0, 1,
		log,
	)
}
