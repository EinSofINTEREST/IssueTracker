# internal/processor/fetcher/core — Interfaces, Models, Errors

소스: [`internal/processor/fetcher/core/`](../../../../internal/processor/fetcher/core/)

크롤러 서브시스템의 가장 기초가 되는 **타입/인터페이스/에러** 만 담는 패키지. 다른 어떤 crawler
하위 패키지도 본 패키지를 import 합니다 (sink 노드).

<br>

## 핵심 인터페이스

| 인터페이스         | 위치                                                              | 책임                                                  |
|--------------------|------------------------------------------------------------------|-------------------------------------------------------|
| `Crawler`          | [crawler.go](../../../../internal/processor/fetcher/core/crawler.go)        | Initialize / Start / Stop / Fetch / HealthCheck       |
| `Parser`           | (동일 파일)                                                       | RawContent → 도메인 모델 (별도 [parser](../parser/rule.md) 가 구현)  |
| `RateLimiter`      | (동일 파일)                                                       | Wait(ctx) — 토큰 buckets 등으로 호출 빈도 제한        |
| `URLRateLimiter`   | (동일 파일)                                                       | URL → IP 매핑 + per-IP rate limit (DNS 해석 필요)     |
| `HTTPClient`       | [http_client.go](../../../../internal/processor/fetcher/core/http_client.go) | Do(req) — 표준 `*http.Client` wrapper                |

<br>

## 핵심 모델

| 타입                  | 위치                                                              | 설명                                                    |
|-----------------------|------------------------------------------------------------------|---------------------------------------------------------|
| `SourceInfo`          | [models.go](../../../../internal/processor/fetcher/core/models.go)          | Country / Type / Name / BaseURL / Language              |
| `Target`, `TargetType` | (동일)                                                            | URL + page/list/article 등 타입                         |
| `RawContent`          | (동일)                                                            | HTML / StatusCode / Headers + 메타                      |
| `Content`             | (동일)                                                            | 파싱된 본문 (ID, Title, Body, Author, PublishedAt, …)   |
| `RawContentRef`, `ContentRef` | (동일)                                                    | Kafka 메시지로 보내는 lightweight 참조 (Claim Check)   |
| `CrawlJob`            | [job.go](../../../../internal/processor/fetcher/core/job.go)                | Kafka 직렬화 가능 JSON (RetryCount/Priority 포함)       |
| `ProcessingMessage`   | (동일)                                                            | 파이프라인 stage 간 wrapper                              |
| `Priority`            | (동일)                                                            | High=1 / Normal=2 / Low=3                              |
| `Config`              | (동일)                                                            | HTTP timeout / User-Agent / RPS 등 기본값               |

<br>

## 에러 / HTTP 상태

| 파일                                                                  | 책임                                                |
|----------------------------------------------------------------------|-----------------------------------------------------|
| [errors.go](../../../../internal/processor/fetcher/core/errors.go)              | `CrawlerError` + 카테고리 / 코드 / Retryable 플래그 |
| [http_status.go](../../../../internal/processor/fetcher/core/http_status.go)    | 상태 코드 → 카테고리 변환 helper                    |
| [retry.go](../../../../internal/processor/fetcher/core/retry.go)                | `WithRetry` (지수 backoff + Retryable 검사)         |

`CrawlerError` 의 카테고리 / 코드 체계는 [.claude/rules/04-error-handling.md](../../../../.claude/rules/04-error-handling.md)
가 단일 소스. boundary 레이어에서는 반드시 본 타입으로 반환.

<br>

## 부수 helper

| 파일                                                                  | 역할                                                     |
|----------------------------------------------------------------------|----------------------------------------------------------|
| [extractor.go](../../../../internal/processor/fetcher/core/extractor.go)        | HTML 으로부터 link 추출 wrapper (boundary 레이어)         |
| [http2_metrics.go](../../../../internal/processor/fetcher/core/http2_metrics.go) | HTTP/2 latency 등 metrics 수집                          |
| [ip_resolver.go](../../../../internal/processor/fetcher/core/ip_resolver.go)    | host → IP 해석 (rate limiter 가 IP 단위로 동작)         |

<br>

## 의존 관계

- **누구도 import 하지 않는** 외부 의존 외엔 standard lib + zerolog 정도
- 본 패키지를 import 하는 패키지: `domain/`, `handler/`, `worker/`, `parser/`, `rate_limiter/`, 그리고 [`internal/processor/parser`](../parser/README.md), [`internal/scheduler`](../../scheduler.md), [`internal/storage/service`](../../storage/service.md) 등 — 사실상 거의 모든 곳

본 패키지가 변경되면 광범위한 영향이 있으므로 신중히. 새 인터페이스 추가는 자유롭지만 기존 인터페이스
시그니처 변경은 큰 PR 이 됩니다.
