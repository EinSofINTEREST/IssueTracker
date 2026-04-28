package worker_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/crawler/core"
	"issuetracker/internal/crawler/worker"
	"issuetracker/pkg/queue"
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
