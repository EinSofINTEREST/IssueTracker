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
// Mock ProcessingLock
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
// ProcessingLock 단위 테스트
// ─────────────────────────────────────────────────────────────────────────────

// TestNoopProcessingLock_AlwaysAcquires는 NoopProcessingLock 이 항상 락 획득에 성공하는지 검증합니다.
func TestNoopProcessingLock_AlwaysAcquires(t *testing.T) {
	var locker worker.NoopProcessingLock

	acquired, err := locker.Acquire(context.Background(), "any-key")
	assert.NoError(t, err)
	assert.True(t, acquired)
}

// TestNoopProcessingLock_ReleaseNoError 는 NoopProcessingLock 의 Release 가 항상 성공하는지 검증합니다.
func TestNoopProcessingLock_ReleaseNoError(t *testing.T) {
	var locker worker.NoopProcessingLock

	err := locker.Release(context.Background(), "any-key")
	assert.NoError(t, err)
}

// ProcessingKey 가 (stage, url) 페어로 일관된 키를 만들고, stage 가 다르거나 url 이 다르면 키도 다른지 검증.
func TestProcessingKey_Determinism(t *testing.T) {
	url := "https://example.com/article/1"
	a1 := worker.ProcessingKey(worker.StageFetcher, url)
	a2 := worker.ProcessingKey(worker.StageFetcher, url)
	assert.Equal(t, a1, a2, "동일 (stage, url) 은 동일 키")

	b := worker.ProcessingKey(worker.StageParser, url)
	assert.NotEqual(t, a1, b, "stage 다르면 키 다름")

	c := worker.ProcessingKey(worker.StageFetcher, "https://example.com/article/2")
	assert.NotEqual(t, a1, c, "url 다르면 키 다름")
}

// ─────────────────────────────────────────────────────────────────────────────
// Pool + ProcessingLock 통합 테스트
// ─────────────────────────────────────────────────────────────────────────────

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
	wantKey := worker.ProcessingKey(worker.StageFetcher, job.Target.URL)

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
	wantKey := worker.ProcessingKey(worker.StageFetcher, job.Target.URL)

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
	wantKey := worker.ProcessingKey(worker.StageFetcher, job.Target.URL)

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
