# internal/crawler/worker — Pool Manager + Retry/CircuitBreaker

소스: [`internal/crawler/worker/`](../../../../internal/crawler/worker/)

크롤러 단계의 핵심 오케스트레이터. **3-tier priority Kafka consumer pool** 을 운영하며, **Redis 기반
RetryScheduler** 로 지연 재시도를 처리하고 **per-source CircuitBreaker** 로 실패 폭주를 차단합니다.

> 단계 무관 distributed lock (ProcessingLock / IngestionLock) 은 [`internal/locks`](../locks/README.md)
> 로 분리됨 (이슈 #197). fetcher worker 는 `locks.ProcessingLock(stage="fetcher", url)` 형태로 사용.

<br>

## 핵심 타입

| 타입                           | 위치                                                                   | 책임                                                            |
|-------------------------------|-----------------------------------------------------------------------|-----------------------------------------------------------------|
| `PoolManager`                  | [manager.go](../../../../internal/crawler/worker/manager.go)           | 3개 priority pool 의 lifecycle 관리                              |
| `KafkaConsumerPool`            | [pool.go](../../../../internal/crawler/worker/pool.go)                 | 단일 priority 의 worker goroutine pool + retry 라우팅           |
| `RetryScheduler` (interface)   | [retry_scheduler.go](../../../../internal/crawler/worker/retry_scheduler.go) | 지연 retry — Redis ZSET 또는 즉시 republish                  |
| `CircuitBreakerRegistry`       | [circuit_breaker.go](../../../../internal/crawler/worker/circuit_breaker.go) | per-source 실패율 트래킹 + 차단                              |
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
