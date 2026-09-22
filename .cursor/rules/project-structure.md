# Project Structure

## Directory Layout

IssueTracker follows the [Standard Go Project Layout](https://github.com/golang-standards/project-layout).

```
issuetracker/
├── cmd/                        # Application entry points (모두 bin/ 으로 빌드)
│   ├── issuetracker/          # Fetcher + Parser + Validate + Enrich + Scheduler
│   ├── processor/             # Validator-only standalone
│   ├── migrate/               # DB migration (up)
│   ├── migrate-down/          # DB migration (down)
│   ├── rule-validator/        # parsing rule 검증 도구
│   └── admin/                 # 운영자 도구 (진입 마커 무효화 / 강제 재크롤 / DLQ)
│
├── internal/                   # Private application code
│   ├── bus/                   # Kafka I/O 단일 출처 (Publisher / RetryScheduler)
│   ├── workerpool/            # stage 공용 consumer pool harness
│   ├── scheduler/             # 시드 URL 주기 발행
│   ├── promptcontract/        # prompt placeholder 계약 집계
│   ├── classifier/            # grpc/http client (현재 파이프라인 미연결)
│   ├── locks/                 # IngestionMarker / ProcessingLock / StageGate
│   ├── processor/             # 파이프라인 단계
│   │   ├── fetcher/           # Web fetch + parse 라우팅 + worker pool
│   │   ├── parser/            # DB-driven rule engine + ParserWorker
│   │   ├── validate/          # 품질 점수 기반 Validation
│   │   └── enrich/            # extract / cross-verify / context / score
│   └── storage/               # postgres / redis / service / decorator / model
│
├── pkg/                        # Public library code
│   ├── logger/                # zerolog wrapper
│   ├── config/                # 환경변수 로딩
│   ├── queue/                 # Kafka producer / consumer + 우선순위 ZSET
│   ├── redis/                 # client + lock + leader lock
│   ├── llm/                   # provider + prompt loader·계약
│   ├── agent/                 # CLI agent 추상 + claude 구현
│   ├── links/                 # URL 정규화 / 링크 추출
│   ├── metrics/               # Prometheus registry
│   ├── resilience/            # circuit breaker
│   └── urlguard/              # URL 차단 규칙
│
├── test/                       # Test files (mirrors service architecture)
│   ├── internal/              # ← internal/
│   └── pkg/                   # ← pkg/
│
├── examples/                   # Usage examples
├── migrations/                 # SQL migrations
├── proto/                      # gRPC 정의
├── scripts/                    # 운영 / 검증 스크립트 (harness-check.sh 등)
├── deployments/                # Deployment configurations
│
├── docs/                       # Documentation
│   ├── architecture/          # 코드 구조 문서 (cmd / internal / pkg / proto)
│   ├── ci/                    # CI 운영 규약, status check 단일 소스
│   └── ko/                    # 한국어 문서
│
├── .claude/                    # Claude AI development rules
│   └── rules/
│
├── .cursor/                    # Cursor IDE rules
│   └── rules/
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
- Examples: `issuetracker`, `processor`, `migrate`, `rule-validator`, `admin`

```go
// cmd/issuetracker/main.go
package main

import (
  "issuetracker/internal/processor/fetcher/core"
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
- Go compiler prevents imports like `github.com/other/project/internal/secret`
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

- **모든** 테스트 파일은 소스 코드와 분리하여 `test/` 아래에만 위치한다
- **서비스 아키텍처와 동일한 디렉토리 구조**를 유지한다
  - `internal/<pkg-path>/` → `test/internal/<pkg-path>/`
  - `pkg/<pkg-path>/` → `test/pkg/<pkg-path>/`
- 패키지 선언은 `package <name>_test` (외부 테스트 패키지) 형식 사용
- `go test` 실행 시 `./test/...` 로 전체 테스트 탐색

**디렉토리 매핑 예시:**
```
internal/classifier/handler.go     → test/internal/classifier/handler_test.go
internal/processor/fetcher/core/retry.go     → test/internal/processor/fetcher/core/retry_test.go
pkg/logger/logger.go               → test/pkg/logger/logger_test.go
```

**Benefits:**
- 소스 코드와 테스트 코드의 명확한 분리
- 서비스 구조 변경 시 테스트 위치를 직관적으로 파악 가능
- 빌드 산출물에서 테스트 제외 용이

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
import "issuetracker/internal/processor/fetcher/core"
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

✓ cmd/issuetracker → internal/processor/fetcher/core → pkg/logger
✗ pkg/logger → internal/processor/fetcher/core
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
