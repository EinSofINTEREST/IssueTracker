package queue

import "time"

// Kafka 토픽 상수 (아키텍처 문서 01-architecture.md 기준)
const (
	// 크롤 job 토픽 (우선순위별)
	TopicCrawlHigh   = "issuetracker.crawl.high"
	TopicCrawlNormal = "issuetracker.crawl.normal"
	TopicCrawlLow    = "issuetracker.crawl.low"

	// 원본 데이터 토픽 (국가별)
	TopicRawUS = "issuetracker.raw.us"
	TopicRawKR = "issuetracker.raw.kr"

	// 처리 파이프라인 토픽
	TopicNormalized = "issuetracker.normalized"
	TopicValidated  = "issuetracker.validated"
	TopicEnriched   = "issuetracker.enriched"
	TopicEmbedded   = "issuetracker.embedded"
	TopicClusters   = "issuetracker.clusters"

	// 시스템 토픽
	TopicDLQ = "issuetracker.dlq"
)

// Consumer group 상수
const (
	GroupCrawlerWorkers = "issuetracker-crawler-workers"
	GroupNormalizers    = "issuetracker-normalizers"
	GroupValidators     = "issuetracker-validators"
	GroupEnrichers      = "issuetracker-enrichers"
	GroupEmbedders      = "issuetracker-embedders"
	GroupClusterers     = "issuetracker-clusterers"
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
