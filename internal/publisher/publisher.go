// Package publisher는 크롤러가 페이지에서 발견한 URL을 다음 CrawlJob으로 연결하는
// 체이닝 발행 컴포넌트를 제공합니다.
//
// 역할 분리:
//   - Scheduler  : 등록된 소스의 시드 Job만 생성 (internal/scheduler)
//   - Publisher  : 크롤 결과에서 발견된 URL을 다음 Job으로 연결 (이 패키지)
package publisher

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"issuetracker/internal/crawler/core"
	"issuetracker/pkg/logger"
	"issuetracker/pkg/queue"
)

// PriorityResolver는 CrawlJob의 우선순위를 결정하는 인터페이스입니다.
// worker.CompositeResolver 등이 이를 구현합니다.
type PriorityResolver interface {
	Resolve(job *core.CrawlJob) core.Priority
}

// Publisher는 크롤된 페이지에서 발견된 URL을 새 CrawlJob으로 변환하여
// 우선순위에 맞는 Kafka crawl 토픽에 발행합니다.
type Publisher struct {
	producer queue.Producer
	resolver PriorityResolver
	log      *logger.Logger
}

// New는 새 Publisher를 생성합니다.
func New(producer queue.Producer, resolver PriorityResolver, log *logger.Logger) *Publisher {
	return &Publisher{
		producer: producer,
		resolver: resolver,
		log:      log,
	}
}

// Publish는 발견된 URL 목록으로 CrawlJob을 생성하고 한 번의 배치 요청으로 Kafka에 발행합니다.
// 단건 순차 호출 대신 PublishBatch를 사용하여 Kafka 왕복을 1회로 줄입니다.
func (p *Publisher) Publish(
	ctx context.Context,
	crawlerName string,
	urls []string,
	targetType core.TargetType,
	timeout time.Duration,
) error {
	if len(urls) == 0 {
		return nil
	}

	msgs := make([]queue.Message, 0, len(urls))

	for _, url := range urls {
		job := &core.CrawlJob{
			ID:          newJobID(),
			CrawlerName: crawlerName,
			Target: core.Target{
				URL:  url,
				Type: targetType,
			},
			ScheduledAt: time.Now(),
			Timeout:     timeout,
			MaxRetries:  3,
		}

		job.Priority = p.resolver.Resolve(job)

		msg, err := p.buildMessage(job)
		if err != nil {
			return fmt.Errorf("build message for %s: %w", url, err)
		}

		msgs = append(msgs, msg)
	}

	if err := p.producer.PublishBatch(ctx, msgs); err != nil {
		return fmt.Errorf("batch publish %d jobs for crawler %s: %w", len(msgs), crawlerName, err)
	}

	p.log.WithFields(map[string]interface{}{
		"crawler":   crawlerName,
		"job_count": len(msgs),
	}).Debug("chained jobs batch published to kafka")

	return nil
}

// buildMessage는 CrawlJob을 Kafka Message로 변환합니다.
func (p *Publisher) buildMessage(job *core.CrawlJob) (queue.Message, error) {
	data, err := job.Marshal()
	if err != nil {
		return queue.Message{}, fmt.Errorf("marshal job %s: %w", job.ID, err)
	}

	return queue.Message{
		Topic: crawlTopic(job.Priority),
		Key:   []byte(job.ID),
		Value: data,
		Headers: map[string]string{
			"crawler":  job.CrawlerName,
			"priority": fmt.Sprintf("%d", int(job.Priority)),
		},
	}, nil
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

// newJobID는 crypto/rand 기반의 고유 Job ID(32자 hex)를 생성합니다.
func newJobID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
