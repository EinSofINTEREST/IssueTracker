# internal/crawler/worker — Pool Manager + Distributed Coordination

소스: [`internal/crawler/worker/`](../../../../internal/crawler/worker/)

크롤러 단계의 핵심 오케스트레이터. **3-tier priority Kafka consumer pool** 을 운영하며, **Redis 기반
ProcessingLock / IngestionLock / RetryScheduler** 로 다중 인스턴스 환경의 중복 처리/재시도를 안전하게
처리합니다. 추가로 **per-source CircuitBreaker** 로 실패 폭주를 차단합니다.

<br>

## 핵심 타입

| 타입                          | 위치                                                                   | 책임                                                            |
|------------------------------|-----------------------------------------------------------------------|-----------------------------------------------------------------|
| `PoolManager`                 | [manager.go](../../../../internal/crawler/worker/manager.go)           | 3개 priority pool 의 lifecycle 관리                              |
| `KafkaConsumerPool`           | [pool.go](../../../../internal/crawler/worker/pool.go)                 | 단일 priority 의 worker goroutine pool + retry 라우팅           |
| `ProcessingLock` (interface)  | [processing_lock.go](../../../../internal/crawler/worker/processing_lock.go) | URL 동시 처리 방지 — Redis SETNX / Noop 두 구현                |
| `IngestionLock` (interface)   | [ingestion_lock.go](../../../../internal/crawler/worker/ingestion_lock.go)   | URL pipeline 진입 marker — Publisher 가 사용                  |
| `RetryScheduler` (interface)  | [retry_scheduler.go](../../../../internal/crawler/worker/retry_scheduler.go) | 지연 retry — Redis ZSET 또는 즉시 republish                  |
| `CircuitBreakerRegistry`      | [circuit_breaker.go](../../../../internal/crawler/worker/circuit_breaker.go) | per-source 실패율 트래킹 + 차단                              |
| `PriorityResolver` (interface) | [resolver.go](../../../../internal/crawler/worker/resolver.go)         | retry/escalation 시 새 priority 결정 전략                       |

<br>

## 3-Tier Pool 구조

```
            ┌─────────────────────────────────┐
            │         PoolManager             │
            └────┬────────────┬──────────┬────┘
                 │            │          │
   ┌─────────────▼─┐  ┌───────▼───┐  ┌───▼─────────┐
   │ HighPool (3)  │  │ NormalPool│  │ LowPool (2) │
   │ TopicCrawlHigh│  │     (6)   │  │TopicCrawlLow│
   └───────────────┘  │TopicCrawl-│  └─────────────┘
                      │   Normal  │
                      └───────────┘

각 pool 의 worker goroutine 이 수행:
  1. Kafka.FetchMessage
  2. ProcessingLock.Acquire(stage="fetcher", url)
     ├ 실패 (이미 처리 중) → skip + commit
     └ 성공 → continue
  3. CircuitBreaker.Allow(source)
     ├ open → defer/skip
     └ closed → continue
  4. handler.Registry.Handle(ctx, job)
     ├ 성공 → commit
     └ 에러:
        - Retryable + RetryCount < Max → job.ScheduledAt 셋팅 후 RetryScheduler.Enqueue(ctx, job, lastErr)
        - 비-Retryable 또는 Max 초과 → DLQ 발행 + commit
        - CircuitBreaker.RecordFailure(source)
```

<br>

## ProcessingLock (Redis SETNX 또는 Noop)

이슈 #178. 동일 URL 이 여러 worker / 인스턴스에서 단계별 (fetcher / parser / validator) 중복 처리되는 것을
차단. Stage prefix (`processing:fetcher:URL`, `processing:parser:URL` 등) 로 단계 구분.

```go
type ProcessingLock interface {
    Acquire(ctx context.Context, stage, url string) (acquired bool, err error)
    Release(ctx context.Context, stage, url string) error
}
```

구현:
- [`NewRedisProcessingLock`](../../../../internal/crawler/worker/processing_lock.go) — Redis `SET NX PX ttl`
- [`NoopProcessingLock`](../../../../internal/crawler/worker/processing_lock.go) — 항상 acquired=true (단일 프로세스 / dev)

<br>

## IngestionLock (Publisher 와 짝)

[`internal/publisher`](../publisher.md) 가 Kafka enqueue **직전**에 URL 을 marking — 이미 marked URL 은
Publisher 가 무시. ProcessingLock 은 처리 단계 dedup, IngestionLock 은 진입 dedup 으로 책임 분리.

```go
type IngestionLock interface {
    Acquire(ctx context.Context, url string) (acquired bool, err error)
}
```

기본 TTL 은 `redisCfg.IngestionLockTTL` (ENV 로 조정).

<br>

## RetryScheduler (Redis ZSET 또는 즉시)

이슈 #82. retry 대상 job 을 **worker slot 점유 없이** 미래 시점에 재발행.

```go
type RetryScheduler interface {
    Enqueue(ctx context.Context, job *core.CrawlJob, lastErr error) error
}
```

호출자 (worker pool) 는 `job.ScheduledAt` 과 `job.RetryCount` 를 미리 셋팅해 전달합니다 — 구현체가
score 로 사용하거나 즉시 발행하거나의 차이만 흡수.

구현:
- [`NewRedisDelayedRetryScheduler`](../../../../internal/crawler/worker/retry_scheduler.go) — Redis ZSET
  (score=`ScheduledAt` unix timestamp) + 별도 goroutine (`Start(ctx)` / `Stop()`) 이 ready job 을
  Kafka 에 publish — worker slot 미점유 (이슈 #82)
- [`KafkaImmediateRetryScheduler`](../../../../internal/crawler/worker/retry_scheduler.go) — Redis 부재 시
  fallback, 즉시 republish 후 worker 가 `ScheduledAt` 까지 sleep (worker slot 점유)

<br>

## CircuitBreaker (per-source)

[circuit_breaker.go](../../../../internal/crawler/worker/circuit_breaker.go). 사이트별 실패율을 추적하다가
일정 threshold 초과 시 일정 시간 차단 (Closed → Open → HalfOpen).

상태 전이는 INFO 로 로그.

<br>

## PriorityResolver

[resolver.go](../../../../internal/crawler/worker/resolver.go). retry 시 새 priority 결정 전략. 합성
가능 — `CompositeResolver` 가 여러 전략을 fallback chain 으로:

```go
resolver := NewCompositeResolver(PriorityNormal)
resolver.Add(NewSourcePriorityResolver(PriorityNormal))   // 사이트별 명시 우선순위
resolver.Add(NewRuleBasedPriorityResolver(PriorityNormal)) // 룰 기반 우선순위
```

<br>

## 의존

- [`internal/crawler/core`](core.md) — `CrawlJob`, `Priority`, `CrawlerError`
- [`internal/crawler/handler`](handler.md) — Registry dispatch
- [`internal/storage/service`](../storage/service.md) — `ContentService` (성공 시 ContentRef 발행)
- [`pkg/queue`](../../pkg/queue.md), [`pkg/redis`](../../pkg/redis.md), [`pkg/logger`](../../pkg/logger.md)

<br>

## 외부 시스템

| 시스템    | 용도                                                              |
|----------|------------------------------------------------------------------|
| Kafka    | TopicCrawlHigh/Normal/Low consume / TopicFetched / TopicDLQ produce |
| Redis    | ProcessingLock (SETNX) / IngestionLock (SETNX) / RetryQueue (ZSET) |

<br>

## 관련 이슈

- 이슈 #82 — Redis delayed retry queue
- 이슈 #178 — IngestionLock + ProcessingLock 단일 책임화
- 이슈 #134 — fetcher / parser 분리
- 이슈 #72 — graceful shutdown shutting_down 필드
- 이슈 #124 — backlog throttle (scheduler 와 연계)
