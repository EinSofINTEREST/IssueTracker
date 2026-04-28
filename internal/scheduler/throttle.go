package scheduler

import (
	"context"
	"time"

	"issuetracker/internal/crawler/core"
	"issuetracker/pkg/logger"
	"issuetracker/pkg/queue"
)

// Throttler 는 publish 직전에 호출되어 throttle 여부를 결정합니다.
// true 반환 시 Scheduler 는 emit 호출 없이 silent drop 합니다.
//
// 의도적으로 매우 작은 인터페이스 — Scheduler 는 단지 "이 job 을 보내도 되는가?" 만
// 알고 싶어하며, 구체적인 임계값/조회 방법은 구현체가 책임집니다.
type Throttler interface {
	ShouldThrottle(ctx context.Context, job *core.CrawlJob) bool
}

// BacklogThrottler 는 Kafka crawl 토픽의 backlog (consumer-group lag) 가 임계값을
// 초과하면 publish 를 차단하는 Throttler 구현체입니다.
//
// 동작:
//   - job.Priority 에 대응하는 crawl 토픽의 lag 를 BacklogChecker 로 조회
//   - lag > maxBacklog 시 true (throttle) + WARN 로그
//   - lag 조회 실패 시 fail-open (false 반환) — 일시적 Kafka 장애가 영구 차단으로
//     이어지지 않도록. 다음 tick 에서 자동 재시도되며, 실패 사유는 WARN 으로 남김
//
// maxBacklog <= 0 이면 항상 false (disabled). main 에서 환경변수 0 일 때 굳이
// throttler 를 생성하지 않더라도, 안전하게 무시되는 동작을 보장.
type BacklogThrottler struct {
	checker    queue.BacklogChecker
	groupID    string
	maxBacklog int64
	timeout    time.Duration
	log        *logger.Logger
}

// NewBacklogThrottler 는 BacklogThrottler 를 생성합니다.
//
// 인자:
//   - checker    : lag 조회기 (보통 queue.NewBacklogChecker 결과)
//   - groupID    : crawl 토픽을 소비하는 consumer group (queue.GroupCrawlerWorkers)
//   - maxBacklog : 차단 임계값. 0 이하면 throttle 비활성
//   - timeout    : 단일 lag 조회 RPC 의 deadline (0 이면 ctx 만 사용)
//   - log        : 차단/조회실패 WARN 로그를 남길 logger
func NewBacklogThrottler(
	checker queue.BacklogChecker,
	groupID string,
	maxBacklog int64,
	timeout time.Duration,
	log *logger.Logger,
) *BacklogThrottler {
	return &BacklogThrottler{
		checker:    checker,
		groupID:    groupID,
		maxBacklog: maxBacklog,
		timeout:    timeout,
		log:        log,
	}
}

// ShouldThrottle 은 job.Priority 에 대응하는 crawl 토픽의 backlog 를 조회하여
// 임계값 초과 시 true 를 반환합니다. Throttler 인터페이스 구현.
func (t *BacklogThrottler) ShouldThrottle(ctx context.Context, job *core.CrawlJob) bool {
	if t.maxBacklog <= 0 {
		return false
	}

	topic := crawlTopic(job.Priority)

	checkCtx := ctx
	if t.timeout > 0 {
		var cancel context.CancelFunc
		checkCtx, cancel = context.WithTimeout(ctx, t.timeout)
		defer cancel()
	}

	backlog, err := t.checker.Backlog(checkCtx, topic, t.groupID)
	if err != nil {
		// fail-open: 일시적 조회 실패가 publish 를 영구 차단하지 않도록.
		// 다음 tick 에서 재시도되며, 모니터링은 WARN 누적으로 감지 가능.
		t.log.WithFields(map[string]interface{}{
			"topic":   topic,
			"group":   t.groupID,
			"crawler": job.CrawlerName,
			"url":     job.Target.URL,
		}).WithError(err).Warn("backlog check failed, allowing publish")
		return false
	}

	if backlog > t.maxBacklog {
		t.log.WithFields(map[string]interface{}{
			"topic":       topic,
			"group":       t.groupID,
			"backlog":     backlog,
			"max_backlog": t.maxBacklog,
			"crawler":     job.CrawlerName,
			"url":         job.Target.URL,
		}).Warn("kafka backlog exceeds threshold, throttling publish")
		return true
	}

	return false
}
