# IssueTracker

**[한국어](docs/ko/README.md)** | English

> Global Issue Collection and Analysis System

[![Go Version](https://img.shields.io/badge/Go-1.22+-00ADD8?style=flat&logo=go)](https://go.dev/)
[![Test Coverage](https://img.shields.io/badge/coverage-92.1%25-brightgreen)](./coverage.out)
[![License](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

## Overview

IssueTracker is a scalable, extensible system designed to crawl news, social media, and community sources worldwide, process and normalize multilingual content, and identify major issues through embedding and clustering.

**Initial Target Markets**: United States and South Korea

## Features

- 🌐 **Multi-source Crawling**: Support for news sites, RSS feeds, APIs, and community platforms
- 🔄 **Kafka-based Pipeline**: Distributed, asynchronous processing with fault tolerance
- 🧠 **ML-powered Analysis**: Embedding generation and clustering for issue detection
- 🌍 **Multilingual Support**: Process content in multiple languages (English, Korean)
- 📊 **Real-time Monitoring**: Prometheus metrics and health checks
- ✅ **Production-ready**: Comprehensive error handling, retry logic, and rate limiting
- 🏗️ **Standard Go Layout**: Following golang-standards/project-layout

## Architecture

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

## Quick Start

### Prerequisites

- Go 1.21+
- PostgreSQL 15+
- Apache Kafka 3.5+
- Redis 7+

### Installation

```bash
git clone https://github.com/yourusername/issuetracker
cd issuetracker
go mod tidy
```

### Building

```bash
# Build all binaries (crawler, processor, issuetracker)
make build

# Or build individual binaries
go build -o bin/crawler ./cmd/crawler
go build -o bin/processor ./cmd/processor
go build -o bin/issuetracker ./cmd/issuetracker
```

### Kafka Setup

```bash
# Start Kafka broker + UI (localhost:9092, UI: http://localhost:8080)
make kafka-start

# Stop (data preserved)
make kafka-stop

# Stop + delete all data
make kafka-clean
```

**Partition configuration** — copy `.env.example` and adjust before starting:

```bash
cp deployments/docker/.env.example deployments/docker/.env
# Edit: KAFKA_PARTITIONS_HIGH, KAFKA_PARTITIONS_NORMAL, KAFKA_PARTITIONS_LOW
make kafka-start
```

### Running

```bash
# Start Kafka first
make kafka-start

# Run crawler only (connects to localhost:9092, subscribes to crawl.high/normal/low)
make run-crawler

# Run validate processor only (subscribes to issuetracker.normalized)
make run-processor

# Run crawler + processor together (single process)
make run-issuetracker

# Run Kafka pipeline example (in-memory mock, no Kafka required)
make run-kafka-pipeline

# Run basic example
make run-example

# Run binaries directly
./bin/crawler
./bin/processor
./bin/issuetracker
```

**Binary summary:**

| Binary | Entry Point | Description |
|--------|-------------|-------------|
| `bin/crawler` | `cmd/crawler/` | Crawler pool manager only |
| `bin/processor` | `cmd/processor/` | Validate worker only |
| `bin/issuetracker` | `cmd/issuetracker/` | Crawler + Processor combined |

### Database Setup

The project uses PostgreSQL with pgx/v5 driver for data persistence.

```bash
# Start PostgreSQL container
make pg-start

# Check PostgreSQL status
make pg-status

# Run migrations (idempotent, safe to run multiple times)
make pg-migrate

# Rollback migrations (use with caution in production)
make pg-migrate-down

# Connect to PostgreSQL shell
make pg-psql
```

**Migrations**:
- `001_create_raw_contents.up.sql` — Store RawContent (original HTML)
- `002_create_contents.up.sql` — Store normalized Content
- `003_create_news_articles.up.sql` — Store NewsArticle (title, body, author, category, tags, image_urls, published_at)

### Testing

```bash
# Run all tests
make test

# Run with verbose output
make test-verbose

# Check coverage
make coverage

# Generate HTML coverage report
make coverage-html
```

### Development

```bash
# Format code
make fmt

# Run linter
make lint

# Clean build artifacts
make clean

# Update dependencies
make deps
```

## Current Status

✅ **Core Crawler Infrastructure** (v0.2.0)
- [x] Core interfaces and data models
- [x] HTTP client with connection pooling
- [x] Token bucket rate limiter
- [x] Retry logic with exponential backoff
- [x] Comprehensive error handling
- [x] Structured logging with zerolog
- [x] Context-aware logging
- [x] 92.1% test coverage
- [x] Standard Go project layout
- [x] Makefile for build automation
- [x] cmd/ entry points

✅ **Kafka Integration** (v0.3.0)
- [x] Producer / Consumer interface abstraction (`pkg/queue`)
- [x] KafkaConsumerPool — priority-based multi-worker goroutine pool
- [x] Handler Registry — crawler name → handler dispatch
- [x] DLQ (Dead Letter Queue) with retry-count-based routing
- [x] Docker Compose Kafka stack (KRaft mode, no Zookeeper)
- [x] Kafka topic initialization with configurable partitions via `.env`
- [x] Kafka UI at `http://localhost:8080`
- [x] Priority-based multi-pool manager (`PoolManager`)

✅ **News Domain Crawlers** (v0.4.0)
- [x] News domain DIP interfaces (`internal/crawler/domain/news/news.go`)
- [x] Chain of Responsibility handler (`handler.go`) — RSS → GoQuery → Browser fallback chain
- [x] RSS/GoQuery/Browser adapters (`fetcher/`)
- [x] **Korean Sources**:
  - [x] Naver crawler (GoQuery → Browser fallback) + Parser
  - [x] Yonhap crawler (GoQuery) + Parser
  - [x] Daum crawler (GoQuery) + Parser
  - [x] KR registry assembly entry point (`kr/registry.go`)
- [x] **US Sources**:
  - [x] CNN crawler (GoQuery) + Parser
  - [x] US registry assembly entry point (`us/registry.go`)
- [x] PostgreSQL storage layer (`internal/storage/news_article.go`, `postgres/news_article.go`)
- [x] Migration — `news_articles` table creation (`003_create_news_articles.up.sql`)
- [x] Tests — 780+ cases for KR parser/crawler, 519+ cases for US parser/crawler

✅ **Kafka Blob Offloading** (v0.5.0)
- [x] `RawContentRef` — lightweight Kafka message struct (ID + metadata, no HTML body)
- [x] `RawContentService` injected into `KafkaConsumerPool` for Postgres-first storage
- [x] Worker saves full `RawContent` (including HTML) to Postgres, then publishes only `RawContentRef` to Kafka
- [x] Duplicate URL handling — returns existing record ID without error
- [x] `PoolManager` (`manager.go`) — priority-based multi-pool orchestration (High / Normal / Low)
- [x] Unit tests for pool processing logic (`test/internal/worker/`)

✅ **Content Validation Pipeline** (v0.6.0)
- [x] `ContentProcessor` interface — common pipeline stage interface (`internal/processor/processor.go`)
- [x] `Validator` interface + `ValidationResult` / `ValidationError` types — shared validation contract
- [x] `NewValidator` factory — `SourceType` 기반 디스패치 (news / community)
- [x] News validator (`validate/news/`) — Title 10~500자, Body 100~50,000자, PublishedAt 필수, 품질 점수 산출, 스팸(대문자·구두점 과용) 탐지
- [x] Community validator (`validate/community/`) — Body 50자 이상, PublishedAt 선택, 반복 문자·도배 패턴 탐지
- [x] `NewContentProcessor` adapter — Validator → ContentProcessor 어댑팅, Reliability 필드 업데이트
- [x] Validate `Worker` — Kafka Consumer/Producer 기반 워커 (수동 커밋, retry count 기반 DLQ 라우팅)
- [x] Unit tests — 89.1% coverage (`test/internal/processor/validate/`)
- [x] `Content` struct에 JSON 직렬화 태그 추가 (Kafka `ProcessingMessage.Data` 호환)

📋 **Planned**
- [ ] Normalize processing stage
- [ ] Enrich processing stage (entity extraction, sentiment analysis)
- [ ] Embedding generation
- [ ] Clustering algorithms
- [ ] API endpoints
- [ ] Web dashboard

## Project Structure

Following the [Standard Go Project Layout](https://github.com/golang-standards/project-layout):

```
issuetracker/
├── cmd/
│   ├── crawler/               # Crawler-only entry point
│   │   └── main.go
│   ├── processor/             # Validate processor entry point
│   │   └── main.go
│   └── issuetracker/          # Crawler + Processor combined entry point
│       └── main.go
│
├── internal/
│   ├── crawler/
│   │   ├── core/              # Crawler interfaces, models, errors, retry
│   │   ├── handler/           # Handler interface + Registry (crawler name dispatch)
│   │   │   ├── handler.go     # Handler interface, Registry
│   │   │   └── noop.go        # Fallback noop handler
│   │   ├── worker/            # Kafka consumer pool
│   │   │   ├── pool.go        # KafkaConsumerPool (goroutine worker pool + DLQ + Postgres offload)
│   │   │   └── manager.go     # PoolManager (High/Normal/Low priority pool orchestration)
│   │   └── domain/
│   │       └── news/          # News domain crawlers (DIP + Chain of Responsibility)
│   │           ├── news.go    # Domain interfaces (NewsFetcher, NewsRSSFetcher, ...)
│   │           ├── handler.go # Chain: RSS → GoQuery → Browser
│   │           ├── fetcher/   # Adapters (rss, goquery, browser)
│   │           ├── kr/        # Korean sources
│   │           │   ├── naver/ # Naver (config, crawler, parser)
│   │           │   ├── yonhap/ # Yonhap (config, crawler, parser)
│   │           │   ├── daum/  # Daum (config, crawler, parser)
│   │           │   └── registry.go # Assembly & registration entry point
│   │           └── us/        # US sources
│   │               ├── cnn/   # CNN (config, crawler, parser)
│   │               └── registry.go # Assembly & registration entry point
│   ├── processor/
│   │   ├── processor.go       # ContentProcessor, Validator, ValidationResult interfaces
│   │   └── validate/          # Content validation stage
│   │       ├── validator.go   # NewValidator factory + NewContentProcessor adapter
│   │       ├── worker.go      # Kafka Worker (normalized → validated, DLQ routing)
│   │       ├── news/          # News-specific validator
│   │       │   └── validator.go # Title/Body length, PublishedAt, spam detection
│   │       └── community/     # Community-specific validator
│   │           └── validator.go # Min body, flood pattern detection
│   └── storage/               # Data access layer
│       ├── news_article.go    # NewsArticle repository interface
│       └── postgres/          # PostgreSQL implementation
│           └── news_article.go # pgx/v5 based NewsArticle CRUD
│
├── pkg/
│   ├── logger/                # Structured logger (zerolog)
│   └── queue/                 # Kafka producer/consumer abstraction
│       ├── queue.go           # Producer, Consumer interfaces
│       ├── config.go          # Topic/group constants, Config
│       ├── producer.go        # KafkaProducer (kafka-go)
│       └── consumer.go        # KafkaConsumer (kafka-go, manual commit)
│
├── deployments/
│   └── docker/
│       ├── docker-compose.yml # Kafka broker (KRaft) + kafka-ui
│       └── .env.example       # Partition configuration template
│
├── examples/
│   ├── basic_usage.go
│   └── kafka_pipeline/        # In-memory mock pipeline example
│
├── migrations/                # Database migrations (PostgreSQL)
│   ├── 001_create_raw_contents.up.sql     # raw_contents table
│   ├── 002_create_contents.up.sql         # contents table
│   ├── 003_create_news_articles.up.sql    # news_articles table + indexes
│   └── *.down.sql             # Rollback migrations
│
├── test/                      # Package-level tests
├── docs/
│   ├── en/
│   └── ko/
├── Makefile
├── go.mod
└── go.sum
```

### Design Rationale

**Standard Go Project Layout:**
- **`cmd/`**: Application entry points (main packages)
- **`internal/`**: Private code (cannot be imported by external projects)
- **`pkg/`**: Public library code (can be imported by external projects)
- Benefits:
  - Industry-standard structure
  - Clear separation of concerns
  - Easy to navigate for Go developers
  - Better dependency management

## Core Components

### Crawler Interface

```go
type Crawler interface {
  Name() string
  Source() SourceInfo
  Initialize(ctx context.Context, config Config) error
  Start(ctx context.Context) error
  Stop(ctx context.Context) error
  Fetch(ctx context.Context, target Target) (*RawContent, error)
  HealthCheck(ctx context.Context) error
}
```

### HTTP Client

- Connection pooling (max 100 idle connections)
- Configurable timeouts
- Response size limiting (10MB)
- Automatic retry on network errors
- Integrated logging

### Rate Limiter

- Token bucket algorithm
- Configurable requests per hour
- Burst support
- Context-aware waiting
- Performance optimized

### Error Handling

- Typed errors with categories
- Retryable vs non-retryable errors
- Error codes for tracking
- Full error context preservation

### Structured Logger

- Built on `zerolog` for high performance
- Context-aware logging
- Multiple log levels (Debug, Info, Warn, Error, Fatal)
- JSON and pretty-print formats
- Request ID tracking
- Crawler-specific fields

## Crawler Implementations

IssueTracker provides two crawlers for static and dynamic pages:

### Goquery - Static Crawling (`implementation/goquery`)

Lightweight crawler for static HTML pages.

```go
crawler := goquery.NewGoqueryCrawler("my-crawler", sourceInfo, config)
raw, err := crawler.Fetch(ctx, target)
article, err := crawler.FetchAndParse(ctx, target, selectors)
```

### Chromedp - Dynamic Crawling (`implementation/chromedp`)

Headless browser crawler for dynamic pages requiring JavaScript rendering.

```go
crawler := chromedp.NewChromedpCrawler("my-crawler", sourceInfo, config)
crawler.Initialize(ctx, config)
defer crawler.Stop(ctx)

raw, err := crawler.Fetch(ctx, target)                       // Rendered HTML
article, err := crawler.FetchAndParse(ctx, target, selectors) // Render + Parse
result, err := crawler.EvaluateJS(ctx, url, "document.title") // Execute JS
```

### Comparison

| | Goquery | Chromedp |
|---|---|---|
| **Purpose** | Static HTML | JavaScript SPA |
| **Speed** | Fast (~1s) | Slow (~3-5s) |
| **Memory** | Low (~10MB) | High (~100MB) |
| **JS Support** | ✗ | ✓ |
| **Use Cases** | News, RSS | Community, SPA |

### Run Example

```bash
make run-comparison
```

## News Domain Crawlers

### Korean Sources

#### Naver
- **Fetcher**: GoQuery → Browser fallback
- **Features**: Category extraction, image URL collection, KST → UTC conversion
- **Date Format**: `"2026-03-02 14:54:16"` (KST)

#### Yonhap
- **Fetcher**: GoQuery
- **Features**: Multiple author extraction, tags/keywords collection, photo gallery support
- **Date Format**: `"2024-01-15 14:30"` (KST)

#### Daum
- **Fetcher**: GoQuery
- **Features**: Category, images, metadata extraction
- **Date Format**: ISO 8601

### US Sources

#### CNN
- **Fetcher**: GoQuery
- **Features**: Section/subsection extraction, byline parsing, metadata support
- **Date Format**: ISO 8601

**All parsers**:
- Extract: Title, Body, Author, Category, Tags, ImageURLs, PublishedAt
- Handle missing fields gracefully (fallback to defaults)
- Convert all timestamps to UTC
- 780+ test cases (KR), 519+ test cases (US)

## Development

### Code Style

- **Indentation**: 2 spaces (NOT tabs)
- **Comments**: Korean + English mix
- **Testing**: Minimum 70% coverage (currently 92.1%)
- **Naming**: Clear, self-documenting names

### Running Linters

```bash
# Using Makefile
make lint

# Or directly
golangci-lint run
```

### Documentation

Development rules are located in [`.claude/rules/`](.claude/rules/):

1. **[01-architecture.md](.claude/rules/01-architecture.md)** - System architecture
2. **[02-crawler-implementation.md](.claude/rules/02-crawler-implementation.md)** - Crawler standards
3. **[03-data-processing.md](.claude/rules/03-data-processing.md)** - Processing pipeline
4. **[04-error-handling.md](.claude/rules/04-error-handling.md)** - Error handling & monitoring
5. **[05-testing.md](.claude/rules/05-testing.md)** - Testing strategy
6. **[06-code-style.md](.claude/rules/06-code-style.md)** - Code conventions

## Example Usage

See [examples/basic_usage.go](examples/basic_usage.go) for a complete example:

```go
import (
  "issuetracker/pkg/logger"
  "issuetracker/internal/crawler/core"
)

// Setup logger (development mode with pretty printing)
logConfig := logger.DefaultConfig()
logConfig.Level = logger.LevelDebug
logConfig.Pretty = true
log := logger.New(logConfig)

// Create HTTP client with rate limiting
config := core.DefaultConfig()
config.RequestsPerHour = 60
httpClient := core.NewHTTPClient(config)
rateLimiter := core.NewRateLimiter(config.RequestsPerHour, config.BurstSize)

// Add logger to context
ctx := context.Background()
ctx = log.ToContext(ctx)

// Add request ID for tracing
requestLog := log.WithRequestID("req-123")
requestCtx := requestLog.ToContext(ctx)

// Fetch with retry (logging happens automatically)
var resp *core.HTTPResponse
err := core.WithRetry(requestCtx, core.DefaultRetryPolicy, func() error {
  var fetchErr error
  resp, fetchErr = httpClient.Get(requestCtx, url)
  return fetchErr
})
```

## Makefile Commands

```bash
# Build & Run
make build              # Build all binaries → bin/crawler, bin/processor, bin/issuetracker
make run-crawler        # Build and run crawler only
make run-processor      # Build and run validate processor only
make run-issuetracker   # Build and run crawler + processor combined
make run-example        # Run basic usage example
make run-kafka-pipeline # Run in-memory Kafka pipeline example

# Test & Quality
make test               # Run all tests
make coverage           # Run tests with coverage report
make lint               # Run linters
make fmt                # Format code
make clean              # Remove build artifacts

# Kafka
make kafka-start        # Start Kafka broker + UI (creates topics)
make kafka-stop         # Stop (data preserved at KAFKA_DATA_DIR)
make kafka-clean        # Stop + delete all data
make kafka-status       # Show container status
make kafka-topics       # List all topics
make kafka-describe     # Show partition/leader details per topic
make kafka-scale-partitions TOPIC=<topic> PARTITIONS=<n>  # Increase partitions

# PostgreSQL
make pg-start           # Start PostgreSQL container
make pg-stop            # Stop PostgreSQL (data preserved)
make pg-clean           # Stop + delete all data
make pg-status          # Show PostgreSQL container status
make pg-migrate         # Run database migrations (idempotent)
make pg-migrate-down    # Rollback migrations (use with caution)
make pg-psql            # Connect to PostgreSQL shell

make help               # Show all commands with descriptions
```

## Contributing

Please read the development rules in `.claude/rules/` before contributing.

## License

MIT

## Contact

For questions or feedback, please open an issue on GitHub.
