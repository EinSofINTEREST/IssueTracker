# pkg/config — Environment-Driven Configuration

소스: [`pkg/config/`](../../../pkg/config/)

`.env` + 환경변수 기반 설정 로더. 모든 컴포넌트가 본 패키지의 Load* 함수를 호출해 자기 config struct
를 받습니다.

이슈 #440 (PR #441) 에서 21 file 단일 패키지를 도메인별 6 sub-package 로 분리. 이슈 #439 (PR #442) 에서 env 값 검증을 위한 공통 `parse` helper 도입.

<br>

## Sub-package 구조

| Sub-package | 책임 | 주요 Loader |
|---|---|---|
| [`pkg/config/app/`](../../../pkg/config/app/) | 애플리케이션 부트스트랩 | `LoadLog`, `LoadMetrics`, `LoadShutdown` |
| [`pkg/config/storage/`](../../../pkg/config/storage/) | PostgreSQL / Redis 접속 | `Load` (DB), `LoadRedis` |
| [`pkg/config/fetcher/`](../../../pkg/config/fetcher/) | crawler / chromedp pool / 자동 transition | `LoadChromedpPool`, `LoadAutoUpgrade`, `LoadAutoDowngrade` |
| [`pkg/config/processor/`](../../../pkg/config/processor/) | parser/validate/scheduler/blacklist | `LoadBlacklist`, `LoadScheduler`, `LoadStaleRelearn`, `LoadValidate` |
| [`pkg/config/llm/`](../../../pkg/config/llm/) | LLM provider / prompt / classifier | `LoadLLM`, `LoadPrompt`, `LoadPathInfer`, `LoadClassifier`, `LoadGoogleCSE` |
| [`pkg/config/runtime/`](../../../pkg/config/runtime/) | worker count / stage toggle / retry scheduler / stage gate | `LoadStages`, `LoadWorkerCounts`, `LoadStageGate`, `LoadRetryScheduler` |
| [`pkg/config/internal/parse/`](../../../pkg/config/internal/parse/) | env 값 파싱 helper (port / duration / int / bool / float / ratio 등) — sub-package 들이 의존 |

<br>

## Loader 함수 (대표)

| 함수 | 패키지 | 반환 타입 | 결정 항목 |
|---|---|---|---|
| `app.LoadLog` | app | `LogConfig` | log level, pretty |
| `app.LoadMetrics` | app | `MetricsConfig` | `METRICS_ADDR` (default `:9090`) |
| `app.LoadShutdown` | app | `ShutdownConfig` | overall + claudegen shutdown timeout |
| `storage.Load` | storage | `DBConfig` | PostgreSQL host/port/user/password/database/MaxConns/MinConns/QueryTimeout |
| `storage.LoadRedis` | storage | `RedisConfig` | host/port/password/DB/IngestionLockTTL/poolSize |
| `fetcher.LoadChromedpPool` | fetcher | `ChromedpPoolConfig` | worker_count, semaphore, remote URLs |
| `fetcher.LoadAutoUpgrade` | fetcher | `AutoUpgradeConfig` | goquery → chromedp 자동 승격 임계값 |
| `processor.LoadBlacklist` | processor | `BlacklistConfig` | parser_blacklist 활성/캐시 TTL (이슈 #295/#297) |
| `processor.LoadScheduler` | processor | `SchedulerConfig` | entry interval / MaxBacklog / MaxRetries (이슈 #124) |
| `processor.LoadValidate` | processor | `ValidateConfig` | 본문 길이 임계값 / reliability 임계값 |
| `processor.LoadStaleRelearn` | processor | `StaleRelearnConfig` | stale rule 자동 재학습 (PR #294) |
| `llm.LoadLLM` | llm | `LLMConfig` | provider / API key / model / timeout / enabled (이슈 #149) |
| `llm.LoadPathInfer` | llm | `PathInferConfig` | refiner enabled / interval / minSamples (이슈 #173) |
| `llm.LoadPrompt` | llm | `PromptConfig` | prompt loader (file → embed chain) |
| `llm.LoadClassifier` | llm | `ClassifierConfig` | classifier gRPC/HTTP endpoint / timeout |
| `runtime.LoadStages` | runtime | `StagesConfig` | **stage toggle** — fetcher/parser/validate/enrich/scheduler 별 enable (이슈 #443) |
| `runtime.LoadWorkerCounts` | runtime | `WorkerCountsConfig` | fetcher/parser/validate/enrich worker pool 크기 |
| `runtime.LoadStageGate` | runtime | `StageGateConfig` | 단계별 ProcessingLock + Semaphore 임계값 |

각 Config struct 는 보통 `DSN()` 등 헬퍼 메소드를 포함합니다.

<br>

## Stage Toggle (이슈 #443)

`runtime.StagesConfig` 가 한 바이너리로 fetcher-only / parser-only / validate-only / enrich-only / scheduler-only 노드 운영을 가능하게 함. Kafka consumer group 이 partition 분배를 처리하므로 stage 간 결합은 토픽 경유로 유지.

```bash
STAGES_FETCHER_ENABLED=true    # default true
STAGES_PARSER_ENABLED=true     # default true
STAGES_VALIDATE_ENABLED=true   # default true
STAGES_ENRICH_ENABLED=true     # default true (이슈 #446)
STAGES_SCHEDULER_ENABLED=true  # default true
```

모든 stage 가 false 인 구성은 의미 없음 — `LoadStages` 가 에러로 거부.

<br>

## `parse` helper (이슈 #439)

`pkg/config/internal/parse/` 가 env 값 파싱/검증의 공통 정책을 제공:

- env 변수가 비어있으면 noop (default 보존)
- 비어있지 않으면 파싱 시도, 실패 시 명확한 wrap 에러
- 의미적 경계 검증 (port 범위 / 양수 / 비음수 / ratio 등) 실패 시 명확한 에러

**호출 패턴**:
```go
if err := parse.Port("POSTGRES_PORT", &cfg.Port); err != nil {
    return DBConfig{}, err
}
if err := parse.Duration("POSTGRES_QUERY_TIMEOUT", &cfg.QueryTimeout); err != nil {
    return DBConfig{}, err
}
```

잘못된 env 값에 의한 silent degraded 동작 (무한 대기 / port 0 / 음수 timeout 등) 방지가 목적.

<br>

## 설계 원칙

- 환경변수는 모두 `os.Getenv` 또는 `joho/godotenv` 로 읽음
- 누락 시 default 적용 또는 명시적 에러 (Loader 마다 정책)
- 도메인별 sub-package 로 분리 — 추가 ENV 키는 해당 sub-package 에 새 Load* 함수 추가 또는 기존 함수 확장
- struct 필드는 export — 호출자가 직접 읽음 (getter 없음)
- 파싱은 항상 `parse` helper 경유 — silent degraded 회피

<br>

## 호출 측

거의 모든 [`cmd/`](../cmd/README.md) entry point 와 [`internal/`](../internal/README.md) 의 wiring 진입부.
[`cmd/issuetracker.md`](../cmd/issuetracker.md) 의 "환경 변수 의존" 표 참조.

<br>

## 의존

- 외부: `github.com/joho/godotenv` — `.env` 파일 자동 로드 (entry point 가 호출)
- 본 패키지를 import 하는 다른 pkg/ 모듈 (queue, redis, llm 등) 은 자기 Config struct 만 받음 — `pkg/config`
  자체에 의존하는 일은 권장되지 않으나 현재 `pkg/redis` 등은 의존 (config struct 직접 사용 편의상)

<br>

## ENV 키 (요약)

| Prefix | 사용처 |
|---|---|
| `POSTGRES_*` | [storage](../internal/storage/postgres.md) |
| `KAFKA_*` | [pkg/queue](queue.md) |
| `REDIS_*` | [pkg/redis](redis.md) |
| `LLM_*`, `GEMINI_API_KEY`, `OPENAI_API_KEY`, `ANTHROPIC_API_KEY` | [pkg/llm](llm.md) |
| `CLAUDE_CODE_*` | [pkg/agent/claude](agent/claude.md) — claudegen 컨테이너 |
| `STAGES_*_ENABLED` | [runtime](#stage-toggle-이슈-443) — stage toggle (이슈 #443) |
| `FETCHER_*` | [fetcher](../internal/processor/fetcher/README.md) — chromedp pool / auto upgrade |
| `BLACKLIST_*` | parser_blacklist matcher (이슈 #295/#297) |
| `VALIDATE_*` | [validate.md](../internal/processor/validate.md) |
| `SCHEDULER_*` | [scheduler.md](../internal/scheduler.md) |
| `PATH_INFER_*`, `REFINEMENT_*` | [refiner](../internal/processor/parser/rule.md) |
| `METRICS_ADDR` | [pkg/metrics](metrics.md) |
| `LOG_*` | [pkg/logger](logger.md) |
| `CLASSIFIER_*` | [classifier](../internal/classifier/README.md) |
| `SHUTDOWN_TIMEOUT` | overall graceful shutdown |
| `ENRICHER_DB_RO_*` | enrich MCP postgres (이슈 #472) |

전체 키 목록은 [`.env.example`](../../../.env.example) 가 단일 소스.

<br>

## 관련 이슈

- 이슈 #439 — env 값 파싱/검증 공통 helper (`parse` package)
- 이슈 #440 — 21 file → 6 sub-package 도메인 그룹화
- 이슈 #443 — stage toggle env flags
- 이슈 #446 — enrich stage 도입
- 이슈 #472 — enrich MCP postgres ENV
