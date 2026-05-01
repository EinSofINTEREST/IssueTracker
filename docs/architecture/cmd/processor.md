# cmd/processor — Standalone Validator

소스: [`cmd/processor/main.go`](../../../cmd/processor/main.go)
산출물: `bin/processor` (`make build` 또는 `make run-processor`)

[`internal/processor/validate`](../../../internal/processor/validate/) 단계만 단독 실행하는 바이너리.
[`cmd/issuetracker`](issuetracker.md) 의 부분집합으로, dev/test 또는 검증 단독 스케일링이 필요할 때 사용합니다.

<br>

## 역할

- `issuetracker.normalized` consume → `issuetracker.validated` (또는 DLQ) 발행
- crawler / parser 가 이미 떠 있는 환경에서 검증 인스턴스를 별도 process 로 띄울 때 사용
- **NoopProcessingLock 사용** — Redis wiring 없음, 다중 인스턴스 운영은 [`cmd/issuetracker`](issuetracker.md) 권장

<br>

## Wiring 요약

```
1. logger / metrics endpoint
2. LoadValidate() — 검증 임계값
3. Kafka: Consumer(TopicNormalized) + Producer
4. PostgreSQL pool → ContentRepository → ContentService
5. validate.NewWorker(... NoopProcessingLock{} ...)
6. SIGTERM → cancel → worker.Stop(30s timeout)
```

자세한 검증 로직은 [internal/processor/validate.md](../internal/processor/validate.md) 참조.

<br>

## 의존 패키지

- [`internal/processor/validate`](../../../internal/processor/validate/) — Worker / Validator
- [`internal/crawler/worker`](../../../internal/crawler/worker/) — `NoopProcessingLock`
- [`internal/storage/postgres`](../../../internal/storage/postgres/) + [`internal/storage/service`](../../../internal/storage/service/)
- [`pkg/config`](../../../pkg/config/), [`pkg/queue`](../../../pkg/queue/), [`pkg/logger`](../../../pkg/logger/), [`pkg/metrics`](../../../pkg/metrics/)

<br>

## issuetracker 와의 차이

| 항목                    | `cmd/processor`                        | `cmd/issuetracker`                      |
|-------------------------|---------------------------------------|----------------------------------------|
| 실행 stage              | validate 만                            | crawler + parser + validator + scheduler + refiner |
| ProcessingLock          | `NoopProcessingLock{}` (no-op)         | `NewRedisProcessingLock` (Redis SETNX)  |
| RetryScheduler / IngestionLock | 없음                            | Redis 기반 (이슈 #82, #178)             |
| 다중 인스턴스 안전성    | Kafka consumer group rebalance 로 동시 처리 가능성 — dev/test 권장 | 단일 ProcessingLock 공유로 안전        |
