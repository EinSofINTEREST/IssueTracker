package worker_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"

	"issuetracker/internal/crawler/core"
	"issuetracker/internal/crawler/worker"
	"issuetracker/internal/locks"
	"issuetracker/pkg/queue"
)

// ─────────────────────────────────────────────────────────────────────────────
// Mock ProcessingLock — 본 파일 안에서만 사용 (test/internal/locks 의 동일명 mock 와 분리).
// 단위 테스트 (NoopProcessingLock / ProcessingKey) 는 test/internal/locks 에 있음.
// ─────────────────────────────────────────────────────────────────────────────

type mockProcessingLock struct{ mock.Mock }

func (m *mockProcessingLock) Acquire(ctx context.Context, key string) (bool, error) {
	args := m.Called(ctx, key)
	return args.Bool(0), args.Error(1)
}

func (m *mockProcessingLock) Release(ctx context.Context, key string) error {
	args := m.Called(ctx, key)
	return args.Error(0)
}

func runPoolWithLocker(t *testing.T, consumer *mockConsumer, pool *worker.KafkaConsumerPool, msg *queue.Message) {
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

// TestKafkaConsumerPool_ProcessingLock_AlreadyAcquired_SkipsWithoutCommit 는
// 다른 worker 가 이미 락을 보유 중일 때 처리와 commit 을 모두 건너뛰는지 검증합니다.
// 메시지 유실 방지: 락 보유 워커가 처리 도중 장애로 종료되어도 해당 워커가 commit 하지
// 않았다면 Kafka 가 메시지를 재전달하여 재처리가 보장됩니다.
func TestKafkaConsumerPool_ProcessingLock_AlreadyAcquired_SkipsWithoutCommit(t *testing.T) {
	consumer := new(mockConsumer)
	producer := new(mockProducer)
	handler := new(mockJobHandler)
	contentSvc := new(mockContentService)
	locker := new(mockProcessingLock)

	pool := worker.NewKafkaConsumerPoolWithOptions(
		consumer, producer, handler, contentSvc, 1,
		worker.NewCircuitBreakerRegistry(worker.DefaultCircuitBreakerConfig, nil),
		locker,
	)

	job := newTestJob()
	msg := marshaledJobMsg(t, job)
	wantKey := locks.ProcessingKey(locks.StageFetcher, job.Target.URL)

	// 이미 다른 worker 가 락을 보유 중
	locker.On("Acquire", mock.Anything, wantKey).Return(false, nil)

	runPoolWithLocker(t, consumer, pool, msg)

	// handler, producer, commit 모두 미호출 검증
	handler.AssertNotCalled(t, "Handle", mock.Anything, mock.Anything)
	producer.AssertNotCalled(t, "Publish", mock.Anything, mock.Anything)
	consumer.AssertNotCalled(t, "CommitMessages", mock.Anything, mock.Anything)
	locker.AssertExpectations(t)
}

// TestKafkaConsumerPool_ProcessingLock_Acquired_ReleasedAfterProcessing 는
// 락 획득 후 처리 완료 시 Release 가 호출되는지 검증합니다.
func TestKafkaConsumerPool_ProcessingLock_Acquired_ReleasedAfterProcessing(t *testing.T) {
	consumer := new(mockConsumer)
	producer := new(mockProducer)
	handler := new(mockJobHandler)
	contentSvc := new(mockContentService)
	locker := new(mockProcessingLock)

	pool := worker.NewKafkaConsumerPoolWithOptions(
		consumer, producer, handler, contentSvc, 1,
		worker.NewCircuitBreakerRegistry(worker.DefaultCircuitBreakerConfig, nil),
		locker,
	)

	job := newTestJob()
	content := newTestContent()
	msg := marshaledJobMsg(t, job)
	wantKey := locks.ProcessingKey(locks.StageFetcher, job.Target.URL)

	locker.On("Acquire", mock.Anything, wantKey).Return(true, nil)
	locker.On("Release", mock.Anything, wantKey).Return(nil)

	handler.On("Handle", mock.Anything, job).Return([]*core.Content{content}, nil)
	contentSvc.On("Store", mock.Anything, content).Return(content.ID, false, nil)
	producer.On("Publish", mock.Anything, mock.MatchedBy(func(m queue.Message) bool {
		return m.Topic == queue.TopicNormalized
	})).Return(nil)
	consumer.On("CommitMessages", mock.Anything, mock.Anything).Return(nil)

	runPoolWithLocker(t, consumer, pool, msg)

	locker.AssertCalled(t, "Release", mock.Anything, wantKey)
	handler.AssertExpectations(t)
}

// TestKafkaConsumerPool_ProcessingLock_AcquireError_ProceedsWithoutLock 는
// 락 획득 오류 시 처리를 중단하지 않고 경고 후 계속 진행하는지 검증합니다.
func TestKafkaConsumerPool_ProcessingLock_AcquireError_ProceedsWithoutLock(t *testing.T) {
	consumer := new(mockConsumer)
	producer := new(mockProducer)
	handler := new(mockJobHandler)
	contentSvc := new(mockContentService)
	locker := new(mockProcessingLock)

	pool := worker.NewKafkaConsumerPoolWithOptions(
		consumer, producer, handler, contentSvc, 1,
		worker.NewCircuitBreakerRegistry(worker.DefaultCircuitBreakerConfig, nil),
		locker,
	)

	job := newTestJob()
	content := newTestContent()
	msg := marshaledJobMsg(t, job)
	wantKey := locks.ProcessingKey(locks.StageFetcher, job.Target.URL)

	// 락 획득 오류 — Redis 장애 시뮬레이션
	locker.On("Acquire", mock.Anything, wantKey).Return(false, errors.New("redis unavailable"))

	// 락 오류에도 처리는 계속 진행되어야 합니다
	handler.On("Handle", mock.Anything, job).Return([]*core.Content{content}, nil)
	contentSvc.On("Store", mock.Anything, content).Return(content.ID, false, nil)
	producer.On("Publish", mock.Anything, mock.MatchedBy(func(m queue.Message) bool {
		return m.Topic == queue.TopicNormalized
	})).Return(nil)
	consumer.On("CommitMessages", mock.Anything, mock.Anything).Return(nil)

	runPoolWithLocker(t, consumer, pool, msg)

	handler.AssertExpectations(t)
	producer.AssertExpectations(t)
	// 락 오류 시 Release 는 호출되지 않아야 합니다
	locker.AssertNotCalled(t, "Release", mock.Anything, mock.Anything)
}
