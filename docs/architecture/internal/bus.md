# internal/bus — Kafka I/O 단일 출처

소스: [`internal/bus/`](../../../internal/bus/)

파이프라인의 **모든 Kafka 발행을 한 곳으로 모은** 컴포넌트 (이슈 #385).
크롤된 페이지에서 발견된 URL 을 다음 CrawlJob 으로 발행하는 것이 주 책임이며,
seed / upgrade / retry / DLQ 발행 경로도 함께 소유한다.
[scheduler](scheduler.md) 가 등록된 시드만 다루는 것과 책임 분리.

> 본 패키지는 이전에 `internal/publisher` 였다. 이슈 #385 에서 Kafka I/O 를 단일
> 출처로 모으면서 `internal/bus` 로 통합됐다.

<br>

## 구성

| 파일 | 역할 |
|---|---|
| `worker.go` | `Publisher` 정의 + `New` + `Forward` / `PublishJob` + DLQ 계측 |
| `chain.go` | `PublishChained` — 발견 링크를 chained job 으로 발행 |
| `seed.go` | `PublishSeed` — scheduler 시드 발행 |
| `upgrade.go` | `PublishUpgrade` — chromedp 승급 재발행 |
| `retry.go` | `RetryScheduler` — 지연 재큐잉 (헤더 복원 포함) |
| `guard.go` | `IngestionMarker` / `PipelineGuard` 인터페이스 + optional setter |
| `resolver.go` | `PriorityResolver` — priority → topic 라우팅 |
| `priority_refresher.go` | priority 규칙 갱신 |
| `dlq_metrics.go` | `dlq_published_total{origin}` (이슈 #543) |

<br>

## 책임

- URL 정규화 ([`pkg/links.Normalizer`](../pkg/links.md))
- 파이프라인 진입 marker atomic dedup ([`locks.IngestionMarker`](locks/README.md))
- URL Guard 검사 ([`pkg/urlguard.Gate`](../pkg/urlguard.md))
- Priority 결정 (`PriorityResolver` — 본 패키지 [`resolver.go`](../../../internal/bus/resolver.go))
- 토픽 라우팅 (Priority → TopicCrawlHigh/Normal/Low)
- DLQ 발행 및 계측

<br>

## 인터페이스

```go
type Publisher struct { … }
func New(producer queue.Producer, resolver PriorityResolver, log *logger.Logger) *Publisher

// Optional dependencies (atomic pointer — nil 허용, lazy injection)
(*Publisher) SetNormalizer(*links.Normalizer)
(*Publisher) SetIngestionMarker(IngestionMarker)
(*Publisher) SetPipelineGuard(PipelineGuard)
(*Publisher) SetGate(*urlguard.Gate)
(*Publisher) SetDLQMetrics(*DLQMetrics)

// 발행 경로
(*Publisher) PublishChained(ctx, …) error   // 발견 링크
(*Publisher) PublishSeed(ctx, *core.CrawlJob) error
(*Publisher) PublishJob(ctx, *core.CrawlJob) error
(*Publisher) PublishUpgrade(ctx, host string, msgs []queue.Message) error
(*Publisher) Forward(ctx, Message) error    // 완성된 Message thin pass-through
```

`Forward` 는 호출자가 구성한 Message 를 그대로 발행한다 — worker 특수 메시지
(normalized contentRef / DLQ) 의 구성은 worker 에 남기되 Kafka I/O 만 위임하는 경로 (이슈 #390).

<br>

## PublishChained 흐름

```
for each job in batch:
   1. Normalizer.Normalize(job.Target.URL)            ← Set 됐으면
   2. Gate.Allow(url) → 차단 시 silent drop + WARN    ← Set 됐으면
   3. 진입 marker 획득 — 아래 둘 중 하나 (PipelineGuard 우선)
        ├ PipelineGuard 주입됨 → guard.CheckAndAcquire(url, targetType)
        │    target type 별 TTL 정책 (Article 24h / Category 단명).
        │    Category 도 대상에 포함된다.
        └ guard 미주입 + targetType != Category → IngestionMarker.Acquire(url)
             backward compat fallback. **Category 는 이 경로를 우회** 해
             marker 없이 통과한다.
        결과: already_marked → skip / acquired → continue
   4. PriorityResolver.Resolve(job) → priority
   5. queue.Producer.Publish(topic[priority], jobMsg)
```

각 dependency 는 **nil 허용** — 미설정 시 해당 단계만 skip 하고 통과 (graceful degrade).

<br>

## 호출 측

- [`internal/processor/parser/worker.ParserWorker`](processor/parser/README.md) — TargetTypeList 처리 시 발견 링크 발행
- [`internal/processor/fetcher/worker`](processor/fetcher/worker.md) — DLQ / normalized 발행을 `Forward` 로 위임
- [`internal/scheduler.Scheduler`](scheduler.md) — `PublishSeed` 로 시드 발행

<br>

## 의존

- [`internal/processor/fetcher/core`](processor/fetcher/core.md) — `CrawlJob`
- [`internal/locks`](locks/README.md) — `IngestionMarker`
- [`pkg/queue`](../pkg/queue.md), [`pkg/links`](../pkg/links.md), [`pkg/urlguard`](../pkg/urlguard.md), [`pkg/logger`](../pkg/logger.md)

<br>

## Wiring 위치

[`cmd/issuetracker/main.go`](../../../cmd/issuetracker/main.go):
```go
jobPublisher := bus.New(crawlerProducer, resolver, log)
jobPublisher.SetNormalizer(links.NewNormalizer())
jobPublisher.SetIngestionMarker(ingestionMarker)  // Redis 가 살아있을 때만
jobPublisher.SetDLQMetrics(dlqMetrics)
// jobPublisher.SetGate(...)  ← URL Guard wiring 위치
```

<br>

## 관련 이슈

- 이슈 #126 — Publisher 단일 책임화
- 이슈 #178 — 진입 marker 단일 책임화
- 이슈 #119 — URL Guard
- 이슈 #385 — Kafka I/O 단일 출처로 `internal/bus` 통합
- 이슈 #390 — `Forward` pass-through 도입
- 이슈 #541 — `IngestionLock` → `IngestionMarker` 개명
- 이슈 #543 — DLQ 발행 계측
