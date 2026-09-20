package queue

import "time"

// Kafka 토픽 상수 (아키텍처 문서 01-architecture.md 기준)
const (
	// 크롤 job 토픽 (우선순위별)
	TopicCrawlHigh   = "issuetracker.crawl.high"
	TopicCrawlNormal = "issuetracker.crawl.normal"
	TopicCrawlLow    = "issuetracker.crawl.low"

	// TopicCrawlChromedp: chromedp 전용 fetch 큐.
	// goquery worker 가 lazy-load 감지 / fetch 실패 시 재발행하거나, 단계 3 의 자동 upgrade
	// trigger / force_fetcher metadata path 가 직접 enqueue. 별도 consumer group
	// (GroupChromedpFetchers) 의 chromedp worker 가 consume 하여 worker 단위 semaphore 로
	// Chrome 인스턴스 동시 호출 수를 제어. 우선순위 분리는 본 sub scope 외 (단일 토픽).
	TopicCrawlChromedp = "issuetracker.crawl.chromedp"

	// Deprecated: 국가별 raw 토픽은 초기 설계안이며 실제 파이프라인은 아래 TopicFetched 단일
	// 토픽을 씁니다. 저장소 내부 사용처는 0 이지만 pkg/ 는 internal 경계 밖이라 삭제가 외부
	// consumer 의 소스 호환성을 깨므로 alias 로 남깁니다 (이슈 #544 리뷰). 신규 코드에서 사용 금지.
	TopicRawUS = "issuetracker.raw.us"
	// Deprecated: TopicRawUS 주석 참조.
	TopicRawKR = "issuetracker.raw.kr"

	// TopicFetched: fetcher worker 가 RawContent 저장 후 RawContentRef 발행하는 토픽.
	// payload 는 raw_id + url + source_info 만 포함 (HTML 본문 미포함, < 1KB).
	// parser worker (GroupParsers) 가 consume 하여 raw_contents 에서 본문 로드 + 파싱.
	TopicFetched = "issuetracker.fetched"

	// 처리 파이프라인 토픽

	// TopicNormalized: **이름과 실제 역할이 다르다.** 별도의 normalize stage 는 없으며,
	// fetcher / parser 가 파싱을 마친 Content 를 발행하고 validate worker 가 consume 한다.
	// (parser → validate 구간). 이름은 초기 설계의 잔재이고, 운영 중 토픽이라 개명하지 않는다.
	TopicNormalized = "issuetracker.normalized"
	TopicValidated  = "issuetracker.validated"

	// TopicEnriched: 현재 파이프라인의 **종단** 이다 — enrich worker 가 발행하지만 consumer 가
	// 없다. 임베딩 / 클러스터링 도입 시 소비처가 생긴다 (이슈 #17 / #18 / #20).
	TopicEnriched = "issuetracker.enriched"

	// planned — 임베딩(이슈 #17) / 클러스터링(이슈 #18) / Vector DB(이슈 #20) 도입 시 사용.
	// 현재 발행·소비 코드 모두 없다. 상수만 남겨 토픽 이름 규약을 고정한다.
	TopicEmbedded = "issuetracker.embedded"
	TopicClusters = "issuetracker.clusters"

	// 시스템 토픽
	TopicDLQ = "issuetracker.dlq"
)

// Consumer group 상수
const (
	GroupCrawlerWorkers = "issuetracker-crawler-workers"
	// GroupChromedpFetchers: TopicCrawlChromedp 를 consume 하여 chromedp 만 사용하는 fetcher worker
	// pool 의 consumer group. 일반 crawler worker (GroupCrawlerWorkers) 와 분리되어
	// Chrome 자원과 1:1 매핑된 worker 수 + semaphore 로 동시 호출량 제한.
	GroupChromedpFetchers = "issuetracker-chromedp-fetchers"
	// GroupParsers: TopicFetched 를 consume 하여 raw 로드 + 파싱 + content 저장 + raw 삭제.
	GroupParsers    = "issuetracker-parsers"
	GroupValidators = "issuetracker-validators"
	GroupEnrichers  = "issuetracker-enrichers"

	// planned — 해당 stage 미구현. GroupNormalizers 는 별도 normalize stage 가 존재하지 않아
	// (TopicNormalized 주석 참조) 도입 계획도 없다.
	GroupNormalizers = "issuetracker-normalizers"
	GroupEmbedders   = "issuetracker-embedders"
	GroupClusterers  = "issuetracker-clusterers"
)

// Config는 Kafka 연결 설정을 나타냅니다.
//
// Config holds the configuration for Kafka producer and consumer connections.
type Config struct {
	Brokers      []string
	GroupID      string
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
	MaxRetries   int
	MinBytes     int
	MaxBytes     int
}

// DefaultConfig는 단일 로컬 브로커 기준 기본 Kafka 설정을 반환합니다.
//
// DefaultConfig returns a default Config targeting a local Kafka broker.
// Override Brokers and GroupID before using in production.
func DefaultConfig() Config {
	return Config{
		Brokers:      []string{"localhost:9092"},
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		MaxRetries:   3,
		MinBytes:     10e3, // 10KB
		MaxBytes:     10e6, // 10MB
	}
}
