# internal/publisher — Chained Job Publisher

소스: [`internal/publisher/publisher.go`](../../../internal/publisher/publisher.go)

크롤된 페이지에서 발견된 URL 들을 **다음 CrawlJob** 으로 변환해 Kafka 에 발행하는 컴포넌트.
[scheduler](scheduler.md) 가 등록된 시드만 다루는 것과 책임 분리.

<br>

## 책임

- URL 정규화 ([`pkg/links.Normalizer`](../pkg/links.md))
- IngestionLock atomic dedup ([`locks.IngestionLock`](locks/README.md))
- URL Guard 검사 ([`pkg/urlguard.Gate`](../pkg/urlguard.md))
- Priority 결정 ([`processor/fetcher/worker.PriorityResolver`](processor/fetcher/worker.md))
- 토픽 라우팅 (Priority → TopicCrawlHigh/Normal/Low)

<br>

## 인터페이스

```go
type Publisher struct { … }
func New(producer queue.Producer, resolver PriorityResolver, log *logger.Logger) *Publisher

// Optional dependencies (lazy injection 으로 nil 허용)
(*Publisher) SetNormalizer(*links.Normalizer)
(*Publisher) SetIngestionLock(IngestionLock)
(*Publisher) SetGate(*urlguard.Gate)

// 사용 측이 호출
(*Publisher) PublishBatch(ctx context.Context, jobs []core.CrawlJob) error
```

<br>

## PublishBatch 흐름

```
for each job in batch:
   1. Normalizer.Normalize(job.Target.URL)            ← Set 됐으면
   2. Gate.Allow(url) → 차단 시 silent drop + WARN    ← Set 됐으면
   3. IngestionLock.Acquire(url)                      ← Set 됐으면
        ├ already_locked → skip
        └ acquired → continue
   4. PriorityResolver.Resolve(job) → priority
   5. queue.Producer.Publish(topic[priority], jobMsg)
```

각 dependency 는 **nil 허용** — 미설정 시 해당 단계만 skip 하고 통과 (graceful degrade).

<br>

## 호출 측

- [`internal/parser/worker.ParserWorker`](parser/README.md) — TargetTypeList 처리 시 발견 링크 발행
- [`internal/scheduler.Scheduler`](scheduler.md) 는 **미사용** — Scheduler 는 직접 `Emitter.Emit` 호출

<br>

## 의존

- [`internal/processor/fetcher/core`](processor/fetcher/core.md) — `CrawlJob`
- [`internal/processor/fetcher/worker`](processor/fetcher/worker.md) — `PriorityResolver`
- [`internal/locks`](locks/README.md) — `IngestionLock`
- [`pkg/queue`](../pkg/queue.md), [`pkg/links`](../pkg/links.md), [`pkg/urlguard`](../pkg/urlguard.md), [`pkg/logger`](../pkg/logger.md)

<br>

## Wiring 위치

[`cmd/issuetracker/main.go`](../../../cmd/issuetracker/main.go):
```go
jobPublisher := publisher.New(crawlerProducer, resolver, log)
jobPublisher.SetNormalizer(links.NewNormalizer())
jobPublisher.SetIngestionLock(ingestionLock)  // Redis 가 살아있을 때만
// jobPublisher.SetGate(...)  ← URL Guard wiring 위치
```

<br>

## 관련 이슈

- 이슈 #126 — Publisher 단일 책임화
- 이슈 #178 — IngestionLock 단일 책임화
- 이슈 #119 — URL Guard
