// Package scheduler는 등록된 소스에 대해 주기적으로 CrawlJob을 생성하고
// Kafka crawl 토픽에 발행하는 스케줄러를 제공합니다.
//
// Package scheduler provides a periodic job scheduler that creates CrawlJobs
// from registered source entries and publishes them to Kafka crawl topics.
package scheduler

import (
	"context"
	"time"

	"issuetracker/internal/crawler/core"
)

// ScheduleEntry는 주기적으로 발행할 크롤 Job의 스케줄 항목입니다.
//
// ScheduleEntry describes a single URL to be crawled on a fixed interval.
type ScheduleEntry struct {
	// CrawlerName은 이 Job을 처리할 크롤러 이름입니다 (registry 키).
	CrawlerName string

	// URL은 크롤링 대상 URL입니다.
	URL string

	// TargetType은 URL의 타입입니다 (feed / category / sitemap / article).
	TargetType core.TargetType

	// Interval은 Job 발행 주기입니다.
	Interval time.Duration

	// Priority는 crawl 토픽 우선순위입니다.
	Priority core.Priority

	// Timeout은 개별 크롤 Job의 최대 실행 시간입니다.
	Timeout time.Duration
}

// Emitter는 Scheduler가 생성한 CrawlJob을 Kafka crawl 토픽에 발행하는 인터페이스입니다.
// Scheduler의 역할은 시드 Job 생성에 한정되며, 라우팅은 Emitter 구현체가 담당합니다.
//
// Emitter routes initial seed CrawlJobs to the appropriate Kafka crawl topic.
// Scheduler is only responsible for creating jobs; Emitter handles delivery.
type Emitter interface {
	Emit(ctx context.Context, job *core.CrawlJob) error
}
