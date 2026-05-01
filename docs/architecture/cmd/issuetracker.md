# cmd/issuetracker — Integrated Pipeline

소스: [`cmd/issuetracker/main.go`](../../../cmd/issuetracker/main.go)
산출물: `bin/issuetracker` (`make build`)

전체 파이프라인 (Crawler + Parser + Validator + Scheduler + Refiner) 을 **단일 프로세스에서 wire** 하는
오케스트레이터. 운영 시 권장 entry point.

<br>

## 역할

- `main()` 은 오직 **wiring 다이어그램** — 비즈니스 로직 0
- 각 단계는 동일 `context.Context` 를 공유 → SIGTERM 시 일괄 cancel
- Redis / LLM 은 fail-soft — 부재 시 noop fallback 으로 graceful degrade

<br>

## Wiring 흐름 (코드 순서)

```
1. logger / metrics endpoint    (pkg/logger, pkg/metrics)
2. Kafka producer + 3-tier consumers (high/normal/low)
3. PostgreSQL pool              (internal/storage/postgres)
4. rule.Parser + rule.Resolver  (parsing_rules 시드 검증 → 부재 시 fail-fast)
5. raw_contents Service / Content Service (Claim Check)
6. Source 등록                  (kr.Register / us.Register → handler.Registry)
7. Redis: ProcessingLock / IngestionLock / DelayedRetryScheduler (이슈 #178, #82)
8. PoolManager (3-tier crawler workers)
9. LLM provider (chain policy, 이슈 #149)
10. ParserWorker (consumer group: parsers)
11. Refiner (path_pattern 정밀화, 이슈 #173 단계 4-2)
12. RawContentCleaner cron       (Claim Check 잔존물 정리)
13. Scheduler + BacklogThrottler (이슈 #124)
14. Validate Worker
15. SIGTERM 대기 → 역순 graceful shutdown
```

각 단계의 자세한 책임은 해당 패키지 문서 참조:

- [internal/processor/fetcher/worker.md](../internal/processor/fetcher/worker.md) — PoolManager, RetryScheduler
- [internal/locks/README.md](../internal/locks/README.md) — ProcessingLock, IngestionLock
- [internal/parser/rule.md](../internal/parser/rule.md) — rule.Parser, llmgen, refiner
- [internal/parser/README.md](../internal/parser/README.md) — ParserWorker (Claim Check)
- [internal/processor/validate.md](../internal/processor/validate.md) — Validator
- [internal/scheduler.md](../internal/scheduler.md) — seed job 발행

<br>

## Build helpers (main.go 내부)

| 함수                      | 책임                                                        | 실패 동작        |
|--------------------------|------------------------------------------------------------|------------------|
| `buildLLMProvider()`      | `LLM_ENABLED` + API key 검증 → [`pkg/llm`](../../../pkg/llm/) chain provider 구성 | nil 반환 (LLM 비활성) |
| `buildLLMGenerator()`     | provider nil 이면 nil — 아니면 [`llmgen.New`](../../../internal/parser/rule/llmgen/) | nil 허용         |
| `buildRefiner()`          | `REFINEMENT_ENABLED` + provider 옵션 결합 → [`refiner.New`](../../../internal/parser/rule/refiner/) | nil 반환 (정밀화 비활성) |
| `verifyParsingRulesSeeded()` | 기본 파싱 규칙(seed data) 이 DB 에 정상 로드됐는지 검증 | **fail-fast (Fatal)** |

<br>

## 환경 변수 의존

config 로딩은 [`pkg/config`](../../../pkg/config/) 가 단일 소스. 주요 키:

| Loader                  | 결정 항목                                              | 비활성 시                               |
|-------------------------|-------------------------------------------------------|----------------------------------------|
| `LoadLog()`             | 로그 레벨 / Pretty 출력                                | 기본값                                  |
| `LoadMetrics()`         | `METRICS_ADDR` (default `:9090`)                      | 빈 값이면 endpoint 미기동              |
| `Load()` (DB)           | PostgreSQL 접속                                        | **fail-fast**                          |
| `LoadRedis()`           | Redis 접속                                             | warn 후 NoopProcessingLock fallback    |
| `LoadLLM()`             | provider, model, API key, timeout                     | nil provider                            |
| `LoadRefinement()`      | refiner interval / min samples                        | nil refiner                             |
| `LoadScheduler()`       | seed entry interval / backlog 임계값                  | **fail-fast**                          |
| `LoadValidate()`        | 검증 임계값                                            | **fail-fast**                          |

<br>

## Worker 카운트 상수

```go
const (
    validateWorkerCount = 8
    parserWorkerCount   = 6
)
```

- `validateWorkerCount` ≤ `issuetracker.normalized` 파티션 수 (기본 32)
- `parserWorkerCount` 는 fetcher 와 독립 — chromedp/LLM 부담이 클수록 증설

<br>

## Graceful Shutdown 순서

SIGINT/SIGTERM 수신 시:

1. logger 에 `shutting_down=true` 부착 (이슈 #72)
2. root `cancel()` — 모든 goroutine 의 ctx 무효화
3. shutdownCtx (30s timeout) 로 역순 Stop:
   - `sched.Stop()`
   - `manager.Stop(shutdownCtx)` — drain crawler workers
   - `pw.Stop(shutdownCtx)` — parser worker
   - `llmGen.Stop(shutdownCtx)` — in-flight LLM call drain (이슈 #149)
   - `pathRefiner.Stop(shutdownCtx)` — refiner cycle drain (PR #191)
   - `cleaner.Stop()` — cleanup cron
   - `validateWorker.Stop(shutdownCtx)`
4. `defer` 체인이 Kafka producer/consumer, redis, pgpool 닫음
