# Architecture and Design Principles

## Core Architecture

### System Overview
- **Purpose**: Global issue aggregation and clustering system
- **Initial Scope**: US and South Korea
- **Design Philosophy**: Extensible, scalable, maintainable

### Architectural Layers

```
┌─────────────────────────────────────────┐
│     API / Job Scheduler Layer           │
├─────────────────────────────────────────┤
│     Crawler Orchestration Layer         │
├─────────────────────────────────────────┤
│     Source-Specific Crawlers            │
│  (News, Community, Social Media)        │
├─────────────────────────────────────────┤
│     Data Processing Pipeline            │
│  (Normalize, Validate, Enrich)          │
├─────────────────────────────────────────┤
│     Embedding & ML Layer                │
│  (Vectorize, Cluster, Classify)         │
├─────────────────────────────────────────┤
│     Storage Layer                       │
│  (Raw, Processed, Embeddings)           │
└─────────────────────────────────────────┘
```

## Design Principles

### 1. Modularity
- Each data source MUST be implemented as a separate module
- Use interface-based design for all crawlers
- Implement plugin architecture for adding new sources

### 2. Scalability
- Design for horizontal scaling from day one
- Use message queues for async processing
- Implement rate limiting per source
- Support distributed crawling

### 3. Extensibility
- Country-agnostic design with locale support
- Easy addition of new data sources
- Configurable processing pipelines
- Plugin-based embedding strategies

### 4. Data Quality
- Implement strict validation at ingestion
- Maintain data lineage and provenance
- Support duplicate detection
- Version all data schemas

## Current Implementation Status

### ✅ Completed (v0.2.0)
- **Core Crawler Infrastructure**
  - Crawler interface design
  - HTTP client with connection pooling
  - Token bucket rate limiter
  - Retry logic with exponential backoff
  - Comprehensive error handling
  - Structured logging with zerolog
  - Context-aware logging
  - 테스트 커버리지 — CI 게이트 40% (`ci-quality.yml`). 실측값은 PR Summary 참조
  - Standard Go project layout
  - Makefile build automation
  - Command-line entry points

### 🚧 In Progress
- Source-specific crawler implementations (CNN, Naver, etc.)
- Kafka integration for job distribution
- Processing pipeline setup

### 📋 Planned
- Embedding generation (OpenAI, multilingual-e5)
- Clustering algorithms (HDBSCAN)
- API endpoints (REST/GraphQL)
- Web dashboard (monitoring, analytics)
- Database integration (PostgreSQL, Redis)
- Deployment configurations (Docker, K8s)

## Directory Structure

Following [Standard Go Project Layout](https://github.com/golang-standards/project-layout):

```
issuetracker/
├── cmd/                        # Application entry points (all build to bin/)
│   ├── issuetracker/          # ✅ Fetcher + Parser + Validate + Enrich + Scheduler → bin/issuetracker
│   │   └── main.go
│   ├── processor/             # ✅ Validator-only standalone → bin/processor
│   │   └── main.go
│   ├── migrate/               # ✅ DB migration (up) → bin/migrate
│   │   └── main.go
│   ├── migrate-down/          # ✅ DB migration (down) → bin/migrate-down
│   │   └── main.go
│   ├── rule-validator/        # ✅ parsing rule 검증 도구 → bin/rule-validator
│   │   └── main.go
│   └── admin/                 # ✅ 운영자 도구 — 진입 마커 무효화 / 강제 재크롤 / DLQ 확인 (이슈 #542)
│       └── main.go
│   # HTTP API server (이슈 #21) 는 미구현 — cmd/api 없음
│
├── internal/                   # Private application code
│   ├── bus/                   # ✅ Publisher / Consumer / RetryScheduler — Kafka I/O 단일 출처 (이슈 #385)
│   ├── workerpool/            # ✅ stage 공용 consumer pool harness (poll → dispatch → commit, 이슈 #403)
│   ├── scheduler/             # ✅ 시드 URL 주기 발행 (scheduler_entries 기반)
│   ├── promptcontract/        # ✅ 전 stage prompt placeholder 계약 집계 (이슈 #539)
│   ├── scoring/               # ✅ host 단위 priority score — High ↔ Normal 자동 분기 (이슈 #382)
│   ├── locks/                 # ✅ 단계 무관 distributed lock — 4 stage 공유 (이슈 #197)
│   │   ├── ingestion_marker.go # IngestionMarker — SET NX 진입 마커 (release 없음, 이슈 #541)
│   │   ├── processing_lock.go # ProcessingLock + ProcessingKey(stage, url)
│   │   ├── semaphore.go       # in-process 동시 슬롯 cap (인스턴스 간 비공유 — 이슈 #545)
│   │   └── stage_gate.go      # Semaphore + ProcessingLock 합성
│   ├── processor/             # ✅ 파이프라인 단계별 정렬 — 모든 stage 가 본 디렉토리 하위 (이슈 #195)
│   │   ├── fetcher/           # ✅ Web fetch + DB-driven parse 라우팅 + worker pool (이슈 #198)
│   │   │   ├── core/          # 인터페이스 + 모델 + 에러 + HTTP client + retry
│   │   │   ├── handler/       # crawler_name → Handler registry
│   │   │   ├── implementation/# chromedp / goquery 구현체
│   │   │   ├── domain/        # 사이트 chain handler + 사이트별 등록 (sources/)
│   │   │   ├── rate_limiter/  # IP 단위 token bucket
│   │   │   └── worker/        # PoolManager + KafkaConsumerPool + RetryScheduler + CircuitBreaker
│   │   ├── parser/            # ✅ Domain-agnostic parser + DB-driven rule engine + ParserWorker (이슈 #100, #196, #204)
│   │   │   ├── stage.go       # ContentParser / LinkListParser interfaces + Page model
│   │   │   ├── rule/          # parsing_rules 기반 단일 engine
│   │   │   │   ├── llmgen/    # LLM 기반 selector 자동 생성 (이슈 #149)
│   │   │   │   ├── pathinfer/ # path_pattern 추론 알고리즘 (이슈 #173)
│   │   │   │   └── refiner/   # path_pattern 정밀화 polling
│   │   │   └── worker/        # Claim Check 기반 ParserWorker (Kafka consumer) + RawContentCleaner
│   │   ├── validate/          # ✅ Validate worker (품질 점수 기반 Validation)
│   │   └── enrich/            # ✅ Enrich worker — extract / cross-verify / context / score (이슈 #445)
│   └── storage/               # ✅ postgres / redis / service / decorator / model
│   # internal/embedding (Embedding & ML) 은 미구현 — 이슈 #17 #18 #20
│
├── pkg/                        # Public library code
│   ├── logger/                # ✅ zerolog wrapper + context 주입
│   ├── config/                # ✅ 환경변수 로딩 (app / llm / runtime / storage)
│   ├── queue/                 # ✅ Kafka producer / consumer + 우선순위 ZSET 큐
│   ├── redis/                 # ✅ client + lock + leader lock + retry ZSET
│   ├── llm/                   # ✅ provider (gemini/openai/anthropic) + prompt loader·계약
│   │   └── prompt/assets/     # 내장 prompt 자산 (parser / enrich)
│   ├── agent/                 # ✅ CLI agent 추상 (Agent) + claude 구현 + MCP dependency
│   ├── links/                 # ✅ URL 정규화 / 링크 추출
│   ├── metrics/               # ✅ Prometheus registry
│   ├── resilience/            # ✅ circuit breaker / failure counter
│   └── urlguard/              # ✅ URL 차단 규칙
│
├── test/                       # Test files (mirrors service architecture)
│   ├── internal/              # ✅ internal/ 패키지 테스트
│   │   ├── locks/             # ← internal/locks/
│   │   ├── scoring/           # ← internal/scoring/
│   │   ├── processor/         # ← internal/processor/ (fetcher/{core,worker,...} + parser/{rule,worker} + validate)
│   │   └── storage/           # ← internal/storage/
│   └── pkg/                   # ✅ pkg/ 패키지 테스트
│       ├── config/            # ← pkg/config/
│       └── logger/            # ← pkg/logger/
│
├── examples/                   # Usage examples
│   ├── basic_usage/           # ✅ 기본 사용 예제
│   ├── crawler_comparison/    # ✅ fetcher 구현체 비교
│   └── kafka_pipeline/        # ✅ Kafka 파이프라인 예제
│
├── configs/                    # Configuration files (planned)
├── scripts/                    # Build and deployment scripts (planned)
├── deployments/                # Deployment configurations (planned)
│   └── docker/
│
├── docs/                       # Documentation
│   ├── architecture/          # ✅ 코드 구조 문서 (cmd / internal / pkg / proto)
│   ├── ci/                    # ✅ CI 운영 규약, status check 단일 소스
│   ├── ko/                    # ✅ 한국어 문서
│   └── en/                    # English docs — 목표, 현재 미존재
│
├── .claude/                    # Claude AI development rules
│   └── rules/
│
├── .cursor/                    # Cursor IDE rules
│   └── rules/
│
├── Makefile                    # ✅ Build automation
├── go.mod                      # ✅ Go module definition
├── go.sum                      # ✅ Dependency checksums
└── README.md                   # ✅ Project documentation
```

### Directory Purposes

**`cmd/`**: Application entry points (main packages)
- Each subdirectory is an executable; all build to `bin/<name>` via `make build`
- Minimal logic, imports from `internal/` and `pkg/`
- New entry points MUST be added to the `build` target in `Makefile` alongside the corresponding `*_BINARY` variable

**`internal/`**: Private application code
- Cannot be imported by external projects
- Core business logic
- Source-specific implementations

**`pkg/`**: Public library code
- Can be imported by external projects
- Reusable, generic utilities
- Well-documented, production-ready

**`test/`**: Test files
- 모든 테스트 파일은 소스 코드와 분리하여 `test/` 아래에만 위치
- **서비스 아키텍처와 동일한 디렉토리 구조**를 따름
  - `internal/<pkg>/` → `test/internal/<pkg>/`
  - `pkg/<pkg>/` → `test/pkg/<pkg>/`
- 패키지 선언은 `package <name>_test` (외부 테스트 패키지)

**Current Status**: ✅ = Implemented, (planned) = To be implemented

## Technology Stack

### Core (✅ Implemented)
- **Language**: Go 1.24 (`go.mod` 의 `go` 지시자가 단일 출처 — CI 는 `go-version-file: go.mod`)
- **HTTP Client**: ✅ Custom client with connection pooling (max 100 idle)
- **Rate Limiting**: ✅ Token bucket algorithm
- **Retry Logic**: ✅ Exponential backoff with configurable policies
- **Logging**: ✅ Structured logging with `zerolog`
- **Testing**: ✅ `testify/assert` for assertions, table-driven tests

### Storage (Planned)
- **Primary DB**: PostgreSQL 15+ (structured data)
- **Vector DB**: Qdrant or pgvector (embeddings)
- **Cache**: Redis 7+ (rate limiting, deduplication)
- **Object Storage**: S3-compatible (raw HTML, media)

### Message Queue (✅ Implemented)
- **Queue**: Apache Kafka (client: `segmentio/kafka-go`, 직접 쓰지 않고 `pkg/queue` 래퍼 경유)
- **Use Cases**: Async processing, job distribution
- **Topics**: 단일 출처는 [`pkg/queue/config.go`](../../pkg/queue/config.go) 의 상수다.
  실제 사용: `issuetracker.crawl.{high,normal,low}` · `issuetracker.crawl.chromedp` ·
  `issuetracker.fetched` · `issuetracker.normalized` · `issuetracker.validated` ·
  `issuetracker.enriched` · `issuetracker.dlq`
- ⚠️ `issuetracker.raw.{country}` 는 **초기 설계안이며 쓰이지 않는다** (이슈 #544 — 외부
  호환을 위해 상수만 deprecated alias 로 유지)

### Observability (Planned)
- **Metrics**: Prometheus
- **Tracing**: OpenTelemetry
- **Monitoring**: Grafana dashboards

### Dependencies (Current)
```go
require (
  github.com/rs/zerolog v1.34.0      // Structured logging
  github.com/stretchr/testify v1.11.1 // Testing assertions
)
```

## Data Flow

```
Scheduler → [crawl.{priority}] → Fetch → [fetched] → Parse → [normalized]
                                                                  ↓
                          (종단) [enriched] ← Enrich ← [validated] ← Validate
```

`[enriched]` 는 **현재 파이프라인의 종단** 이다 — 발행은 하지만 consumer 가 없다.
임베딩 / 클러스터링이 들어오면 소비처가 생긴다 (이슈 #17 / #18 / #20).

### Stage Definitions

1. **Scheduler**: `scheduler_entries` 기준으로 시드 URL 을 주기 발행 → `crawl.{priority}`
2. **Fetch**: 콘텐츠 수집 후 **본문은 `raw_contents` 테이블에 저장** 하고, 토픽에는
   `RawContentRef` (raw_id + url + source_info, < 1KB) 만 발행 — Claim Check 패턴 (이슈 #134)
3. **Kafka (fetched)**: `issuetracker.fetched`
4. **Parse**: `raw_contents` 에서 본문을 로드해 DB-driven 룰로 파싱 → `Content` 생성
5. **Kafka (normalized)**: `issuetracker.normalized` — **이름과 역할이 다르다.** 별도의
   normalize stage 는 없으며 parser 산출물이 여기로 간다. 운영 중 토픽이라 개명하지 않는다
6. **Validate**: 품질 점수 기반 검증. 실패 시 재학습 cycle (이슈 #363~#366) 또는 DLQ
7. **Kafka (validated)**: `issuetracker.validated`
8. **Enrich**: extract / cross-verify / context / score (이슈 #445)
9. **Kafka (enriched)**: `issuetracker.enriched` — **종단**
10. **DLQ**: 처리 불가 메시지는 `issuetracker.dlq` 로 격리 (이슈 #542 / #559)

**미구현 (계획):** Embed (#17) → `embedded` · Cluster (#18) → `clusters` · Vector DB (#20).
해당 토픽 상수는 이름 규약 고정용으로만 존재하며 발행·소비 코드가 없다.

### Kafka-Based Processing Benefits

1. **Decoupling**: Each processing stage is independent
2. **Scalability**: Scale each stage independently based on consumer lag
3. **Reliability**: Message persistence and replay capability
4. **Observability**: Track data flow through topics and consumer groups
5. **Flexibility**: Easy to add new processing stages or consumers

## Configuration Strategy

### Multi-Environment Support
- Development, Staging, Production configs
- Environment variable overrides
- Secrets management (Vault or similar)

### Source Configuration

> ⚠️ **아래 블록은 목표 형태의 예시이며 실재하지 않습니다.** 현재 소스는 YAML 이 아니라
> **DB row** 로 관리합니다 (`fetcher_rules` / `parsing_rules` / `scheduler_entries` —
> 이슈 #198). 새 사이트 추가는 Go 코드도 YAML 도 아닌 DB row + (필요 시) LLM 룰 생성입니다.
> 또한 소스별 토픽 매핑은 없습니다 — 모든 fetch 결과가 `issuetracker.fetched` 하나로 갑니다.

```yaml
sources:
  us:
    news:
      - name: "cnn"
        enabled: true
        rate_limit: 100/hour
        selectors: "..."
        kafka:
          topic: "issuetracker.raw.us"
          partition_key: "domain"
    communities:
      - name: "reddit"
        enabled: true
        subreddits: ["news", "worldnews"]
        kafka:
          topic: "issuetracker.raw.us"
          partition_key: "subreddit"
  kr:
    news:
      - name: "naver"
        enabled: true
        rate_limit: 200/hour
        kafka:
          topic: "issuetracker.raw.kr"
          partition_key: "domain"

kafka:
  brokers:
    - "kafka-1.example.com:9092"
    - "kafka-2.example.com:9092"
    - "kafka-3.example.com:9092"

  topics:
    # Crawl job topics (by priority)
    crawl_high: "issuetracker.crawl.high"
    crawl_normal: "issuetracker.crawl.normal"
    crawl_low: "issuetracker.crawl.low"

    # Processing pipeline topics
    # (raw_us / raw_kr 은 초기 설계안 — 실제로는 fetched 단일 토픽. 이슈 #544)
    fetched: "issuetracker.fetched"
    normalized: "issuetracker.normalized"
    validated: "issuetracker.validated"
    enriched: "issuetracker.enriched"
    embedded: "issuetracker.embedded"
    clusters: "issuetracker.clusters"

    # System topics
    dlq: "issuetracker.dlq"

  consumer_groups:
    crawler_workers: "issuetracker-crawler-workers"
    normalizers: "issuetracker-normalizers"
    validators: "issuetracker-validators"
    enrichers: "issuetracker-enrichers"
    embedders: "issuetracker-embedders"
    clusterers: "issuetracker-clusterers"

  # Topic configurations
  topic_configs:
    default_partitions: 16
    default_replication_factor: 3
    retention_ms: 86400000  # 24 hours for most topics
```

## Scalability Considerations

### Horizontal Scaling with Kafka

1. **Crawler Layer**
   - Stateless crawler instances
   - Multiple instances per consumer group
   - Kafka handles load balancing across instances
   - Scale by adding more instances to consumer group

2. **Processing Pipeline**
   - Each stage scales independently
   - Monitor consumer lag per topic
   - Auto-scale based on lag threshold
   - Example: Add more embedders if `issuetracker.enriched` lag increases

3. **Kafka Cluster**
   - 3+ broker cluster for production
   - Partition topics for parallelism
   - Replication factor 3 for reliability
   - Use Kafka's rack awareness for availability

### Performance Targets

1. **Throughput**
   - Process 10,000+ articles/hour per crawler instance
   - 50,000+ messages/second per Kafka cluster
   - Embedding latency < 100ms per document
   - End-to-end pipeline latency < 5 minutes (p95)

2. **Availability**
   - 99.9% uptime for critical crawlers
   - Zero data loss (Kafka replication)
   - Graceful degradation on component failure

3. **Scalability Metrics**
   - Consumer lag < 1000 messages per partition
   - Message processing rate > arrival rate
   - Database write throughput > 1000 ops/sec

### Resource Management

1. **Crawler Instances**
   - Memory limit: 512MB per instance
   - CPU throttling for non-critical sources
   - Concurrent connection limit per domain

2. **Kafka Resources**
   - Broker heap: 4-8GB
   - Disk: SSD for performance, tiered storage for retention
   - Network: 10Gbps recommended

3. **Processing Workers**
   - Memory limit based on stage:
     - Normalizer/Validator: 256MB
     - Enricher: 512MB
     - Embedder: 1GB (model loading)
     - Clusterer: 2GB (in-memory clustering)

4. **Monitoring**
   - Disk space monitoring and cleanup
   - Kafka broker disk usage alerts
   - Consumer group lag monitoring
   - Auto-scaling triggers based on metrics

### Kafka Partition Strategy

- **Crawl job topics** (`issuetracker.crawl.*`): 우선순위별 분리. 파티션 수는
  `KAFKA_PARTITIONS_{HIGH,NORMAL,LOW,CHROMEDP}` 로 설정 (`.env.example` 참조)
- **Processing topics** (`fetched` / `normalized` / `validated` / `enriched`): 처리량에 맞춰 확장
- ~~**Raw topics** (`issuetracker.raw.*`)~~ — 쓰이지 않음 (이슈 #544)
- **DLQ topic**: 8 partitions (low volume expected)
- **Partition key**: Use domain/source for ordering within same source
- **Rebalancing**: Minimal impact with proper consumer group size
