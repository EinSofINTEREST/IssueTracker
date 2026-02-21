# EcoScrapper

**[한국어](docs/ko/README.md)** | English

> Global Issue Collection and Analysis System

[![Go Version](https://img.shields.io/badge/Go-1.22+-00ADD8?style=flat&logo=go)](https://go.dev/)
[![Test Coverage](https://img.shields.io/badge/coverage-92.1%25-brightgreen)](./coverage.out)
[![License](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

## Overview

EcoScrapper is a scalable, extensible system designed to crawl news, social media, and community sources worldwide, process and normalize multilingual content, and identify major issues through embedding and clustering.

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
git clone https://github.com/yourusername/ecoscrapper
cd ecoscrapper
go mod tidy
```

### Building

```bash
# Build all binaries
make build

# Or build specific binary
go build -o bin/crawler ./cmd/crawler
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

# Run crawler (connects to localhost:9092, subscribes to crawl.normal)
make run-crawler

# Run Kafka pipeline example (in-memory mock, no Kafka required)
make run-kafka-pipeline

# Run basic example
make run-example

# Run binary directly
./bin/crawler
```

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

🚧 **In Progress**
- [ ] Source-specific crawler implementations (CNN, Naver, etc.)
- [ ] Priority-based multi-pool manager

📋 **Planned**
- [ ] Processing pipeline (normalize → validate → enrich)
- [ ] Embedding generation
- [ ] Clustering algorithms
- [ ] API endpoints
- [ ] Web dashboard

## Project Structure

Following the [Standard Go Project Layout](https://github.com/golang-standards/project-layout):

```
ecoscrapper/
├── cmd/
│   └── crawler/               # Crawler entry point
│       └── main.go
│
├── internal/
│   └── crawler/
│       ├── core/              # Crawler interfaces, models, errors, retry
│       ├── handler/           # Handler interface + Registry (crawler name dispatch)
│       │   ├── handler.go     # Handler interface, Registry
│       │   └── noop.go        # Fallback noop handler
│       ├── worker/            # Kafka consumer pool
│       │   └── pool.go        # KafkaConsumerPool (goroutine worker pool + DLQ)
│       └── news/              # Source-specific crawlers (planned)
│           ├── us/            # US sources (CNN, NYT, ...)
│           └── kr/            # Korean sources (Naver, Daum, ...)
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

EcoScrapper provides two crawlers for static and dynamic pages:

### Goquery - Static Crawling (`implementation/goquery`)

정적 HTML 페이지를 위한 경량 크롤러입니다.

```go
crawler := goquery.NewGoqueryCrawler("my-crawler", sourceInfo, config)
raw, err := crawler.Fetch(ctx, target)
article, err := crawler.FetchAndParse(ctx, target, selectors)
```

### Chromedp - Dynamic Crawling (`implementation/chromedp`)

JavaScript 렌더링이 필요한 동적 페이지를 위한 헤드리스 브라우저 크롤러입니다.

```go
crawler := chromedp.NewChromedpCrawler("my-crawler", sourceInfo, config)
crawler.Initialize(ctx, config)
defer crawler.Stop(ctx)

raw, err := crawler.Fetch(ctx, target)                       // 렌더링된 HTML
article, err := crawler.FetchAndParse(ctx, target, selectors) // 렌더링 + 파싱
result, err := crawler.EvaluateJS(ctx, url, "document.title") // JS 실행
```

### Comparison

| | Goquery | Chromedp |
|---|---|---|
| **용도** | 정적 HTML | JavaScript SPA |
| **속도** | 빠름 (~1s) | 느림 (~3-5s) |
| **메모리** | 낮음 (~10MB) | 높음 (~100MB) |
| **JS 지원** | X | O |
| **사용 사례** | 뉴스, RSS | 커뮤니티, SPA |

### Run Example

```bash
make run-comparison
```

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
  "ecoscrapper/pkg/logger"
  "ecoscrapper/internal/crawler/core"
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
make build              # Build crawler binary → bin/crawler
make run-crawler        # Build and run crawler
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

make help               # Show all commands with descriptions
```

## Contributing

Please read the development rules in `.claude/rules/` before contributing.

## License

MIT

## Contact

For questions or feedback, please open an issue on GitHub.
