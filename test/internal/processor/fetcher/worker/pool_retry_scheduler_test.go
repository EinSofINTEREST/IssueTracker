package worker_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"issuetracker/internal/processor/fetcher/worker"
	"issuetracker/internal/publisher"
	"issuetracker/pkg/logger"
	pkgredis "issuetracker/pkg/redis"
)

// poolRetryFakeQueue 는 publisher 측 retryQueueClient 인터페이스를 구조적으로 만족하는
// in-memory 더블입니다. pool.SetRetryScheduler 통합 시나리오 전용 (다른 publisher 단위 테스트는
// test/internal/publisher/retry_test.go 의 자체 fake 사용).
type poolRetryFakeQueue struct {
	mu    sync.Mutex
	items []poolRetryItem
}

type poolRetryItem struct {
	jobID       string
	payload     []byte
	scheduledAt time.Time
}

func newPoolRetryFakeQueue() *poolRetryFakeQueue { return &poolRetryFakeQueue{} }

func (f *poolRetryFakeQueue) EnqueueRetry(_ context.Context, jobID string, payload []byte, scheduledAt time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.items = append(f.items, poolRetryItem{jobID: jobID, payload: payload, scheduledAt: scheduledAt})
	return nil
}

func (f *poolRetryFakeQueue) PeekDueRetries(_ context.Context, _ time.Time, _ int) ([]pkgredis.DueRetry, error) {
	return nil, nil
}

func (f *poolRetryFakeQueue) AckRetry(_ context.Context, _ string) error { return nil }

func (f *poolRetryFakeQueue) snapshotLen() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.items)
}

// idle counter helper (RedisDelayedRetryScheduler 가 폴링되지 않도록 PollInterval=1h)
var poolRetrySchedulerCounter atomic.Int32

// TestKafkaConsumerPool_SetRetryScheduler_BypassesInlinePublish 는 SetRetryScheduler 로
// 주입한 publisher.RetryScheduler 구현체가 호출되어 inline producer.Publish 가 우회됨을 검증.
func TestKafkaConsumerPool_SetRetryScheduler_BypassesInlinePublish(t *testing.T) {
	consumer := new(mockConsumer)
	producer := new(mockProducer)
	handler := new(mockJobHandler)
	contentSvc := new(mockContentService)

	pool := worker.NewKafkaConsumerPool(consumer, producer, handler, contentSvc, 1)

	q := newPoolRetryFakeQueue()
	customScheduler := publisher.NewRedisDelayedRetryScheduler(q, producer, publisher.RedisRetrySchedulerConfig{
		PollInterval:            time.Hour,
		BatchSize:               1,
		RepublishFailureBackoff: time.Hour,
	}, logger.New(logger.DefaultConfig()))
	pool.SetRetryScheduler(customScheduler)

	job := newTestJob()
	job.RetryCount = 1
	msg := marshaledJobMsg(t, job)

	handler.On("Handle", mock.Anything, job).Return(nil, errors.New("transient"))
	consumer.On("CommitMessages", mock.Anything, mock.Anything).Return(nil)

	runPool(t, consumer, pool, msg)

	// inline producer.Publish 는 호출되지 않아야 함 — 모든 retry 가 Redis 경로로
	producer.AssertNotCalled(t, "Publish", mock.Anything, mock.Anything)
	assert.Equal(t, 1, q.snapshotLen(), "주입된 RedisDelayedRetryScheduler 가 enqueue 받았음")
	consumer.AssertCalled(t, "CommitMessages", mock.Anything, mock.Anything)

	// counter referenced to silence unused-var lint on package init paths
	_ = poolRetrySchedulerCounter.Load()
}
