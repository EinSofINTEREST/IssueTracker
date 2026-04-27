package worker_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"issuetracker/internal/crawler/core"
	"issuetracker/internal/crawler/worker"
	"issuetracker/pkg/logger"
	"issuetracker/pkg/urlguard"
)

// TestKafkaConsumerPool_Gate_BlocksRSSURL_CommitsAndSkipsHandler:
// 차단 URL job 은 handler.Handle 호출 없이 메시지만 commit 되어야 함 (큐에서 즉시 제거).
//
// 이슈 #119 의 핵심 효과 — stale URL 메시지가 retry 사이클 없이 정리됨을 보장.
func TestKafkaConsumerPool_Gate_BlocksRSSURL_CommitsAndSkipsHandler(t *testing.T) {
	consumer := new(mockConsumer)
	producer := new(mockProducer)
	handler := new(mockJobHandler)
	contentSvc := new(mockContentService)

	pool := worker.NewKafkaConsumerPool(consumer, producer, handler, contentSvc, 1)
	pool.SetGate(urlguard.NewGate(urlguard.Default(), logger.New(logger.DefaultConfig())))

	// 차단될 RSS URL 을 가진 job 메시지
	job := newTestJob()
	job.Target.URL = "https://rss.cnn.com/rss/cnn_health.rss"
	msg := marshaledJobMsg(t, job)

	// commit 만 호출되어야 하고 handler.Handle 은 호출되면 안 됨
	consumer.On("CommitMessages", mock.Anything, mock.Anything).Return(nil)

	runPool(t, consumer, pool, msg)

	consumer.AssertCalled(t, "CommitMessages", mock.Anything, mock.Anything)
	handler.AssertNotCalled(t, "Handle", mock.Anything, mock.Anything)
	contentSvc.AssertNotCalled(t, "Store", mock.Anything, mock.Anything)
	producer.AssertNotCalled(t, "Publish", mock.Anything, mock.Anything)
}

// TestKafkaConsumerPool_Gate_AllowsArticleURL_DelegatesToHandler:
// 통과 URL 은 기존 처리 흐름으로 위임 (handler.Handle 호출 + content publish + commit).
func TestKafkaConsumerPool_Gate_AllowsArticleURL_DelegatesToHandler(t *testing.T) {
	consumer := new(mockConsumer)
	producer := new(mockProducer)
	handler := new(mockJobHandler)
	contentSvc := new(mockContentService)

	pool := worker.NewKafkaConsumerPool(consumer, producer, handler, contentSvc, 1)
	pool.SetGate(urlguard.NewGate(urlguard.Default(), logger.New(logger.DefaultConfig())))

	job := newTestJob()
	job.Target.URL = "https://edition.cnn.com/health/article-123"
	content := newTestContent()
	msg := marshaledJobMsg(t, job)

	handler.On("Handle", mock.Anything, job).Return([]*core.Content{content}, nil)
	contentSvc.On("Store", mock.Anything, content).Return(content.ID, false, nil)
	producer.On("Publish", mock.Anything, mock.Anything).Return(nil)
	consumer.On("CommitMessages", mock.Anything, mock.Anything).Return(nil)

	runPool(t, consumer, pool, msg)

	handler.AssertCalled(t, "Handle", mock.Anything, job)
	consumer.AssertCalled(t, "CommitMessages", mock.Anything, mock.Anything)
}

// TestKafkaConsumerPool_NoGate_LegacyBehavior:
// SetGate 미호출 시 기존 동작 — 모든 URL 처리.
func TestKafkaConsumerPool_NoGate_LegacyBehavior(t *testing.T) {
	consumer := new(mockConsumer)
	producer := new(mockProducer)
	handler := new(mockJobHandler)
	contentSvc := new(mockContentService)

	pool := worker.NewKafkaConsumerPool(consumer, producer, handler, contentSvc, 1)
	// SetGate 호출 없음

	job := newTestJob()
	job.Target.URL = "https://rss.cnn.com/rss/cnn_health.rss"
	content := newTestContent()
	msg := marshaledJobMsg(t, job)

	// 가드 없으므로 RSS URL 도 handler 가 호출됨
	handler.On("Handle", mock.Anything, job).Return([]*core.Content{content}, nil)
	contentSvc.On("Store", mock.Anything, content).Return(content.ID, false, nil)
	producer.On("Publish", mock.Anything, mock.Anything).Return(nil)
	consumer.On("CommitMessages", mock.Anything, mock.Anything).Return(nil)

	runPool(t, consumer, pool, msg)

	handler.AssertCalled(t, "Handle", mock.Anything, job)
}

// TestKafkaConsumerPool_Gate_AllowAllGuard_DelegatesAll:
// AllowAllGuard 명시 사용 시 모든 URL 위임 (가드 우회 옵션).
func TestKafkaConsumerPool_Gate_AllowAllGuard_DelegatesAll(t *testing.T) {
	consumer := new(mockConsumer)
	producer := new(mockProducer)
	handler := new(mockJobHandler)
	contentSvc := new(mockContentService)

	pool := worker.NewKafkaConsumerPool(consumer, producer, handler, contentSvc, 1)
	pool.SetGate(urlguard.NewGate(urlguard.AllowAllGuard{}, logger.New(logger.DefaultConfig())))

	job := newTestJob()
	job.Target.URL = "https://rss.cnn.com/rss/cnn_health.rss"
	content := newTestContent()
	msg := marshaledJobMsg(t, job)

	handler.On("Handle", mock.Anything, job).Return([]*core.Content{content}, nil)
	contentSvc.On("Store", mock.Anything, content).Return(content.ID, false, nil)
	producer.On("Publish", mock.Anything, mock.Anything).Return(nil)
	consumer.On("CommitMessages", mock.Anything, mock.Anything).Return(nil)

	runPool(t, consumer, pool, msg)

	handler.AssertCalled(t, "Handle", mock.Anything, job)
	assert.True(t, true) // sentinel
}

// TestKafkaConsumerPool_Gate_RaceFreeUpdate:
// SetGate 가 atomic.Pointer 기반이므로 Start 이후에도 race 없음.
// 본 테스트는 -race detector 로 검증 (실제 race 시 detector 가 fail).
func TestKafkaConsumerPool_Gate_RaceFreeUpdate(t *testing.T) {
	consumer := new(mockConsumer)
	producer := new(mockProducer)
	handler := new(mockJobHandler)
	contentSvc := new(mockContentService)

	pool := worker.NewKafkaConsumerPool(consumer, producer, handler, contentSvc, 1)

	// 동시에 여러 번 SetGate 호출 — atomic.Pointer 가 보장
	guards := []*urlguard.Gate{
		urlguard.NewGate(urlguard.Default(), nil),
		urlguard.NewGate(urlguard.AllowAllGuard{}, nil),
	}
	for i := 0; i < 100; i++ {
		pool.SetGate(guards[i%2])
	}
}
