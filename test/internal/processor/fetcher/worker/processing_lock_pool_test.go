package worker_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"issuetracker/internal/locks"
	"issuetracker/internal/processor/fetcher/core"
	"issuetracker/internal/processor/fetcher/worker"
	"issuetracker/pkg/queue"
)

// ─────────────────────────────────────────────────────────────────────────────
// Mock StageGate — fetcher pool 의 StageGate 분기 (acquire / skip / release) 검증용.
// 이슈 #356 — fetcher 가 ProcessingLock 직접 호출 대신 StageGate 합성 사용으로 변경됨에 따라
// 본 mock 는 StageGate 인터페이스를 구현. ProcessingLock 자체 단위 테스트는 test/internal/locks 참조.
// ─────────────────────────────────────────────────────────────────────────────

type mockStageGate struct {
	mock.Mock
	releaseCalls atomic.Int32
}

func (m *mockStageGate) Acquire(ctx context.Context, url string) (func(), bool, error) {
	args := m.Called(ctx, url)
	if err := args.Error(2); err != nil {
		return nil, false, err
	}
	acquired := args.Bool(1)
	if !acquired {
		return nil, false, nil
	}
	return func() { m.releaseCalls.Add(1) }, true, nil
}

func (m *mockStageGate) ReleaseCalls() int32 { return m.releaseCalls.Load() }

func runPoolWithGate(t *testing.T, consumer *mockConsumer, pool *worker.KafkaConsumerPool, msg *queue.Message) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())

	consumer.On("FetchMessage", mock.Anything).Return(msg, nil).Once()
	consumer.On("FetchMessage", mock.Anything).
		Run(func(_ mock.Arguments) { cancel() }).
		Return(nil, context.Canceled)
	consumer.On("Close").Return(nil)

	pool.Start(ctx)
	<-ctx.Done()

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopCancel()
	_ = pool.Stop(stopCtx)
}

// TestKafkaConsumerPool_StageGate_AlreadyAcquired_SkipsWithoutCommit 는
// 다른 worker 가 이미 lock 을 보유 중일 때 처리와 commit 을 모두 건너뛰는지 검증합니다.
// 메시지 유실 방지: lock 보유 worker 가 처리 도중 장애로 종료되어도 해당 worker 가 commit 하지
// 않았다면 Kafka 가 메시지를 재전달하여 재처리가 보장됩니다.
func TestKafkaConsumerPool_StageGate_AlreadyAcquired_SkipsWithoutCommit(t *testing.T) {
	consumer := new(mockConsumer)
	producer := new(mockProducer)
	handler := new(mockJobHandler)
	contentSvc := new(mockContentService)
	gate := new(mockStageGate)

	pool := worker.NewKafkaConsumerPoolWithOptions(
		consumer, newTestPublisher(producer), handler, contentSvc, 1,
		worker.NewCircuitBreakerRegistry(worker.DefaultCircuitBreakerConfig, nil),
		gate,
	)

	job := newTestJob()
	msg := marshaledJobMsg(t, job)

	// 이미 다른 worker 가 lock 을 보유 중 (acquired=false, err=nil)
	gate.On("Acquire", mock.Anything, job.Target.URL).Return(nil, false, nil)

	runPoolWithGate(t, consumer, pool, msg)

	// handler, producer, commit 모두 미호출 검증
	handler.AssertNotCalled(t, "Handle", mock.Anything, mock.Anything)
	producer.AssertNotCalled(t, "Publish", mock.Anything, mock.Anything)
	consumer.AssertNotCalled(t, "CommitMessages", mock.Anything, mock.Anything)
	gate.AssertExpectations(t)
	assert.Equal(t, int32(0), gate.ReleaseCalls(), "skip 시 release 호출 X")
}

// TestKafkaConsumerPool_StageGate_Acquired_ReleasedAfterProcessing 는
// gate 획득 후 처리 완료 시 release 가 호출되는지 검증합니다.
func TestKafkaConsumerPool_StageGate_Acquired_ReleasedAfterProcessing(t *testing.T) {
	consumer := new(mockConsumer)
	producer := new(mockProducer)
	handler := new(mockJobHandler)
	contentSvc := new(mockContentService)
	gate := new(mockStageGate)

	pool := worker.NewKafkaConsumerPoolWithOptions(
		consumer, newTestPublisher(producer), handler, contentSvc, 1,
		worker.NewCircuitBreakerRegistry(worker.DefaultCircuitBreakerConfig, nil),
		gate,
	)

	job := newTestJob()
	content := newTestContent()
	msg := marshaledJobMsg(t, job)

	gate.On("Acquire", mock.Anything, job.Target.URL).Return(nil, true, nil)
	handler.On("Handle", mock.Anything, job).Return([]*core.Content{content}, nil)
	contentSvc.On("Store", mock.Anything, content).Return(content.ID, false, nil)
	producer.On("Publish", mock.Anything, mock.MatchedBy(func(m queue.Message) bool {
		return m.Topic == queue.TopicNormalized
	})).Return(nil)
	consumer.On("CommitMessages", mock.Anything, mock.Anything).Return(nil)

	runPoolWithGate(t, consumer, pool, msg)

	assert.Equal(t, int32(1), gate.ReleaseCalls(), "성공 처리 후 release 1회 호출")
	handler.AssertExpectations(t)
}

// TestKafkaConsumerPool_StageGate_AcquireError_ProceedsWithoutGate 는
// gate 획득 오류 (ctx 정상 상태에서) 시 처리를 중단하지 않고 경고 후 계속 진행하는지 검증합니다.
// ctx cancel 케이스는 fetcher 내부에서 별도 분기로 처리 (return err) — 본 테스트는 인프라 오류 경로만.
//
// 결정성 (determinism): cancel 시점을 Publish.Once 직후로 동기화 — 첫 msg 의 fail-open 경로가
// 완료된 후에만 polling 의 두 번째 FetchMessage 가 cancel + ctx.Canceled 반환. 이로써 ctx
// race 가 사라져 dispatch site 의 `if ctx.Err() != nil { return err }` 분기가 우회됨.
func TestKafkaConsumerPool_StageGate_AcquireError_ProceedsWithoutGate(t *testing.T) {
	consumer := new(mockConsumer)
	producer := new(mockProducer)
	handler := new(mockJobHandler)
	contentSvc := new(mockContentService)
	gate := new(mockStageGate)

	pool := worker.NewKafkaConsumerPoolWithOptions(
		consumer, newTestPublisher(producer), handler, contentSvc, 1,
		worker.NewCircuitBreakerRegistry(worker.DefaultCircuitBreakerConfig, nil),
		gate,
	)

	job := newTestJob()
	content := newTestContent()
	msg := marshaledJobMsg(t, job)

	// gate 획득 오류 — Redis 장애 시뮬레이션. ctx 는 정상 상태.
	gate.On("Acquire", mock.Anything, job.Target.URL).Return(nil, false, errors.New("redis unavailable"))

	// gate 오류에도 처리는 계속 진행 (graceful degrade)
	published := make(chan struct{}, 1)
	handler.On("Handle", mock.Anything, job).Return([]*core.Content{content}, nil)
	contentSvc.On("Store", mock.Anything, content).Return(content.ID, false, nil)
	producer.On("Publish", mock.Anything, mock.MatchedBy(func(m queue.Message) bool {
		return m.Topic == queue.TopicNormalized
	})).
		Run(func(_ mock.Arguments) { published <- struct{}{} }).
		Return(nil)
	consumer.On("CommitMessages", mock.Anything, mock.Anything).Return(nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	consumer.On("FetchMessage", mock.Anything).Return(msg, nil).Once()
	consumer.On("FetchMessage", mock.Anything).
		Run(func(_ mock.Arguments) {
			// 첫 메시지의 fail-open 처리가 Publish 까지 도달한 뒤 cancel.
			<-published
			cancel()
		}).
		Return(nil, context.Canceled)
	consumer.On("Close").Return(nil)

	pool.Start(ctx)
	<-ctx.Done()
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopCancel()
	_ = pool.Stop(stopCtx)

	handler.AssertExpectations(t)
	producer.AssertExpectations(t)
	assert.Equal(t, int32(0), gate.ReleaseCalls(), "gate 오류 시 release 호출 X")
}

// 컴파일 타임 contract — locks.StageGate 인터페이스 만족 확인.
var _ locks.StageGate = (*mockStageGate)(nil)
