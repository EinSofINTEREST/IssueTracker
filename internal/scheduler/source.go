// Package scheduler는 등록된 소스에 대해 주기적으로 CrawlJob을 생성하고
// Kafka crawl 토픽에 발행하는 스케줄러를 제공합니다.
//
// Package scheduler provides a periodic job scheduler that creates CrawlJobs
// from registered source entries and publishes them to Kafka crawl topics.
package scheduler

import (
	"time"

	"issuetracker/internal/processor/fetcher/core"
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

	// Timeout은 개별 크롤 Job의 최대 실행 시간입니다.
	Timeout time.Duration
}

// SeedPublisher interface 는 이슈 #396 으로 publisher 패키지로 이동.
// scheduler 는 publisher.SeedPublisher 를 import 하여 사용 — 메타 #385 의 단일 책임
// 원칙 (Kafka I/O 책임 인터페이스는 publisher 측 정의).
