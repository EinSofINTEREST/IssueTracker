# internal/ — Private Application Code

[`internal/`](../../../internal/) 는 IssueTracker 의 비공개 비즈니스 로직을 담는 디렉토리입니다.
Go 의 `internal` 규칙에 의해 외부 모듈에서 import 할 수 없으므로 안전하게 깨질 수 있는 인터페이스를
보유합니다.

이 디렉토리의 패키지는 [`pkg/`](../../../pkg/) 의 generic 유틸을 사용해 도메인 로직을 구성하며,
[`cmd/`](../../../cmd/) 의 entry point 가 이들을 wire 합니다.

<br>

## 패키지 일람

| 디렉토리                                       | 역할                                                       | 문서                                                       |
|-----------------------------------------------|-----------------------------------------------------------|-----------------------------------------------------------|
| [`internal/processor/fetcher/`](../../../internal/processor/fetcher/) | 웹 크롤링 — fetch + DB-driven parse + rate limit + worker pool | [processor/fetcher/README.md](processor/fetcher/README.md)                    |
| [`internal/processor/parser/`](../../../internal/processor/parser/)   | Claim Check 기반 ParserWorker (TopicFetched 소비)         | [parser/README.md](processor/parser/README.md)                      |
| [`internal/processor/`](../../../internal/processor/) | 검증 stage (news/community Validator)                | [processor/README.md](processor/README.md)                |
| [`internal/classifier/`](../../../internal/classifier/) | ELArchive Classifier client (gRPC + HTTP fallback)  | [classifier/README.md](classifier/README.md)              |
| [`internal/publisher/`](../../../internal/publisher/) | chained CrawlJob 발행 + IngestionLock                 | [publisher.md](publisher.md)                              |
| [`internal/scheduler/`](../../../internal/scheduler/) | 주기적 seed job 발행 + backlog throttle              | [scheduler.md](scheduler.md)                              |
| [`internal/storage/`](../../../internal/storage/)     | Repository 인터페이스 + PostgreSQL 구현 + Service 계층 | [storage/README.md](storage/README.md)                    |

<br>

## 레이어 규칙

[.claude/rules/04-error-handling.md](../../../.claude/rules/04-error-handling.md) 의
"Layer Rules" 와 정합:

- 외부에 노출되는 boundary 메소드는 `core.CrawlerError` 로 카테고리화된 에러 반환
- 내부 helper 는 `fmt.Errorf("...: %w", err)` wrap 사용
- `pkg/` 의 generic util 이 반환한 에러는 `internal/` boundary 에서 categorized 에러로 변환
