# Project Structure

## Directory Layout

IssueTracker follows the [Standard Go Project Layout](https://github.com/golang-standards/project-layout).

```
issuetracker/
├── cmd/                        # Application entry points
│   ├── crawler/               # Crawler executable
│   │   └── main.go
│   ├── processor/             # Processor executable (planned)
│   └── api/                   # API server executable (planned)
│
├── internal/                   # Private application code
│   └── crawler/
│       └── core/              # ✅ Core crawler implementation
│           ├── crawler.go     # Crawler interface
│           ├── errors.go      # Error types
│           ├── http_client.go # HTTP client
│           ├── models.go      # Data models
│           ├── rate_limiter.go# Rate limiter
│           └── retry.go       # Retry logic
│
├── pkg/                        # Public library code
│   └── logger/                # ✅ Reusable logger package
│       └── logger.go
│
├── test/                       # Test files
│   ├── internal_crawler_core/ # Internal crawler tests
│   └── pkg_logger/            # Logger package tests
│
├── examples/                   # Usage examples
│   └── basic_usage.go
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
│       ├── 01-architecture.md
│       ├── 02-crawler-implementation.md
│       ├── 03-data-processing.md
│       ├── 04-error-handling.md
│       ├── 05-testing.md
│       └── 06-code-style.md
│
├── .cursor/                    # Cursor IDE rules
│   ├── README.md
│   ├── git-conventions.md
│   ├── code-style.md
│   ├── project-structure.md
│   └── development-workflow.md
│
├── Makefile                    # Build automation
├── go.mod                      # Go module definition
├── go.sum                      # Dependency checksums
└── README.md
```

## Directory Purposes

### `/cmd`
**Application entry points (main packages)**

- Each subdirectory represents an executable
- Contains only `main.go` with minimal logic
- Imports and orchestrates from `internal/` and `pkg/`
- Examples: `crawler`, `processor`, `api`

```go
// cmd/crawler/main.go
package main

import (
  "issuetracker/internal/crawler/core"
  "issuetracker/pkg/logger"
)

func main() {
  // Application initialization
}
```

### `/internal`
**Private application code**

- Cannot be imported by external projects
- Contains core business logic
- Each module is isolated
- Examples: `crawler/core`, `processor`, `storage`

**Key Rules:**
- Go compiler prevents imports like `github.com/other/project/internal/crawler`
- Use this for application-specific code
- Well-structured internal packages promote modularity

### `/pkg`
**Public library code**

- Can be imported by external projects
- Contains reusable, generic utilities
- Should have minimal dependencies
- Examples: `logger`, `http`, `queue`

**Key Rules:**
- Code here should be production-ready
- Document all exported functions
- Maintain backward compatibility
- Examples: `issuetracker/pkg/logger`

### `/test`
**Test files**

- All test files separated from source
- Organized by package structure
- Use `*_test` package pattern
- Examples: `internal_crawler_core/`, `pkg_logger/`

**Benefits:**
- Clean separation of concerns
- Easy to exclude from builds
- Clear test organization

### `/examples`
**Usage examples**

- Demonstrate how to use the library
- Runnable code samples
- Good for documentation
- Example: `basic_usage.go`

### `/configs`
**Configuration files**

- YAML/JSON configuration templates
- Environment-specific configs
- Examples: `config.yaml`, `config.prod.yaml`

### `/scripts`
**Build and deployment scripts**

- Build automation
- Database migrations
- Deployment helpers
- Examples: `build.sh`, `migrate.sh`

### `/deployments`
**Deployment configurations**

- Docker files
- Kubernetes manifests
- CI/CD configurations
- Examples: `docker/Dockerfile`, `k8s/deployment.yaml`

### `/docs`
**Documentation**

- English (`en/`) and Korean (`ko/`) versions
- Architecture diagrams
- API documentation
- User guides

### `/.claude`
**Claude AI development rules**

- Architecture guidelines
- Implementation standards
- Testing strategies
- Code conventions

### `/.cursor`
**Cursor IDE rules**

- Git conventions
- Code style guide
- Project structure
- Development workflow

## Import Paths

### Internal Packages
```go
import "issuetracker/internal/crawler/core"
```

- Use for private application code
- Cannot be imported by external projects

### Public Packages
```go
import "issuetracker/pkg/logger"
```

- Use for reusable libraries
- Can be imported by external projects

### External Dependencies
```go
import "github.com/rs/zerolog/log"
```

- Third-party packages
- Managed by `go.mod`

## Module Organization

### Core Principles

1. **Separation of Concerns**
   - Each package has a single responsibility
   - Clear boundaries between modules
   - Minimal coupling

2. **Dependency Direction**
   ```
   cmd/ → internal/ → pkg/
         ↓
   external packages
   ```

3. **Testability**
   - Interface-based design
   - Easy mocking
   - Isolated tests

4. **Extensibility**
   - Plugin architecture for crawlers
   - Easy to add new sources
   - Configuration-driven behavior

## File Naming Conventions

### Source Files
- **Lowercase with underscores**: `http_client.go`
- **Descriptive names**: `rate_limiter.go`, not `rl.go`
- **Implementation-specific**: `crawler_rss.go`, `crawler_html.go`

### Test Files
- **Match source file with `_test` suffix**: `http_client_test.go`
- **Integration tests**: `integration_test.go`
- **Benchmark tests**: `benchmark_test.go`

### Package Names
- **Short, lowercase, singular**: `crawler`, not `crawlers`
- **No underscores or mixed caps**: `httpclient`, not `http_client` or `httpClient`
- **Match directory name**: `pkg/logger/` → `package logger`

## Package Design Best Practices

### 1. Keep Packages Focused
```go
// Good - focused package
package logger

type Logger struct {}
func New() *Logger {}
func (l *Logger) Info() {}

// Bad - too many responsibilities
package utils

func ParseURL() {}
func FormatDate() {}
func EncryptPassword() {}
```

### 2. Minimize Dependencies
```go
// Good - minimal dependencies
package models

type Article struct {
  ID    string
  Title string
}

// Bad - unnecessary dependencies
package models

import "github.com/some/http/client"

type Article struct {
  ID     string
  Client *http.Client // Don't mix concerns
}
```

### 3. Use Interfaces
```go
// Good - depend on interfaces
package crawler

type HTTPClient interface {
  Get(url string) (*Response, error)
}

type Crawler struct {
  client HTTPClient // Interface, not concrete type
}

// Bad - depend on concrete types
import "net/http"

type Crawler struct {
  client *http.Client // Hard to test
}
```

### 4. Avoid Circular Dependencies
```
✓ crawler → models
✗ crawler → models → crawler

✓ cmd/crawler → internal/crawler/core → pkg/logger
✗ pkg/logger → internal/crawler/core
```

## Growth Strategy

As the project grows, maintain structure:

### Adding New Features
```
internal/
├── crawler/
│   ├── core/          # Core interfaces
│   ├── news/          # News crawlers
│   │   ├── us/        # US sources
│   │   │   ├── cnn/
│   │   │   └── nytimes/
│   │   └── kr/        # Korean sources
│   │       ├── naver/
│   │       └── daum/
│   └── community/     # Community crawlers
```

### Adding New Services
```
cmd/
├── crawler/       # Crawler service
├── processor/     # Processing service
├── api/           # API service
└── scheduler/     # Job scheduler
```

### Adding New Libraries
```
pkg/
├── logger/        # Logging
├── config/        # Configuration
├── metrics/       # Metrics collection
└── queue/         # Queue abstraction
```

## References

- [Standard Go Project Layout](https://github.com/golang-standards/project-layout)
- [Effective Go](https://go.dev/doc/effective_go)
- [Go Code Review Comments](https://github.com/golang/go/wiki/CodeReviewComments)
