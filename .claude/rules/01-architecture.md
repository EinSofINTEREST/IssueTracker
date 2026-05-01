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
  - 92.1% test coverage
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
│   ├── api/                   # ✅ HTTP API server → bin/api
│   │   └── main.go
│   ├── processor/             # ✅ Validator-only standalone → bin/processor
│   │   └── main.go
│   ├── issuetracker/          # ✅ Fetcher + Parser + Validator combined → bin/issuetracker
│   │   └── main.go
│   ├── migrate/               # ✅ DB migration (up) → bin/migrate
│   │   └── main.go
│   ├── migrate-down/          # ✅ DB migration (down) → bin/migrate-down
│   │   └── main.go
│   └── rldebug/               # ✅ Rate limiter debug 도구 → bin/rldebug
│       └── main.go
│
├── internal/                   # Private application code
│   ├── parser/                # ✅ Domain-agnostic parser + DB-driven rule engine (이슈 #100, #196)
│   │   ├── parser.go          # ContentParser / LinkListParser interfaces + Page model
│   │   ├── rule/              # parsing_rules 기반 단일 engine
│   │   │   ├── llmgen/        # LLM 기반 selector 자동 생성 (이슈 #149)
│   │   │   ├── pathinfer/     # path_pattern 추론 알고리즘 (이슈 #173)
│   │   │   └── refiner/       # path_pattern 정밀화 polling
│   │   └── worker/            # Claim Check 기반 ParserWorker (Kafka consumer)
│   ├── locks/                 # ✅ 단계 무관 distributed lock — fetcher/parser/validator 공유 (이슈 #197)
│   │   ├── ingestion_lock.go  # IngestionLock (Publisher 가 Kafka enqueue 직전 사용)
│   │   └── processing_lock.go # ProcessingLock + ProcessingKey(stage, url)
│   ├── processor/             # ✅ 파이프라인 단계별 정렬 (이슈 #195)
│   │   ├── fetcher/           # ✅ Web fetch + DB-driven parse 라우팅 + worker pool (이슈 #198)
│   │   │   ├── core/          # 인터페이스 + 모델 + 에러 + HTTP client + retry
│   │   │   ├── handler/       # crawler_name → Handler registry
│   │   │   ├── implementation/# chromedp / goquery 구현체
│   │   │   ├── domain/        # 사이트 chain handler + 사이트별 등록 (sources/)
│   │   │   ├── rate_limiter/  # IP 단위 token bucket
│   │   │   └── worker/        # PoolManager + KafkaConsumerPool + RetryScheduler + CircuitBreaker
│   │   └── validate/          # Validate worker (Validation logic)
│   ├── embedding/             # Embedding & ML (planned)
│   │   ├── model/             # Embedding models
│   │   ├── cluster/           # Clustering logic
│   │   └── index/             # Vector indexing
│   └── storage/               # Storage layer (planned)
│       ├── repository/        # Data access layer
│       └── models/            # Domain models
│
├── pkg/                        # Public library code
│   ├── logger/                # ✅ Reusable logger package
│   │   └── logger.go
│   ├── http/                  # HTTP utilities (planned)
│   ├── queue/                 # Queue abstractions (planned)
│   └── config/                # Configuration (planned)
│
├── test/                       # Test files (mirrors service architecture)
│   ├── internal/              # ✅ internal/ 패키지 테스트
│   │   ├── classifier/        # ← internal/classifier/
│   │   ├── locks/             # ← internal/locks/
│   │   ├── parser/            # ← internal/parser/
│   │   ├── processor/         # ← internal/processor/ (fetcher/{core,worker,...} + validate)
│   │   └── storage/           # ← internal/storage/
│   └── pkg/                   # ✅ pkg/ 패키지 테스트
│       ├── config/            # ← pkg/config/
│       └── logger/            # ← pkg/logger/
│
├── examples/                   # Usage examples
│   └── basic_usage.go         # ✅ Basic usage example
│
├── configs/                    # Configuration files (planned)
├── scripts/                    # Build and deployment scripts (planned)
├── deployments/                # Deployment configurations (planned)
│   └── docker/
│
├── docs/                       # Documentation (planned)
│   ├── en/                    # English docs
│   └── ko/                    # Korean docs
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
- **Language**: Go 1.22.2
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

### Message Queue (Planned)
- **Queue**: Apache Kafka 3.5+
- **Use Cases**: Async processing, job distribution
- **Topics**: `issuetracker.raw.{country}`, `issuetracker.normalized`, etc.

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
Source → Fetch → [Kafka: raw] → Normalize → [Kafka: normalized] → Validate → [Kafka: validated]
                                                                                        ↓
[Kafka: clusters] ← Cluster ← [Kafka: embedded] ← Embed ← [Kafka: enriched] ← Enrich ←┘
         ↓
    Store Processed
```

### Stage Definitions

1. **Fetch**: Retrieve content from source
2. **Kafka (raw)**: Publish raw content to country-specific topic (`issuetracker.raw.{country}`)
3. **Normalize**: Convert to common schema, clean text
4. **Kafka (normalized)**: Publish normalized articles (`issuetracker.normalized`)
5. **Validate**: Check data integrity and quality
6. **Kafka (validated)**: Publish validated articles (`issuetracker.validated`)
7. **Enrich**: Extract entities, sentiment, topics
8. **Kafka (enriched)**: Publish enriched articles (`issuetracker.enriched`)
9. **Embed**: Generate vector representations
10. **Kafka (embedded)**: Publish embedded articles (`issuetracker.embedded`)
11. **Cluster**: Group similar issues using streaming/batch processing
12. **Kafka (clusters)**: Publish identified issue clusters (`issuetracker.clusters`)
13. **Store Processed**: Persist analysis results to databases

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
    raw_us: "issuetracker.raw.us"
    raw_kr: "issuetracker.raw.kr"
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

- **Raw topics** (`issuetracker.raw.*`): 16 partitions per country
- **Processing topics**: 32 partitions for high throughput
- **DLQ topic**: 8 partitions (low volume expected)
- **Partition key**: Use domain/source for ordering within same source
- **Rebalancing**: Minimal impact with proper consumer group size
