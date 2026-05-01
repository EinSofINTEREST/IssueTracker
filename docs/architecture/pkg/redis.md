# pkg/redis — Redis Client Wrapper

소스: [`pkg/redis/`](../../../pkg/redis/)

`go-redis/v9` 위에 얇게 wrap 한 클라이언트. 분산 락 (SET NX PX) 과 ZSET 기반 retry queue 를 위한
연결 객체를 제공합니다.

<br>

## API

```go
client, err := redis.New(ctx, redisCfg)  // Ping 으로 연결 검증
defer client.Close()
```

`*Client` 자체에는 `AcquireLock` / `ReleaseLock` / `Enqueue` / `Dequeue` 등 helper 가 정의되어 있을 수
있으나, 현재 시스템에서는 **`*Client` 를 직접 받아** 다음 컴포넌트들이 필요한 명령을 발행합니다:

- `RedisProcessingLock` ([`internal/locks`](../internal/locks/README.md)) — `SET ... NX PX ttl` (이슈 #178)
- `RedisIngestionLock` ([`internal/locks`](../internal/locks/README.md)) — `SET ... NX PX ttl`
- `RedisDelayedRetryScheduler` ([`internal/processor/fetcher/worker`](../internal/processor/fetcher/worker.md)) — ZSET (`ZADD score=runAt`, `ZRANGEBYSCORE`, `ZREM`)

<br>

## 구성

| 파일                                         | 역할                                                |
|---------------------------------------------|-----------------------------------------------------|
| [client.go](../../../pkg/redis/client.go)    | `Client` — go-redis wrap + Ping 검증                |
| (필요 시) lock.go / retry_queue.go           | 락/재시도 큐 helper (사용 패턴에 따라 추가)         |

<br>

## RedisConfig

[`pkg/config.LoadRedis`](config.md):
- `Host`, `Port`, `Password`, `DB`
- `DialTimeout`, `ReadTimeout`, `WriteTimeout`, `PoolSize`
- `IngestionLockTTL` — IngestionLock 의 SET PX 값 (URL 진입 marker 유효 시간)

<br>

## 의존

- 외부: `github.com/redis/go-redis/v9`
- [`pkg/config`](config.md) — `RedisConfig`

<br>

## 호출 측

- [`cmd/issuetracker`](../cmd/issuetracker.md) 단계 7 — `redis.New(ctx, cfg)` + `defer client.Close()`
- [`internal/locks`](../internal/locks/README.md) — Client 를 받아 ProcessingLock / IngestionLock 구현
- [`internal/processor/fetcher/worker`](../internal/processor/fetcher/worker.md) — Client 를 받아 RetryScheduler 구현

Redis 가 부재일 때 [`cmd/issuetracker`](../cmd/issuetracker.md) 는 graceful degrade — `NoopProcessingLock`
fallback.

<br>

## 관련 이슈

- 이슈 #82 — delayed retry queue
- 이슈 #178 — ProcessingLock + IngestionLock 단일 책임화
