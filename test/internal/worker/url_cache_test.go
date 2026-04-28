package worker_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"issuetracker/internal/crawler/core"
	"issuetracker/internal/crawler/worker"
	"issuetracker/pkg/queue"
)

// ─────────────────────────────────────────────────────────────────────────────
// Mock URLCache
// ─────────────────────────────────────────────────────────────────────────────

type mockURLCache struct{ mock.Mock }

func (m *mockURLCache) Exists(ctx context.Context, url string) (bool, error) {
	args := m.Called(ctx, url)
	return args.Bool(0), args.Error(1)
}

func (m *mockURLCache) Set(ctx context.Context, url string) error {
	args := m.Called(ctx, url)
	return args.Error(0)
}

// ─────────────────────────────────────────────────────────────────────────────
// NoopURLCache 단위 테스트
// ─────────────────────────────────────────────────────────────────────────────

// TestNoopURLCache_ExistsAlwaysFalse는 NoopURLCache가 항상 false를 반환하는지 검증합니다.
func TestNoopURLCache_ExistsAlwaysFalse(t *testing.T) {
	var cache worker.NoopURLCache

	exists, err := cache.Exists(context.Background(), "https://example.com/article")
	assert.NoError(t, err)
	assert.False(t, exists)
}

// TestNoopURLCache_SetNoError는 NoopURLCache의 Set이 항상 성공하는지 검증합니다.
func TestNoopURLCache_SetNoError(t *testing.T) {
	var cache worker.NoopURLCache

	err := cache.Set(context.Background(), "https://example.com/article")
	assert.NoError(t, err)
}

// ─────────────────────────────────────────────────────────────────────────────
// Pool + URLCache 통합 테스트
// ─────────────────────────────────────────────────────────────────────────────

// TestKafkaConsumerPool_URLCache_Hit_SkipsWithCommit는
// URL이 캐시에 존재할 때 handler 호출 없이 commit하여 건너뛰는지 검증합니다.
func TestKafkaConsumerPool_URLCache_Hit_SkipsWithCommit(t *testing.T) {
	consumer := new(mockConsumer)
	producer := new(mockProducer)
	handler := new(mockJobHandler)
	contentSvc := new(mockContentService)
	urlCache := new(mockURLCache)

	pool := worker.NewKafkaConsumerPoolWithOptions(
		consumer, producer, handler, contentSvc, 1,
		worker.NewCircuitBreakerRegistry(worker.DefaultCircuitBreakerConfig, nil),
		worker.NoopJobLocker{},
		urlCache,
	)

	job := newTestJob()
	msg := marshaledJobMsg(t, job)

	// URL이 이미 캐시에 존재
	urlCache.On("Exists", mock.Anything, job.Target.URL).Return(true, nil)
	consumer.On("CommitMessages", mock.Anything, mock.Anything).Return(nil)

	runPool(t, consumer, pool, msg)

	// handler는 호출되지 않아야 합니다
	handler.AssertNotCalled(t, "Handle", mock.Anything, mock.Anything)
	// commit은 호출되어야 합니다 (메시지 소비 완료)
	consumer.AssertCalled(t, "CommitMessages", mock.Anything, mock.Anything)
	urlCache.AssertExpectations(t)
}

// TestKafkaConsumerPool_URLCache_Miss_ProceedsAndSets는
// URL이 캐시에 없을 때 정상 처리 후 캐시에 등록하는지 검증합니다.
func TestKafkaConsumerPool_URLCache_Miss_ProceedsAndSets(t *testing.T) {
	consumer := new(mockConsumer)
	producer := new(mockProducer)
	handler := new(mockJobHandler)
	contentSvc := new(mockContentService)
	urlCache := new(mockURLCache)

	pool := worker.NewKafkaConsumerPoolWithOptions(
		consumer, producer, handler, contentSvc, 1,
		worker.NewCircuitBreakerRegistry(worker.DefaultCircuitBreakerConfig, nil),
		worker.NoopJobLocker{},
		urlCache,
	)

	job := newTestJob()
	content := newTestContent()
	msg := marshaledJobMsg(t, job)

	// URL이 캐시에 없음
	urlCache.On("Exists", mock.Anything, job.Target.URL).Return(false, nil)
	// 성공 후 캐시 등록
	urlCache.On("Set", mock.Anything, job.Target.URL).Return(nil)

	handler.On("Handle", mock.Anything, job).Return([]*core.Content{content}, nil)
	contentSvc.On("Store", mock.Anything, content).Return(content.ID, false, nil)
	producer.On("Publish", mock.Anything, mock.MatchedBy(func(m queue.Message) bool {
		return m.Topic == queue.TopicNormalized
	})).Return(nil)
	consumer.On("CommitMessages", mock.Anything, mock.Anything).Return(nil)

	runPool(t, consumer, pool, msg)

	handler.AssertExpectations(t)
	urlCache.AssertCalled(t, "Set", mock.Anything, job.Target.URL)
}

// TestKafkaConsumerPool_URLCache_ExistsError_ProceedsWithFetch는
// 캐시 조회 실패 시 fetch를 차단하지 않고 정상 진행하는지 검증합니다.
func TestKafkaConsumerPool_URLCache_ExistsError_ProceedsWithFetch(t *testing.T) {
	consumer := new(mockConsumer)
	producer := new(mockProducer)
	handler := new(mockJobHandler)
	contentSvc := new(mockContentService)
	urlCache := new(mockURLCache)

	pool := worker.NewKafkaConsumerPoolWithOptions(
		consumer, producer, handler, contentSvc, 1,
		worker.NewCircuitBreakerRegistry(worker.DefaultCircuitBreakerConfig, nil),
		worker.NoopJobLocker{},
		urlCache,
	)

	job := newTestJob()
	content := newTestContent()
	msg := marshaledJobMsg(t, job)

	// 캐시 조회 실패 (Redis 장애)
	urlCache.On("Exists", mock.Anything, job.Target.URL).Return(false, errors.New("redis unavailable"))
	urlCache.On("Set", mock.Anything, job.Target.URL).Return(nil)

	handler.On("Handle", mock.Anything, job).Return([]*core.Content{content}, nil)
	contentSvc.On("Store", mock.Anything, content).Return(content.ID, false, nil)
	producer.On("Publish", mock.Anything, mock.MatchedBy(func(m queue.Message) bool {
		return m.Topic == queue.TopicNormalized
	})).Return(nil)
	consumer.On("CommitMessages", mock.Anything, mock.Anything).Return(nil)

	runPool(t, consumer, pool, msg)

	// 캐시 오류에도 handler는 호출되어야 합니다
	handler.AssertExpectations(t)
}

// TestKafkaConsumerPool_URLCache_CategoryPage_SkipsCache는
// 카테고리 페이지(TargetTypeCategory)에 대해 URL 캐시를 적용하지 않는지 검증합니다.
func TestKafkaConsumerPool_URLCache_CategoryPage_SkipsCache(t *testing.T) {
	consumer := new(mockConsumer)
	producer := new(mockProducer)
	handler := new(mockJobHandler)
	contentSvc := new(mockContentService)
	urlCache := new(mockURLCache)

	pool := worker.NewKafkaConsumerPoolWithOptions(
		consumer, producer, handler, contentSvc, 1,
		worker.NewCircuitBreakerRegistry(worker.DefaultCircuitBreakerConfig, nil),
		worker.NoopJobLocker{},
		urlCache,
	)

	job := &core.CrawlJob{
		ID:          "job-cat-001",
		CrawlerName: "test-crawler",
		Target:      core.Target{URL: "https://example.com/category/news", Type: core.TargetTypeCategory},
		Priority:    core.PriorityNormal,
		MaxRetries:  3,
	}
	msg := marshaledJobMsg(t, job)

	// 카테고리 페이지이므로 handler는 nil, nil 반환 (체이닝 없는 경우)
	handler.On("Handle", mock.Anything, job).Return(nil, nil)
	consumer.On("CommitMessages", mock.Anything, mock.Anything).Return(nil)

	runPool(t, consumer, pool, msg)

	// 카테고리 페이지에 대해서는 URL 캐시를 조회하지 않아야 합니다
	urlCache.AssertNotCalled(t, "Exists", mock.Anything, mock.Anything)
	// handler는 호출되어야 합니다
	handler.AssertExpectations(t)
}
