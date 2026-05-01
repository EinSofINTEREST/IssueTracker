# IssueTracker — Architecture Reference

이 디렉토리는 IssueTracker 시스템의 모듈/연결 관계를 **프로젝트 디렉토리 트리와 동일한 구조**로
정리한 참조 문서입니다. 각 문서는 코드 주석/스펙을 대체하지 않으며, **모듈이 무엇을 하고 어디로
연결되는지** 빠르게 파악하기 위한 지도입니다.

상위 규칙은 [.claude/rules/01-architecture.md](../../.claude/rules/01-architecture.md) 가 정의하며,
이 문서는 그 규칙이 실제 코드에 어떻게 매핑되는지를 보여줍니다.

<br>

## 시스템 한 줄 요약

글로벌 뉴스/커뮤니티 이슈를 **크롤 → 파싱 → 정규화 → 검증 → 분류** 단계로 흘려보내는
Kafka 기반 Go 파이프라인. 각 단계는 토픽으로 분리되어 독립 스케일링 가능.

<br>

## 디렉토리 트리 (architecture docs)

```
docs/architecture/
├── README.md                              ← (you are here)
├── cmd/                                   ← entry point 별 역할
│   ├── README.md
│   ├── api.md
│   ├── issuetracker.md                    ← 통합 파이프라인 오케스트레이터 (main wiring)
│   ├── processor.md                       ← validator-only 단독 실행체
│   ├── migrate.md
│   ├── migrate-down.md
│   └── rldebug.md
├── internal/                              ← 비공개 비즈니스 로직
│   ├── README.md
│   ├── classifier/                        ← ELArchive Classifier client (gRPC + HTTP fallback)
│   │   ├── README.md
│   │   ├── grpc.md
│   │   └── http.md
│   ├── locks/                             ← 단계 무관 distributed lock (이슈 #197)
│   │   └── README.md                      ← ProcessingLock + IngestionLock (Redis SETNX)
│   ├── processor/                         ← 파이프라인 단계별 정렬 (이슈 #195)
│   │   ├── README.md
│   │   ├── fetcher/                       ← Web fetch + DB-driven parse + rate limit + worker pool (이슈 #198)
│   │   │   ├── README.md
│   │   │   ├── core.md                    ← 인터페이스 + 모델 + 에러
│   │   │   ├── domain.md                  ← Fetcher chain + 사이트 등록
│   │   │   ├── handler.md                 ← Registry (crawler_name → Handler)
│   │   │   ├── implementation.md          ← chromedp / goquery
│   │   │   ├── rate_limiter.md
│   │   │   └── worker.md                  ← PoolManager + RetryScheduler + CircuitBreaker
│   │   ├── parser/                        ← Domain-Agnostic Parser + Claim Check Worker (이슈 #204)
│   │   │   ├── README.md
│   │   │   └── rule.md                    ← rule.Parser (DB-driven) + llmgen + pathinfer + refiner
│   │   └── validate.md                    ← news/community Validator
│   ├── publisher.md                       ← chained job 발행 + IngestionLock
│   ├── scheduler.md                       ← seed job 주기적 발행
│   └── storage/
│       ├── README.md
│       ├── postgres.md
│       └── service.md                     ← ContentService / RawContentService
├── pkg/                                   ← 공개 유틸리티
│   ├── README.md
│   ├── config.md
│   ├── links.md
│   ├── llm.md                             ← provider 추상 + chain policy
│   ├── logger.md
│   ├── metrics.md
│   ├── queue.md                           ← Kafka 토픽/그룹 상수
│   ├── redis.md
│   └── urlguard.md
└── proto/
    └── classifier.md                      ← classifier.proto 서비스 정의
```

<br>

## 데이터 흐름 — 한 장 요약

```
┌─────────────┐
│  Scheduler  │  internal/scheduler — DefaultEntries(): CNN, Naver, Yonhap, Daum 카테고리 URL
└──────┬──────┘
       │ CrawlJob (priority high/normal/low)
       ▼
┌──────────────────────────────────────────────────────────────────────────┐
│ Kafka topics: issuetracker.crawl.{high,normal,low}                       │
└──────┬───────────────────────────────────────────────────────────────────┘
       │
       ▼
┌─────────────────────┐    ProcessingLock (Redis SETNX) — URL 동시처리 방지
│   PoolManager       │    CircuitBreaker — source 단위 실패 트래킹
│   (3-tier pools)    │    RetryScheduler (Redis ZSET) — 지연 재시도
│ internal/processor/fetcher/   │
│       worker        │
└──────┬──────────────┘
       │ dispatch by crawler_name
       ▼
┌─────────────────────┐    Chain of Responsibility:
│  ChainHandler       │      goquery (정적 HTML)
│ (processor/fetcher/domain/    │        ↓ (동적/lazy 감지)
│      general)       │      chromedp (헤드리스 Chrome)
└──────┬──────────────┘
       │ RawContent → DB(raw_contents) → RawContentRef (Claim Check)
       ▼
┌──────────────────────────────────────────────────────────────────────────┐
│ Kafka: issuetracker.fetched                                              │
└──────┬───────────────────────────────────────────────────────────────────┘
       │
       ▼
┌─────────────────────┐    rule.Parser (DB-driven, parsing_rules 테이블)
│   ParserWorker      │      ├─ ErrNoRule  → llmgen.Generator (async, enabled=false)
│ internal/processor/parser/    │      └─ list 페이지 → Publisher.PublishBatch (chained CrawlJob)
│       worker        │
└──────┬──────────────┘
       │ Content → ContentService.Store → ContentRef
       ▼
┌──────────────────────────────────────────────────────────────────────────┐
│ Kafka: issuetracker.normalized                                           │
└──────┬───────────────────────────────────────────────────────────────────┘
       │
       ▼
┌─────────────────────┐    news / community Validator
│  Validate Worker    │    Title/Body/PublishedAt 길이 검증, reliability scoring
│ internal/processor/ │    Pass → ContentRef 발행 / Fail → 삭제 + DLQ
│      validate       │
└──────┬──────────────┘
       │
       ▼
┌──────────────────────────────────────────────────────────────────────────┐
│ Kafka: issuetracker.validated   (현 단계는 후속 분류기/임베더 진입점)     │
└──────────────────────────────────────────────────────────────────────────┘

부수 흐름:
  • Refiner (internal/processor/parser/rule/refiner) — 주기적 polling 으로 llm-auto 규칙의
    path_pattern 을 sample_urls 기반으로 정밀화 (이슈 #173).
  • Classifier (internal/classifier) — gRPC 기본 + HTTP fallback 으로 ELArchive Classifier
    호출 — 향후 enrich 단계에서 사용.
```

<br>

## Kafka 토픽 인덱스

토픽명 상수 정의는 [`pkg/queue/config.go`](../../pkg/queue/config.go) 단일 소스. 자세한 partitioning
전략은 [01-architecture.md](../../.claude/rules/01-architecture.md) 참조.

| Topic                              | Producer                                              | Consumer Group                              |
|------------------------------------|-------------------------------------------------------|---------------------------------------------|
| `issuetracker.crawl.high`          | [scheduler](../../internal/scheduler/) / publisher    | `issuetracker-crawler-workers` (`GroupCrawlerWorkers`) — 3 토픽 단일 그룹 |
| `issuetracker.crawl.normal`        | [scheduler](../../internal/scheduler/) / publisher    | `issuetracker-crawler-workers` (동일)                                    |
| `issuetracker.crawl.low`           | [scheduler](../../internal/scheduler/) / publisher    | `issuetracker-crawler-workers` (동일)                                    |
| `issuetracker.fetched`             | [processor/fetcher/worker](../../internal/processor/fetcher/worker/)      | `issuetracker-parsers` (`GroupParsers`)                                   |
| `issuetracker.normalized`          | [processor/parser/worker](../../internal/processor/parser/worker/)        | `issuetracker-validators` (`GroupValidators`)                             |
| `issuetracker.validated`           | [processor/validate](../../internal/processor/validate/) | (downstream — TBD)                                                    |
| `issuetracker.dlq`                 | 모든 stage 실패 분기                                  | (운영 모니터링)                                                            |

> 3-tier priority topic 은 **단일 consumer group** 을 공유합니다 — [PoolManager](../../internal/processor/fetcher/worker/manager.go) 가 priority 별
> Consumer 인스턴스 3개를 같은 group ID 로 띄워, Kafka 가 파티션을 자동 분배합니다.
> 상수 정의는 [`pkg/queue/config.go`](../../pkg/queue/config.go) 단일 소스.

<br>

## 외부 의존성 인덱스

| 외부 시스템                | 어디서 사용                                                 | 용도                                          |
|----------------------------|------------------------------------------------------------|-----------------------------------------------|
| Kafka                      | [pkg/queue/](../../pkg/queue/)                              | 모든 stage 간 메시지 버스                      |
| PostgreSQL                 | [internal/storage/postgres/](../../internal/storage/postgres/) | contents / content_bodies / content_meta / raw_contents / parsing_rules / sample_urls / schema_migrations |
| Redis                      | [pkg/redis/](../../pkg/redis/), [internal/locks/](../../internal/locks/), [internal/processor/fetcher/worker/](../../internal/processor/fetcher/worker/) | ProcessingLock + IngestionLock (locks) / RetryQueue ZSET (processor/fetcher/worker) |
| LLM (Gemini/OpenAI/Claude) | [pkg/llm/](../../pkg/llm/)                                  | parser rule 자동 생성 / path_pattern refinement |
| Chrome (CDP)               | [internal/processor/fetcher/implementation/chromedp/](../../internal/processor/fetcher/implementation/chromedp/) | 동적 페이지 헤드리스 렌더                      |
| ELArchive Classifier       | [internal/classifier/](../../internal/classifier/) + [proto/classifier/](../../proto/classifier/) | 카테고리 분류 (gRPC primary, HTTP fallback)    |
| Prometheus                 | [pkg/metrics/](../../pkg/metrics/)                          | `/metrics` 엔드포인트                          |

<br>

## DB 테이블 인덱스

스키마는 [`migrations/`](../../migrations/) 가 단일 소스. 각 repo 의 자세한 스키마는
[storage/postgres.md](internal/storage/postgres.md) 에서 확인.

| 테이블             | Repository                                                       | 설명                                                 |
|--------------------|------------------------------------------------------------------|------------------------------------------------------|
| `contents`         | [ContentRepository](../../internal/storage/content.go)           | 정규화된 Content 메타 (id, url, title, source 등)   |
| `content_bodies`   | (same)                                                           | 본문 분리 저장 (큰 텍스트)                            |
| `content_meta`     | (same)                                                           | validation_status, classifier 결과 등 메타           |
| `raw_contents`     | [RawContentRepository](../../internal/storage/raw_content.go)    | Claim Check — 임시 HTML 저장 후 parser 가 정리       |
| `parsing_rules`    | [ParsingRuleRepository](../../internal/storage/parsing_rule.go)  | host_pattern + path_pattern → SelectorMap (이슈 #100) |
| `sample_urls`      | [SampleURLRepository](../../internal/storage/sample_url.go)      | refiner 가 path_pattern 정밀화에 사용 (이슈 #173)    |
| `schema_migrations`| (migrations 자체 관리)                                            | 마이그레이션 버전 추적                                |

<br>

## 읽는 순서 (권장)

처음 합류한 사람 기준:

1. [docs/architecture/cmd/issuetracker.md](cmd/issuetracker.md) — `main()` 가 어떤 모듈을 어떤 순서로 wire 하는지
2. [docs/architecture/internal/processor/fetcher/worker.md](internal/processor/fetcher/worker.md) — Kafka consumer + lock + retry 의 핵심 패턴
3. [docs/architecture/internal/processor/parser/README.md](internal/processor/parser/README.md) — Claim Check + rule-driven 파싱
4. [docs/architecture/internal/storage/README.md](internal/storage/README.md) — Repository → Service 분리 원칙
5. [docs/architecture/pkg/queue.md](pkg/queue.md) — 토픽 상수와 Kafka producer/consumer 추상

<br>

## 문서 작성 규약

- **언어**: 한국어 본문 + 영어 기술용어 ([06-code-style.md](../../.claude/rules/06-code-style.md))
- **링크**: 마크다운 파일 위치 기준 상대경로 — 모든 코드 참조는 프로젝트 디렉토리 안에 머무름
- **다이어그램**: ASCII (Mermaid 미사용 — 의존성 없는 렌더링 보장)
- **이슈 참조**: 관련 이슈 번호를 본문에 인라인 표기 (예: 이슈 #173)
- **갱신**: 패키지 구조가 바뀌면 해당 마크다운만 갱신, 다른 곳에 중복 기술하지 않음
