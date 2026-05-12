// Package scheduler는 등록된 소스에 대해 주기적으로 CrawlJob을 생성하고
// Kafka crawl 토픽에 발행하는 스케줄러를 제공합니다.
//
// Package scheduler provides a periodic job scheduler that creates CrawlJobs
// from registered source entries and publishes them to Kafka crawl topics.
package scheduler

import (
	"context"
	"time"

	"issuetracker/internal/processor/fetcher/core"
	"issuetracker/pkg/categories"
)

// ScheduleEntry는 주기적으로 발행할 크롤 Job의 스케줄 항목입니다.
//
// ScheduleEntry describes a single URL to be crawled on a fixed interval.
type ScheduleEntry struct {
	// CrawlerName은 이 Job을 처리할 크롤러 이름입니다 (registry 키 — host 기반).
	CrawlerName string

	// URL은 크롤링 대상 URL입니다.
	URL string

	// TargetType은 URL의 타입입니다 (feed / category / sitemap / article).
	TargetType core.TargetType

	// Interval은 Job 발행 주기입니다.
	Interval time.Duration

	// Priority는 crawl 토픽 우선순위입니다.
	Priority core.Priority

	// Category 는 본 entry 의 콘텐츠 도메인 분류입니다 (이슈 #381 — pkg/categories).
	// scheduler 가 CrawlJob 생성 시 Target.Metadata["category"] 로 주입 → 다운스트림
	// CategoryBasedResolver 가 priority 결정에 사용. 빈 값이면 미분류 (Low fallback).
	//
	// 본 필드는 entry 자체의 분류 — chained job (category page → article) 의 category
	// 상속은 별도 follow-up (publisher 측 작업).
	Category categories.Category

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
