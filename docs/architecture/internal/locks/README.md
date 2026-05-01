## internal/locks — Stage-Agnostic Distributed Coordination

소스: [`internal/locks/`](../../../../internal/locks/)

크롤링 파이프라인 단계 (fetcher / parser / validator) 간 공통으로 사용하는 **Redis 기반 distributed lock**
인터페이스. 단일 패키지로 분리되어 어떤 stage 도 동일 lock 인스턴스를 공유 — 단계 prefix 만으로 stage 구분.

## 핵심 타입

| 타입                              | 위치                                                                | 책임                                                          |
|----------------------------------|---------------------------------------------------------------------|---------------------------------------------------------------|
| `IngestionLock` (interface)       | [ingestion_lock.go](../../../../internal/locks/ingestion_lock.go)   | URL pipeline 진입 marker — Publisher 가 사용 (이슈 #178, #126) |
| `RedisIngestionLock`              | [ingestion_lock.go](../../../../internal/locks/ingestion_lock.go)   | Redis SETNX 기반 구현                                          |
| `NoopIngestionLock`               | [ingestion_lock.go](../../../../internal/locks/ingestion_lock.go)   | 항상 acquired=true (단일 프로세스 / dev)                        |
| `ProcessingLock` (interface)      | [processing_lock.go](../../../../internal/locks/processing_lock.go) | URL 동시 처리 방지 — fetcher/parser/validator 공유 (이슈 #178)  |
| `RedisProcessingLock`             | [processing_lock.go](../../../../internal/locks/processing_lock.go) | Redis SETNX 기반 구현                                          |
| `NoopProcessingLock`              | [processing_lock.go](../../../../internal/locks/processing_lock.go) | 항상 acquired=true (단일 프로세스 / dev)                        |
| `ProcessingKey(stage, url)`       | [processing_lock.go](../../../../internal/locks/processing_lock.go) | (stage, normalized_url) → Redis key 변환 헬퍼                  |
| `Stage{Fetcher,Parser,Validator}` | [processing_lock.go](../../../../internal/locks/processing_lock.go) | stage 상수                                                     |

## ProcessingLock 패턴

```go
type ProcessingLock interface {
    Acquire(ctx context.Context, key string) (acquired bool, err error)
    Release(ctx context.Context, key string) error
}

key := locks.ProcessingKey(locks.StageFetcher, normalizedURL)
acquired, err := lock.Acquire(ctx, key)
if !acquired {
    // 다른 worker 가 처리 중 — skip + commit
}
defer lock.Release(ctx, key)
```

stage 분기는 호출자 책임 — `locks` 패키지 자체는 단순 key/value SETNX 만 노출.

## IngestionLock 패턴

```go
type IngestionLock interface {
    Acquire(ctx context.Context, url string) (acquired bool, err error)
    Invalidate(ctx context.Context, url string) error
}

// Publisher 가 enqueue 직전 호출
acquired, err := lock.Acquire(ctx, normalizedURL)
if !acquired {
    // 이미 pipeline 진입 marker 있음 — 발행 skip
}
```

기본 TTL 은 `redisCfg.IngestionLockTTL` (`INGESTION_LOCK_TTL` 환경변수).

## 호출 측

- [`internal/publisher`](../publisher.md) — `IngestionLock` (Kafka enqueue 직전 dedup)
- [`internal/processor/fetcher/worker`](../processor/fetcher/worker.md) — fetcher worker pool 의 `ProcessingLock(StageFetcher)`
- [`internal/processor/parser/worker`](../processor/parser/README.md) — parser worker 의 `ProcessingLock(StageParser)`
- [`internal/processor/validate`](../processor/validate.md) — validator 의 `ProcessingLock(StageValidator)`

## 외부 시스템

- **Redis**: SETNX (`SET NX PX ttl`) — Acquire / Release / Invalidate

## 관련 이슈

- 이슈 #126 — URL dedup 도입 (Ingestion Lock 의 전신)
- 이슈 #178 — Ingestion Lock + Processing Lock 분리, 단계별 단일 책임화
- 이슈 #197 — `crawler/worker` 에서 lock 인프라를 `internal/locks` 로 분리 (단계 무관 패키지화)
