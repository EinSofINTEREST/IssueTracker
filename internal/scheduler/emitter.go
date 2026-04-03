package scheduler

import (
	"context"
	"fmt"

	"issuetracker/internal/crawler/core"
	"issuetracker/pkg/logger"
	"issuetracker/pkg/queue"
)

// JobEmitter는 Scheduler 전용 Emitter 구현체입니다.
// ScheduleEntry에 이미 결정된 Priority를 그대로 사용하여 Kafka crawl 토픽에 직접 발행합니다.
// 우선순위 재결정이 필요한 체이닝 발행은 internal/publisher 패키지를 사용하세요.
type JobEmitter struct {
	producer queue.Producer
	log      *logger.Logger
}

// NewJobEmitter는 새 JobEmitter를 생성합니다.
func NewJobEmitter(producer queue.Producer, log *logger.Logger) *JobEmitter {
	return &JobEmitter{producer: producer, log: log}
}

// Emit은 job.Priority에 따라 Kafka crawl 토픽에 CrawlJob을 발행합니다.
func (e *JobEmitter) Emit(ctx context.Context, job *core.CrawlJob) error {
	data, err := job.Marshal()
	if err != nil {
		return fmt.Errorf("marshal job %s: %w", job.ID, err)
	}

	topic := crawlTopic(job.Priority)

	msg := queue.Message{
		Topic: topic,
		Key:   []byte(job.ID),
		Value: data,
		Headers: map[string]string{
			"crawler":  job.CrawlerName,
			"priority": fmt.Sprintf("%d", int(job.Priority)),
		},
	}

	if err := e.producer.Publish(ctx, msg); err != nil {
		return fmt.Errorf("emit job %s to %s: %w", job.ID, topic, err)
	}

	e.log.WithFields(map[string]interface{}{
		"job_id":   job.ID,
		"crawler":  job.CrawlerName,
		"url":      job.Target.URL,
		"priority": int(job.Priority),
		"topic":    topic,
	}).Debug("seed job emitted to kafka")

	return nil
}

// crawlTopic은 Priority에 대응하는 Kafka crawl 토픽 이름을 반환합니다.
func crawlTopic(p core.Priority) string {
	switch p {
	case core.PriorityHigh:
		return queue.TopicCrawlHigh
	case core.PriorityLow:
		return queue.TopicCrawlLow
	default:
		return queue.TopicCrawlNormal
	}
}
