package worker_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"issuetracker/internal/crawler/core"
	"issuetracker/internal/crawler/worker"
	"issuetracker/pkg/queue"
)

// ─────────────────────────────────────────────────────────────────────────────
// Mock JobLocker
// ─────────────────────────────────────────────────────────────────────────────

type mockJobLocker struct{ mock.Mock }

func (m *mockJobLocker) Acquire(ctx context.Context, jobID string) (bool, error) {
	args := m.Called(ctx, jobID)
	return args.Bool(0), args.Error(1)
}

func (m *mockJobLocker) Release(ctx context.Context, jobID string) error {
	args := m.Called(ctx, jobID)
	return args.Error(0)
}

// ─────────────────────────────────────────────────────────────────────────────
// 헬퍼
// ─────────────────────────────────────────────────────────────────────────────

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

// ─────────────────────────────────────────────────────────────────────────────
// JobLocker 단위 테스트
// ─────────────────────────────────────────────────────────────────────────────

// TestNoopJobLocker_AlwaysAcquires는 NoopJobLocker가 항상 락 획득에 성공하는지 검증합니다.
func TestNoopJobLocker_AlwaysAcquires(t *testing.T) {
	var locker worker.NoopJobLocker

	acquired, err := locker.Acquire(context.Background(), "job-001")
	assert.NoError(t, err)
	assert.True(t, acquired)
}

// TestNoopJobLocker_ReleaseNoError는 NoopJobLocker의 Release가 항상 성공하는지 검증합니다.
func TestNoopJobLocker_ReleaseNoError(t *testing.T) {
	var locker worker.NoopJobLocker

	err := locker.Release(context.Background(), "job-001")
	assert.NoError(t, err)
}

// ─────────────────────────────────────────────────────────────────────────────
// Pool + JobLocker 통합 테스트
// ─────────────────────────────────────────────────────────────────────────────

// TestKafkaConsumerPool_JobLocker_AlreadyAcquired_SkipsWithoutCommit는
// 다른 worker가 이미 락을 보유 중일 때 처리와 commit을 모두 건너뛰는지 검증합니다.
// 메시지 유실 방지: 락 보유 워커가 처리 도중 장애로 종료되어도 해당 워커가 commit하지
// 않았다면 Kafka가 메시지를 재전달하여 재처리가 보장됩니다.
func TestKafkaConsumerPool_JobLocker_AlreadyAcquired_SkipsWithoutCommit(t *testing.T) {
	consumer := new(mockConsumer)
	producer := new(mockProducer)
	handler := new(mockJobHandler)
	contentSvc := new(mockContentService)
	locker := new(mockJobLocker)

	pool := worker.NewKafkaConsumerPoolWithOptions(
		consumer, producer, handler, contentSvc, 1,
		worker.NewCircuitBreakerRegistry(worker.DefaultCircuitBreakerConfig),
		locker,
		worker.NoopURLCache{},
	)

	job := newTestJob()
	msg := marshaledJobMsg(t, job)

	// 이미 다른 worker가 락을 보유 중
	locker.On("Acquire", mock.Anything, job.ID).Return(false, nil)

	runPoolWithLocker(t, consumer, pool, msg)

	// handler, producer, commit 모두 미호출 검증
	handler.AssertNotCalled(t, "Handle", mock.Anything, mock.Anything)
	producer.AssertNotCalled(t, "Publish", mock.Anything, mock.Anything)
	consumer.AssertNotCalled(t, "CommitMessages", mock.Anything, mock.Anything)
	locker.AssertExpectations(t)
}

// TestKafkaConsumerPool_JobLocker_Acquired_ReleasedAfterProcessing는
// 락 획득 후 처리 완료 시 Release가 호출되는지 검증합니다.
func TestKafkaConsumerPool_JobLocker_Acquired_ReleasedAfterProcessing(t *testing.T) {
	consumer := new(mockConsumer)
	producer := new(mockProducer)
	handler := new(mockJobHandler)
	contentSvc := new(mockContentService)
	locker := new(mockJobLocker)

	pool := worker.NewKafkaConsumerPoolWithOptions(
		consumer, producer, handler, contentSvc, 1,
		worker.NewCircuitBreakerRegistry(worker.DefaultCircuitBreakerConfig),
		locker,
		worker.NoopURLCache{},
	)

	job := newTestJob()
	content := newTestContent()
	msg := marshaledJobMsg(t, job)

	locker.On("Acquire", mock.Anything, job.ID).Return(true, nil)
	locker.On("Release", mock.Anything, job.ID).Return(nil)

	handler.On("Handle", mock.Anything, job).Return([]*core.Content{content}, nil)
	contentSvc.On("Store", mock.Anything, content).Return(content.ID, false, nil)
	producer.On("Publish", mock.Anything, mock.MatchedBy(func(m queue.Message) bool {
		return m.Topic == queue.TopicNormalized
	})).Return(nil)
	consumer.On("CommitMessages", mock.Anything, mock.Anything).Return(nil)

	runPoolWithLocker(t, consumer, pool, msg)

	locker.AssertCalled(t, "Release", mock.Anything, job.ID)
	handler.AssertExpectations(t)
}

// TestKafkaConsumerPool_JobLocker_AcquireError_ProceedsWithoutLock는
// 락 획득 오류 시 처리를 중단하지 않고 경고 후 계속 진행하는지 검증합니다.
func TestKafkaConsumerPool_JobLocker_AcquireError_ProceedsWithoutLock(t *testing.T) {
	consumer := new(mockConsumer)
	producer := new(mockProducer)
	handler := new(mockJobHandler)
	contentSvc := new(mockContentService)
	locker := new(mockJobLocker)

	pool := worker.NewKafkaConsumerPoolWithOptions(
		consumer, producer, handler, contentSvc, 1,
		worker.NewCircuitBreakerRegistry(worker.DefaultCircuitBreakerConfig),
		locker,
		worker.NoopURLCache{},
	)

	job := newTestJob()
	content := newTestContent()
	msg := marshaledJobMsg(t, job)

	// 락 획득 오류 — Redis 장애 시뮬레이션
	locker.On("Acquire", mock.Anything, job.ID).Return(false, errors.New("redis unavailable"))

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
	// 락 오류 시 Release는 호출되지 않아야 합니다
	locker.AssertNotCalled(t, "Release", mock.Anything, mock.Anything)
}
