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

## Directory Structure

```
ecoscrapper/
├── cmd/
│   ├── crawler/          # Main crawler entry point
│   ├── processor/        # Processing pipeline
│   └── scheduler/        # Job scheduling
├── pkg/
│   ├── crawler/
│   │   ├── core/        # Core crawler interfaces
│   │   ├── news/        # News source crawlers
│   │   ├── community/   # Community crawlers
│   │   └── social/      # Social media crawlers
│   ├── processor/
│   │   ├── normalize/   # Data normalization
│   │   ├── enrich/      # Data enrichment
│   │   └── validate/    # Validation logic
│   ├── embedding/
│   │   ├── model/       # Embedding models
│   │   ├── cluster/     # Clustering logic
│   │   └── index/       # Vector indexing
│   ├── storage/
│   │   ├── repository/  # Data access layer
│   │   └── models/      # Domain models
│   └── config/          # Configuration
├── common/
│   ├── http/            # HTTP utilities
│   ├── queue/           # Queue abstractions
│   └── logger/          # Logging utilities
├── configs/             # Configuration files
├── migrations/          # Database migrations
└── scripts/             # Utility scripts
```

## Technology Stack Requirements

### Core
- **Language**: Go 1.21+
- **Concurrency**: goroutines with worker pools
- **HTTP Client**: Custom client with retry, rate limiting, timeout

### Storage
- **Primary DB**: PostgreSQL (structured data)
- **Vector DB**: PostgreSQL (embeddings)
- **Cache**: Redis (rate limiting, deduplication)
- **Object Storage**: S3-compatible (raw HTML, media)

### Message Queue
- **Queue**: Apache Kafka
- **Use Cases**: Async processing, job distribution

### Observability
- **Metrics**: Prometheus
- **Logging**: Structured logging (zerolog or zap)
- **Tracing**: OpenTelemetry
- **Monitoring**: Grafana dashboards

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
2. **Kafka (raw)**: Publish raw content to country-specific topic (`ecoscrapper.raw.{country}`)
3. **Normalize**: Convert to common schema, clean text
4. **Kafka (normalized)**: Publish normalized articles (`ecoscrapper.normalized`)
5. **Validate**: Check data integrity and quality
6. **Kafka (validated)**: Publish validated articles (`ecoscrapper.validated`)
7. **Enrich**: Extract entities, sentiment, topics
8. **Kafka (enriched)**: Publish enriched articles (`ecoscrapper.enriched`)
9. **Embed**: Generate vector representations
10. **Kafka (embedded)**: Publish embedded articles (`ecoscrapper.embedded`)
11. **Cluster**: Group similar issues using streaming/batch processing
12. **Kafka (clusters)**: Publish identified issue clusters (`ecoscrapper.clusters`)
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
          topic: "ecoscrapper.raw.us"
          partition_key: "domain"
    communities:
      - name: "reddit"
        enabled: true
        subreddits: ["news", "worldnews"]
        kafka:
          topic: "ecoscrapper.raw.us"
          partition_key: "subreddit"
  kr:
    news:
      - name: "naver"
        enabled: true
        rate_limit: 200/hour
        kafka:
          topic: "ecoscrapper.raw.kr"
          partition_key: "domain"

kafka:
  brokers:
    - "kafka-1.example.com:9092"
    - "kafka-2.example.com:9092"
    - "kafka-3.example.com:9092"

  topics:
    # Crawl job topics (by priority)
    crawl_high: "ecoscrapper.crawl.high"
    crawl_normal: "ecoscrapper.crawl.normal"
    crawl_low: "ecoscrapper.crawl.low"

    # Processing pipeline topics
    raw_us: "ecoscrapper.raw.us"
    raw_kr: "ecoscrapper.raw.kr"
    normalized: "ecoscrapper.normalized"
    validated: "ecoscrapper.validated"
    enriched: "ecoscrapper.enriched"
    embedded: "ecoscrapper.embedded"
    clusters: "ecoscrapper.clusters"

    # System topics
    dlq: "ecoscrapper.dlq"

  consumer_groups:
    crawler_workers: "ecoscrapper-crawler-workers"
    normalizers: "ecoscrapper-normalizers"
    validators: "ecoscrapper-validators"
    enrichers: "ecoscrapper-enrichers"
    embedders: "ecoscrapper-embedders"
    clusterers: "ecoscrapper-clusterers"

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
   - Example: Add more embedders if `ecoscrapper.enriched` lag increases

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

- **Raw topics** (`ecoscrapper.raw.*`): 16 partitions per country
- **Processing topics**: 32 partitions for high throughput
- **DLQ topic**: 8 partitions (low volume expected)
- **Partition key**: Use domain/source for ordering within same source
- **Rebalancing**: Minimal impact with proper consumer group size
