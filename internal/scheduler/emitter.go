package scheduler

import (
	"context"
	"fmt"

	"issuetracker/internal/processor/fetcher/core"
	"issuetracker/pkg/logger"
	"issuetracker/pkg/queue"
)

// PipelineGuard 는 publish 진입 시 URL 의 pipeline membership 을 체크하는 인터페이스입니다 (이슈 #285).
//
// emitter 가 internal/locks 를 직접 import 하지 않도록 별도 정의 — 구조적 타이핑으로
// locks.PipelineGuard 가 그대로 만족.
type PipelineGuard interface {
	CheckAndAcquire(ctx context.Context, url string, targetType core.TargetType) (bool, error)
}

// JobEmitter는 Scheduler 전용 Emitter 구현체입니다.
// ScheduleEntry에 이미 결정된 Priority를 그대로 사용하여 Kafka crawl 토픽에 직접 발행합니다.
// 우선순위 재결정이 필요한 체이닝 발행은 internal/publisher 패키지를 사용하세요.
//
// PipelineGuard (이슈 #285): SetGuard 로 주입 시 Emit 직전에 CheckAndAcquire 호출 — 같은 URL 의
// cycle 이 진행 중이면 silent skip. Scheduler 의 정기 갱신 의도는 보존 (다음 주기 자연 진입).
// 미주입 (nil) 이면 가드 비활성 (기존 동작 유지).
type JobEmitter struct {
	producer queue.Producer
	guard    PipelineGuard
	log      *logger.Logger
}

// NewJobEmitter는 새 JobEmitter를 생성합니다.
func NewJobEmitter(producer queue.Producer, log *logger.Logger) *JobEmitter {
	return &JobEmitter{producer: producer, log: log}
}

// SetGuard 는 PipelineGuard 를 주입합니다 (이슈 #285).
// nil 주입 시 가드 비활성. Emit 호출 도중 변경하지 말 것 (race) — wiring 단계에서 1회.
func (e *JobEmitter) SetGuard(g PipelineGuard) { e.guard = g }

// Emit은 job.Priority에 따라 Kafka crawl 토픽에 CrawlJob을 발행합니다.
//
// PipelineGuard 가 주입되어 있고 같은 URL 의 cycle 이 진행 중이면 (acquired=false) silent skip
// (debug 로그 + nil 반환). guard 조회 실패는 fail-open (warn 로그 + publish 진행).
func (e *JobEmitter) Emit(ctx context.Context, job *core.CrawlJob) error {
	if e.guard != nil {
		acquired, gerr := e.guard.CheckAndAcquire(ctx, job.Target.URL, job.Target.Type)
		if gerr != nil {
			e.log.WithFields(map[string]interface{}{
				"job_id":  job.ID,
				"crawler": job.CrawlerName,
				"url":     job.Target.URL,
			}).WithError(gerr).Warn("pipeline guard check failed, allowing emit")
		} else if !acquired {
			e.log.WithFields(map[string]interface{}{
				"job_id":      job.ID,
				"crawler":     job.CrawlerName,
				"url":         job.Target.URL,
				"target_type": string(job.Target.Type),
			}).Debug("scheduler emit skipped — url already in pipeline")
			return nil
		}
	}

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
