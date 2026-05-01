# pkg/ — Public Library Code

[`pkg/`](../../../pkg/) 는 외부 모듈도 import 할 수 있는 generic 유틸리티를 보유합니다.
도메인 의존성이 없는 standalone 라이브러리로 설계되어, [`internal/`](../../../internal/) 의 비즈니스
로직과 분리됩니다.

[.claude/rules/04-error-handling.md](../../../.claude/rules/04-error-handling.md) 의 레이어 규칙:
**`pkg/` 는 `fmt.Errorf` wrap 만 사용**, `CrawlerError` 는 호출하는 `internal/` boundary 가 변환.

<br>

## 패키지 일람

| 디렉토리                                  | 역할                                                            | 문서                                |
|------------------------------------------|----------------------------------------------------------------|-------------------------------------|
| [`pkg/config/`](../../../pkg/config/)     | 환경변수 + .env 기반 설정 로더 (DB / Kafka / Redis / LLM / Validate / Scheduler / Refinement / Metrics / Log / Classifier) | [config.md](config.md) |
| [`pkg/links/`](../../../pkg/links/)       | URL 정규화 (Normalizer) + 링크 추출 (Extractor)                | [links.md](links.md)                |
| [`pkg/llm/`](../../../pkg/llm/)           | 다중 LLM provider 추상 + Chain 합성 + 정책 라우팅              | [llm.md](llm.md)                    |
| [`pkg/logger/`](../../../pkg/logger/)     | zerolog 기반 구조화 로거 + ctx 전파                             | [logger.md](logger.md)              |
| [`pkg/metrics/`](../../../pkg/metrics/)   | Prometheus registry + `/metrics` HTTP endpoint                  | [metrics.md](metrics.md)            |
| [`pkg/queue/`](../../../pkg/queue/)       | Kafka producer/consumer + 토픽/그룹 상수 + BacklogChecker      | [queue.md](queue.md)                |
| [`pkg/redis/`](../../../pkg/redis/)       | Redis 클라이언트 wrapper + 분산 락 + ZSET retry queue          | [redis.md](redis.md)                |
| [`pkg/urlguard/`](../../../pkg/urlguard/) | URL 허용/차단 술어 (Guard) + 적용 dispatcher (Gate)            | [urlguard.md](urlguard.md)          |

<br>

## 의존 그래프 (pkg 내부)

```
config ── 다른 pkg 가 의존 (logger / queue / redis / llm 모두 config 를 받음)
logger ── 다른 pkg 가 옵션으로 의존 (nil 허용)
queue / redis / metrics / llm / links / urlguard ── 서로 독립

llm/{anthropic, gemini, openai} ── llm/{providers, factory} 가 init() 으로 등록
llm/chain ── llm 인터페이스 사용
llm/policy ── llm 인터페이스 사용
```

<br>

## 외부 노출 가능 여부

`pkg/` 는 이름상 외부 import 가능하지만, 실제로는 IssueTracker 전용으로만 사용 중입니다. 네이밍/시그니처
변경 시 cross-package 영향 검토 필요.
