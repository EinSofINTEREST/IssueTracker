# pkg/queue — Kafka Abstractions

소스: [`pkg/queue/`](../../../pkg/queue/)

Kafka producer / consumer 의 generic 인터페이스 + 모든 토픽 / 컨슈머 그룹 상수 + BacklogChecker 를
제공합니다. 구현체는 `github.com/segmentio/kafka-go` 사용.

<br>

## 인터페이스

[queue.go](../../../pkg/queue/queue.go):

```go
type Producer interface {
    Publish(ctx, msg Message) error
    PublishBatch(ctx, msgs []Message) error
    Close() error
}

type Consumer interface {
    FetchMessage(ctx) (*Message, error)             // auto-commit 안 함
    CommitMessages(ctx, msgs ...*Message) error     // 처리 성공 후 명시 commit
    Close() error
}

type Message struct {
    Topic     string
    Partition int
    Offset    int64
    Key       []byte
    Value     []byte
    Headers   map[string][]byte
    Time      time.Time
}
```

at-least-once 시맨틱 — `FetchMessage` 후 처리 → `CommitMessages` 호출 순서 (실패 시 재배달).

<br>

## 구현체

| 파일                                          | 역할                                                |
|----------------------------------------------|-----------------------------------------------------|
| [producer.go](../../../pkg/queue/producer.go) | `KafkaProducer` (segmentio Writer wrap)             |
| [consumer.go](../../../pkg/queue/consumer.go) | `KafkaConsumer` (segmentio Reader wrap, Manual commit) |
| [config.go](../../../pkg/queue/config.go)     | `Config` + `DefaultConfig()` + 모든 상수            |
| [backlog.go](../../../pkg/queue/backlog.go)   | `BacklogChecker` — consumer-group lag 측정 (이슈 #124) |

<br>

## 토픽 / 그룹 상수 (단일 소스)

[config.go](../../../pkg/queue/config.go):

```go
// Crawl jobs (priority)
TopicCrawlHigh   = "issuetracker.crawl.high"
TopicCrawlNormal = "issuetracker.crawl.normal"
TopicCrawlLow    = "issuetracker.crawl.low"

// Pipeline stages
TopicFetched     = "issuetracker.fetched"      // ← fetcher → parser (이슈 #134)
TopicNormalized  = "issuetracker.normalized"
TopicValidated   = "issuetracker.validated"
TopicEnriched    = "issuetracker.enriched"     // (계획)
TopicEmbedded    = "issuetracker.embedded"     // (계획)
TopicClusters    = "issuetracker.clusters"     // (계획)

// System
TopicDLQ         = "issuetracker.dlq"

// (legacy) country-별 raw — 현재는 TopicFetched 사용
TopicRawUS = "issuetracker.raw.us"
TopicRawKR = "issuetracker.raw.kr"

// Consumer groups
GroupCrawlerWorkers = "issuetracker-crawler-workers"
GroupParsers        = "issuetracker-parsers"   // 이슈 #134
GroupNormalizers    = "issuetracker-normalizers"
GroupValidators     = "issuetracker-validators"
GroupEnrichers      = "issuetracker-enrichers"
GroupEmbedders      = "issuetracker-embedders"
```

신규 토픽/그룹 추가 시 본 파일에만 추가하고 다른 곳에서 문자열 리터럴 직접 사용 금지.

<br>

## BacklogChecker

[backlog.go](../../../pkg/queue/backlog.go):

```go
type BacklogChecker interface {
    Backlog(ctx context.Context, topic, group string) (int64, error)
}

checker := queue.NewBacklogChecker(brokers, timeout)  // *KafkaBacklogChecker
lag, err := checker.Backlog(ctx, queue.TopicCrawlNormal, queue.GroupCrawlerWorkers)
```

내부에서 토픽의 partition 목록을 metadata 로 조회한 뒤 각 partition 의 latest offset 과 consumer-group
committed offset 의 차이를 합산합니다.

[`internal/scheduler.BacklogThrottler`](../internal/scheduler.md) 가 사용 — backlog 가 임계값 초과 시
seed publish 차단.

<br>

## 의존

- 외부: `github.com/segmentio/kafka-go`
- [`pkg/logger`](logger.md) (옵션)

<br>

## 호출 측

거의 모든 단계 worker — [`processor/fetcher/worker`](../internal/processor/fetcher/worker.md), [`processor/parser/worker`](../internal/processor/parser/README.md),
[`processor/validate`](../internal/processor/validate.md), [`scheduler`](../internal/scheduler.md), [`publisher`](../internal/publisher.md).

<br>

## Topic Configuration

토픽 partition / replication / retention 은 [`deployments/docker/`](../../../deployments/docker/) 의
kafka-init 컨테이너가 관리. 자세한 partitioning 전략은 [01-architecture.md](../../../.claude/rules/01-architecture.md)
참조.

<br>

## 관련 이슈

- 이슈 #124 — BacklogChecker 도입
- 이슈 #134 — TopicFetched + GroupParsers 신설
