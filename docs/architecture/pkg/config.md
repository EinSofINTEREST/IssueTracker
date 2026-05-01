# pkg/config — Environment-Driven Configuration

소스: [`pkg/config/config.go`](../../../pkg/config/config.go)

`.env` + 환경변수 기반 설정 로더. 모든 컴포넌트가 본 패키지의 Load* 함수를 호출해 자기 config struct
를 받습니다.

<br>

## Loader 함수

| 함수                  | 반환 타입             | 결정 항목                                          |
|----------------------|---------------------|---------------------------------------------------|
| `Load()`              | `DBConfig`           | PostgreSQL host/port/user/password/database/MaxConns/MinConns |
| `LoadLog()`           | `LogConfig`          | log level, pretty                                  |
| `LoadMetrics()`       | `MetricsConfig`      | `METRICS_ADDR` (default `:9090`)                  |
| `LoadValidate()`      | `ValidateConfig`     | 본문 길이 임계값 / reliability 임계값             |
| `LoadScheduler()`     | `SchedulerConfig`    | entry interval / MaxBacklog / MaxRetries          |
| `LoadRefinement()`    | `RefinementConfig`   | refiner enabled / interval / minSamples (이슈 #173) |
| `LoadLLM()`           | `LLMConfig`          | provider / API key / model / timeout / enabled (이슈 #149) |
| `LoadRedis()`         | `RedisConfig`        | host/port/password/DB/IngestionLockTTL/poolSize   |
| `LoadClassifier()`    | `ClassifierConfig`   | gRPC/HTTP endpoint / timeout                       |

각 Config struct 는 보통 `DSN()` 등 헬퍼 메소드를 포함합니다.

<br>

## 설계 원칙

- 환경변수는 모두 `os.Getenv` 또는 `joho/godotenv` 로 읽음
- 누락 시 default 적용 또는 명시적 에러 (Loader 마다 정책)
- 한 곳 (`config.go`) 에 집중 — ENV 키 추가는 본 파일에 새 Load* 함수 추가 또는 기존 함수 확장
- struct 필드는 export — 호출자가 직접 읽음 (getter 없음)

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

| Prefix             | 사용처                                        |
|--------------------|----------------------------------------------|
| `DB_*`             | [Load](../internal/storage/postgres.md)       |
| `KAFKA_*`          | [pkg/queue](queue.md)                         |
| `REDIS_*`          | [pkg/redis](redis.md)                         |
| `LLM_*`, `GEMINI_API_KEY`, `OPENAI_API_KEY`, `ANTHROPIC_API_KEY` | [pkg/llm](llm.md) |
| `VALIDATE_*`       | [validate.md](../internal/processor/validate.md) |
| `SCHEDULER_*`      | [scheduler.md](../internal/scheduler.md)      |
| `REFINEMENT_*`     | [refiner](../internal/crawler/parser.md)      |
| `METRICS_ADDR`     | [pkg/metrics](metrics.md)                     |
| `LOG_*`            | [pkg/logger](logger.md)                        |
| `CLASSIFIER_*`     | [classifier](../internal/classifier/README.md) |

전체 키 목록은 [`.env.example`](../../../.env.example) 가 단일 소스.
