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

### Running

```bash
# Run crawler
make run-crawler

# Or run example
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
- [x] **Structured logging with zerolog**
- [x] Context-aware logging
- [x] **92.1% test coverage** ⬆️
- [x] **Standard Go project layout** 🆕
- [x] **Makefile for build automation** 🆕
- [x] **cmd/ entry points** 🆕

🚧 **In Progress**
- [ ] Source-specific crawler implementations (CNN, Naver, etc.)
- [ ] Kafka integration
- [ ] Processing pipeline

📋 **Planned**
- [ ] Embedding generation
- [ ] Clustering algorithms
- [ ] API endpoints
- [ ] Web dashboard

## Project Structure

Following the [Standard Go Project Layout](https://github.com/golang-standards/project-layout):

```
ecoscrapper/
├── cmd/                        # Application entry points
│   ├── crawler/               # Crawler executable
│   │   └── main.go
│   ├── processor/             # Processor executable
│   └── api/                   # API server executable
│
├── internal/                   # Private application code
│   └── crawler/
│       ├── core/              # ✅ Core crawler interfaces
│       │   ├── crawler.go     # Crawler interface
│       │   ├── errors.go      # Error types
│       │   ├── models.go      # Data models
│       │   └── retry.go       # Retry policy
│       └── implementation/    # ✅ Crawler implementations
│           ├── goquery/       # Goquery (정적 크롤링)
│           │   ├── types.go
│           │   ├── crawler.go
│           │   ├── fetch.go
│           │   └── parse.go
│           └── chromedp/      # Chromedp (동적 크롤링)
│               ├── types.go
│               ├── crawler.go
│               ├── fetch.go
│               └── parse.go
│
├── pkg/                        # Public library code
│   └── logger/                # ✅ Reusable logger package
│       └── logger.go
│
├── configs/                    # Configuration files
├── scripts/                    # Build and deployment scripts
├── deployments/                # Deployment configurations
│   └── docker/
│
├── test/                       # Additional test files
│   ├── internal_crawler_core/ # Internal crawler tests
│   └── pkg_logger/            # Logger package tests
│
├── examples/                   # Usage examples
│   └── basic_usage.go
│
├── docs/                       # Documentation
│   ├── en/                    # English docs
│   └── ko/                    # Korean docs
│
├── Makefile                    # Build automation
├── go.mod                      # Go module definition
├── go.sum                      # Dependency checksums
└── README.md
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
make help          # Show all available commands
make build         # Build crawler binary
make test          # Run all tests
make coverage      # Run tests with coverage
make coverage-html # Generate HTML coverage report
make lint          # Run linters
make fmt           # Format code
make clean         # Clean build artifacts
make deps          # Update dependencies
make run-crawler   # Run crawler
make run-example   # Run basic example
make run-comparison # Run crawler implementation comparison
```

## Contributing

Please read the development rules in `.claude/rules/` before contributing.

## License

MIT

## Contact

For questions or feedback, please open an issue on GitHub.
