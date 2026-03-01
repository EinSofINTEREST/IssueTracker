# Code Style and Conventions

## Core Principles

### Readability First
- Code should be self-documenting
- Clear naming over clever code
- Simplicity over complexity
- Remove unnecessary code

### Minimal Comments
- Only comment WHY, not WHAT
- Code should explain itself through naming
- Remove commented-out code
- Avoid redundant comments

### No Over-Engineering
- Solve current problems, not future ones
- Avoid premature abstraction
- Delete unused code completely
- Keep it simple

## Go Style Guide

### Formatting

1. **Indentation**: 2 spaces (NOT tabs)
   ```go
   // Good
   func process() {
     if condition {
       doSomething()
     }
   }

   // Bad
   func process() {
       if condition {
           doSomething()
       }
   }
   ```

2. **Line Length**: Max 100 characters
   - Break long lines at logical points
   - Align function parameters vertically if needed

3. **Imports**: Group and order
   ```go
   import (
     // Standard library
     "context"
     "fmt"
     "time"

     // External packages
     "github.com/rs/zerolog/log"

     // Internal packages
     "issuetracker/internal/crawler"
     "issuetracker/internal/storage"
   )
   ```

4. **Blank Lines**: Strategic spacing
   ```go
   func Fetch(ctx context.Context, url string) (*Content, error) {
     if err := validate(url); err != nil {
       return nil, err
     }

     resp, err := http.Get(url)
     if err != nil {
       return nil, err
     }
     defer resp.Body.Close()

     return parse(resp)
   }
   ```

### Naming Conventions

1. **Variables**: camelCase, descriptive
   ```go
   // Good
   articleCount := 10
   maxRetries := 3
   httpClient := &http.Client{}

   // Bad
   ac := 10
   max_retries := 3
   HTTPClient := &http.Client{}
   ```

2. **Constants**: CamelCase (not SCREAMING_CASE)
   ```go
   // Good
   const (
     DefaultTimeout = 30 * time.Second
     MaxWorkers = 100
   )

   // Bad
   const (
     DEFAULT_TIMEOUT = 30 * time.Second
     MAX_WORKERS = 100
   )
   ```

3. **Functions**: CamelCase
   - Exported: Start with capital letter
   - Unexported: Start with lowercase
   ```go
   // Exported
   func FetchArticle(url string) {}
   func ParseHTML(html string) {}

   // Unexported
   func validateURL(url string) {}
   func extractContent(doc *goquery.Document) {}
   ```

4. **Interfaces**: Short, descriptive names
   ```go
   // Good
   type Crawler interface {
     Fetch(ctx context.Context, url string) error
   }

   type Repository interface {
     Save(ctx context.Context, article *Article) error
     Find(ctx context.Context, id string) (*Article, error)
   }

   // Bad - don't add "Interface" suffix
   type CrawlerInterface interface {}
   ```

5. **Struct Names**: Singular nouns
   ```go
   // Good
   type Article struct {}
   type Parser struct {}

   // Bad
   type Articles struct {}
   type HTMLParser struct {} // redundant if in html package
   ```

### Error Handling

1. **Error Messages**: Lowercase, no punctuation
   ```go
   // Good
   return fmt.Errorf("failed to fetch article: %w", err)

   // Bad
   return fmt.Errorf("Failed to fetch article.", err)
   ```

2. **Early Returns**: Check errors immediately
   ```go
   // Good
   func process() error {
     if err := validate(); err != nil {
       return err
     }

     if err := fetch(); err != nil {
       return err
     }

     return nil
   }

   // Bad
   func process() error {
     err := validate()
     if err == nil {
       err = fetch()
     }
     return err
   }
   ```

3. **Named Return Values**: Only for documentation
   ```go
   // Good - clear intent
   func split(s string) (head, tail string) {
     parts := strings.Split(s, ":")
     return parts[0], parts[1]
   }

   // Bad - naked returns are confusing
   func split(s string) (head, tail string) {
     parts := strings.Split(s, ":")
     head = parts[0]
     tail = parts[1]
     return
   }

   // Best - explicit returns
   func split(s string) (string, string) {
     parts := strings.Split(s, ":")
     return parts[0], parts[1]
   }
   ```

### Function Design

1. **Small Functions**: Max 50 lines
   - One responsibility per function
   - Extract complex logic

2. **Parameter Count**: Max 5 parameters
   ```go
   // Good
   type FetchOptions struct {
     Timeout time.Duration
     Retries int
     Headers map[string]string
   }

   func Fetch(ctx context.Context, url string, opts FetchOptions) {}

   // Bad
   func Fetch(ctx context.Context, url string, timeout time.Duration,
     retries int, headers map[string]string, proxy string) {}
   ```

3. **Context First**: Always first parameter
   ```go
   // Good
   func Fetch(ctx context.Context, url string) {}

   // Bad
   func Fetch(url string, ctx context.Context) {}
   ```

### Comments

**Language Policy**:
- **Primary**: Korean (한국어)
- **Secondary**: English (영어)
- **Encoding**: UTF-8
- **Style**: Mix Korean and English naturally for clarity

**Principles**:
- Keep comments minimal and essential
- Explain WHY, never WHAT
- Use Korean for main explanations, English for technical terms

1. **Function Comments**: Only for exported functions
   ```go
   // FetchArticle: 주어진 URL에서 article을 retrieves and parses 합니다.
   // 기사가 존재하지 않으면 ErrNotFound를 반환합니다.
   func FetchArticle(ctx context.Context, url string) (*Article, error) {
     // ...
   }

   // NewCrawler: 주어진 options에 맞춰 새로운 crawler instance를 생성합니다.
   // timeout이 0이면 기본값(30초)을 사용합니다.
   func NewCrawler(opts Options) *Crawler {
     // ...
   }

   // unexported 함수는 복잡한 경우에만 주석 작성
   func validateURL(url string) error {
     // ...
   }
   ```

2. **Inline Comments**: Explain WHY, not WHAT
   ```go
   // Good - 이유를 설명
   func retry() {
     // 서버 과부하 방지를 위해 재시도 전 대기
     time.Sleep(backoff)
   }

   // Good - 영어와 한글 혼용
   func processArticle() {
     // DLQ로 전송 (3회 재시도 후에도 실패한 경우)
     if retries >= MaxRetries {
       sendToDLQ()
     }
   }

   // Bad - 당연한 내용을 설명
   func retry() {
     // backoff 시간만큼 sleep
     time.Sleep(backoff)
   }
   ```

3. **TODO Comments**: Action items with context
   ```go
   // Good
   // TODO(username): rate limiter를 Redis 기반으로 변경 필요 (distributed 환경 대응)
   func applyRateLimit() {}

   // TODO: Kafka consumer lag이 1000 초과시 auto-scale 구현
   func monitorLag() {}

   // Bad - 맥락 없음
   // TODO: fix this
   func broken() {}
   ```

4. **Package-Level Comments**: Korean + English
   ```go
   // Package crawler는 다양한 소스(HTML, RSS, API)에서 웹 크롤링을 위한
   // 인터페이스와 구현체를 제공합니다.
   //
   // Package crawler provides interfaces and implementations for
   // web crawling across various sources (HTML, RSS, APIs).
   //
   // 모든 구현체는 Crawler 인터페이스를 만족해야 하며,
   // 소스 타입에 따라 적절한 생성자(NewHTMLCrawler, NewRSSCrawler)를 사용하세요.
   package crawler
   ```

### Struct Design

1. **Field Ordering**: Group related fields
   ```go
   type Article struct {
     // Identity
     ID  string
     URL string

     // Content
     Title string
     Body  string

     // Metadata
     Author      string
     PublishedAt time.Time

     // Internal
     createdAt time.Time
   }
   ```

2. **Struct Tags**: Aligned for readability
   ```go
   type Article struct {
     ID          string    `json:"id" db:"id"`
     Title       string    `json:"title" db:"title"`
     PublishedAt time.Time `json:"published_at" db:"published_at"`
   }
   ```

3. **Embedded Structs**: Use sparingly
   ```go
   // Good - clear composition
   type Crawler struct {
     client *http.Client
     config Config
   }

   // Avoid - unclear what's inherited
   type Crawler struct {
     http.Client
     Config
   }
   ```

### Package Design

1. **Package Names**: Short, lowercase, singular
   ```go
   // Good
   package crawler
   package storage

   // Bad
   package crawlers
   package article_storage
   ```

2. **Package Organization**: By functionality
   ```
   internal/
   └── crawler/
       ├── crawler.go      # Main interface
       ├── http.go         # HTTP implementation
       ├── rss.go          # RSS implementation
       └── crawler_test.go
   ```

3. **Internal Packages**: Hide implementation details
   - Use `internal/` for non-public packages
   - Expose only necessary types

### Concurrency

1. **Channel Direction**: Specify when possible
   ```go
   func producer(out chan<- string) {
     out <- "data"
   }

   func consumer(in <-chan string) {
     data := <-in
   }
   ```

2. **Context Handling**: Always check
   ```go
   func process(ctx context.Context) {
     for {
       select {
       case <-ctx.Done():
         return
       default:
         work()
       }
     }
   }
   ```

3. **WaitGroups**: Clear pattern
   ```go
   var wg sync.WaitGroup

   for i := 0; i < workers; i++ {
     wg.Add(1)
     go func() {
       defer wg.Done()
       work()
     }()
   }

   wg.Wait()
   ```

## Database Conventions

### SQL Style

1. **Keywords**: UPPERCASE
2. **Table/Column Names**: snake_case
3. **Indentation**: 2 spaces

```sql
-- Good
SELECT
  id,
  title,
  published_at
FROM articles
WHERE country = 'US'
  AND published_at > NOW() - INTERVAL '24 hours'
ORDER BY published_at DESC
LIMIT 100;

-- Bad
select id, title, published_at from articles where country = 'US'
and published_at > now() - interval '24 hours' order by published_at desc limit 100;
```

### Migration Files

```sql
-- migrations/001_create_articles.up.sql
CREATE TABLE IF NOT EXISTS articles (
  id VARCHAR(255) PRIMARY KEY,
  title TEXT NOT NULL,
  body TEXT NOT NULL,
  url TEXT NOT NULL UNIQUE,
  country VARCHAR(2) NOT NULL,
  published_at TIMESTAMP NOT NULL,
  created_at TIMESTAMP DEFAULT NOW()
);

CREATE INDEX idx_articles_country_published
  ON articles(country, published_at DESC);
```

## Configuration Files

### YAML Style

1. **Indentation**: 2 spaces
2. **Keys**: snake_case
3. **Comments**: Explain non-obvious values

```yaml
# Good
kafka:
  brokers:
    - kafka-1.example.com:9092
    - kafka-2.example.com:9092

  topic_configs:
    default_partitions: 16
    default_replication_factor: 3
    retention_ms: 86400000  # 24 hours

sources:
  us:
    news:
      - name: cnn
        enabled: true
        rate_limit: 100  # requests per hour

# Bad
kafka:
    brokers:
        - "kafka-1.example.com:9092"
        - "kafka-2.example.com:9092"

    topicConfigs:
        defaultPartitions: 16
        defaultReplicationFactor: 3
        # Retention in milliseconds
        retentionMs: 86400000
```

## Testing Conventions

### Test Function Names

```go
// Pattern: Test<Function>_<Scenario>_<Expected>
func TestFetchArticle_ValidURL_ReturnsContent(t *testing.T) {}
func TestFetchArticle_InvalidURL_ReturnsError(t *testing.T) {}
func TestFetchArticle_Timeout_ReturnsTimeoutError(t *testing.T) {}
```

### Test Structure

```go
func TestFetch_Success(t *testing.T) {
  // Arrange
  server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
    w.WriteHeader(http.StatusOK)
    w.Write([]byte("<html></html>"))
  }))
  defer server.Close()

  crawler := NewCrawler()

  // Act
  content, err := crawler.Fetch(context.Background(), server.URL)

  // Assert
  assert.NoError(t, err)
  assert.NotNil(t, content)
}
```

### Table-Driven Tests

```go
func TestNormalizeURL(t *testing.T) {
  tests := []struct {
    name  string
    input string
    want  string
    err   bool
  }{
    {
      name:  "removes www",
      input: "https://www.example.com",
      want:  "https://example.com",
    },
    {
      name:  "invalid url",
      input: "not-a-url",
      err:   true,
    },
  }

  for _, tt := range tests {
    t.Run(tt.name, func(t *testing.T) {
      got, err := NormalizeURL(tt.input)

      if tt.err {
        assert.Error(t, err)
        return
      }

      assert.NoError(t, err)
      assert.Equal(t, tt.want, got)
    })
  }
}
```

## Anti-Patterns to Avoid

### 1. Magic Numbers
```go
// Bad
if retries > 3 {
  return err
}

// Good
const MaxRetries = 3

if retries > MaxRetries {
  return err
}
```

### 2. Deep Nesting
```go
// Bad
if a {
  if b {
    if c {
      if d {
        doSomething()
      }
    }
  }
}

// Good
if !a {
  return
}
if !b {
  return
}
if !c {
  return
}
if !d {
  return
}

doSomething()
```

### 3. Else After Return
```go
// Bad
if err != nil {
  return err
} else {
  process()
}

// Good
if err != nil {
  return err
}

process()
```

### 4. Unnecessary Variables
```go
// Bad
func isValid(s string) bool {
  result := len(s) > 0
  return result
}

// Good
func isValid(s string) bool {
  return len(s) > 0
}
```

### 5. Init Functions
```go
// Avoid - implicit initialization
func init() {
  db = connectDB()
}

// Prefer - explicit initialization
func New() *Service {
  return &Service{
    db: connectDB(),
  }
}
```

## Logging Standards

### Log Levels
```go
// DEBUG - development only
log.Debug().Str("url", url).Msg("fetching article")

// INFO - normal operations
log.Info().Str("source", "cnn").Int("count", 10).Msg("articles fetched")

// WARN - unexpected but handled
log.Warn().Err(err).Msg("retry attempt")

// ERROR - operation failed
log.Error().Err(err).Str("url", url).Msg("failed to fetch article")
```

### Structured Logging
```go
// Good - structured fields
log.Info().
  Str("crawler", "cnn").
  Str("url", url).
  Int("status", resp.StatusCode).
  Dur("duration", elapsed).
  Msg("article fetched")

// Bad - string interpolation
log.Info().Msgf("Fetched %s from %s with status %d in %v", url, "cnn", resp.StatusCode, elapsed)
```

### Context in Logs
```go
// Include relevant context
log.Error().
  Err(err).
  Str("article_id", article.ID).
  Str("source", article.Source).
  Str("error_code", "PARSE_001").
  Msg("parse failed")

// Not just the error
log.Error().Err(err).Msg("error")
```

## Git Commit Conventions

### Commit Message Format
```
[{카테고리}]: {변경 내용}
```

### 카테고리 (Categories)

- **FEAT**: feature, 기능 구현 및 추가
- **FIX**: fix, 버그 수정
- **REFAC**: refactor, 구조 변경, 메소드 구조 변경 및 리팩토링
- **DOCS**: documentation, 문서 작업 및 프롬프트 변경, 주석 등 설명 요소 작성

### 변경 내용 작성 규칙

**⚠️ 중요: 모든 커밋 메시지는 한국어로 작성해야 합니다.**

1. **언어**: 반드시 한국어로 작성 (영어 사용 금지)
2. **형식**: 명사형 종결 (예: "구현", "수정", "추가")
3. **내용**: 변경 내용의 전체적인 요약, 각 모듈 단위의 변경점을 명확히 기술

### Examples

**기능 구현 및 추가:**
```
[FEAT]: Kafka consumer pool을 활용한 크롤러 워커 구현

- KafkaConsumerPool 구조체를 통한 다중 워커 goroutine 관리
- graceful shutdown 지원
- 설정 가능한 워커 개수 구현
```

```
[FEAT]: Reddit 크롤러를 활용한 미국 커뮤니티 데이터 수집

- RSS feed 기반 Reddit 크롤러 구현
- r/news, r/worldnews, r/politics 서브레딧 지원
```

**버그 수정:**
```
[FIX]: HTTP 클라이언트 timeout 에러 처리 개선

- timeout 발생 시 적절한 에러 핸들링 로직 추가
- exponential backoff 기반 재시도 로직 구현
- 느린 소스에서 크롤러 hang 방지
```

**구조 변경 및 리팩토링:**
```
[REFAC]: Article 검증 로직 단순화

- 불필요한 검증 단계 제거
- 관련 검증 로직 통합
- 함수 복잡도 25줄에서 15줄로 감소
```

**문서 작업:**
```
[DOCS]: 크롤러 API 문서 및 사용 예제 작성

- 크롤러 인터페이스 GoDoc 주석 추가
- examples/basic_usage.go 작성
- README.md에 Quick Start 섹션 추가
```

## File Organization

### File Naming
- Lowercase, underscore-separated: `http_client.go`
- Test files: `http_client_test.go`
- Implementation-specific: `crawler_rss.go`, `crawler_html.go`

### File Structure
```go
// 1. Package declaration
package crawler

// 2. Imports
import (
  "context"
  "fmt"
)

// 3. Constants
const (
  DefaultTimeout = 30 * time.Second
)

// 4. Types
type Crawler struct {
  client *http.Client
}

// 5. Constructor
func New(opts Options) *Crawler {
  return &Crawler{
    client: &http.Client{},
  }
}

// 6. Public methods
func (c *Crawler) Fetch(ctx context.Context, url string) error {
  return c.fetch(ctx, url)
}

// 7. Private methods
func (c *Crawler) fetch(ctx context.Context, url string) error {
  return nil
}

// 8. Helper functions
func validateURL(url string) error {
  return nil
}
```

## Performance Guidelines

### 1. Use String Builders
```go
// Good
var sb strings.Builder
for _, s := range items {
  sb.WriteString(s)
}
result := sb.String()

// Bad
var result string
for _, s := range items {
  result += s
}
```

### 2. Pre-allocate Slices
```go
// Good
items := make([]string, 0, expectedSize)

// Bad
var items []string
```

### 3. Use sync.Pool for Frequent Allocations
```go
var bufferPool = sync.Pool{
  New: func() interface{} {
    return new(bytes.Buffer)
  },
}

func process() {
  buf := bufferPool.Get().(*bytes.Buffer)
  defer bufferPool.Put(buf)
  buf.Reset()

  // Use buffer
}
```

## Documentation

### Language Requirements

**Code Documentation** (GoDoc):
- **Primary**: English (영어)
- **Secondary**: Korean (한국어)
- **Format**: English first, then Korean explanation
- **Encoding**: UTF-8

**Documentation Files**:
- **MUST** write all documentation in English first
- **MUST** provide Korean translation in separate directory
- **Directory structure**:
  ```
  docs/
  ├── en/           # English documentation (primary)
  │   ├── README.md
  │   ├── architecture.md
  │   └── api.md
  └── ko/           # Korean translation
      ├── README.md
      ├── architecture.md
      └── api.md
  ```
- **Naming convention**:
  - English: `docs/en/<filename>.md`
  - Korean: `docs/ko/<filename>.md`
- **Content**: Keep both versions synchronized

**README Files** (Root level):
- `README.md` - **MUST** be in English
- Link to Korean version: `docs/ko/README.md`
- Include language selector at the top:
  ```markdown
  # IssueTracker

  **[한국어](docs/ko/README.md)** | English
  ```

### Package Documentation

```go
// Package crawler provides interfaces and implementations for
// web crawling across various sources (HTML, RSS, APIs).
//
// crawler 패키지는 다양한 소스(HTML, RSS, API)에서 웹 크롤링을 위한
// 인터페이스와 구현체를 제공합니다.
//
// The core interface is Crawler, which all implementations must satisfy.
// Use the appropriate constructor (NewHTMLCrawler, NewRSSCrawler) based
// on your source type.
//
// 핵심 인터페이스는 Crawler이며, 모든 구현체는 이를 만족해야 합니다.
// 소스 타입에 따라 적절한 생성자(NewHTMLCrawler, NewRSSCrawler)를 사용하세요.
package crawler
```

### Type Documentation

```go
// Article represents a news article from any source.
// All fields are required except UpdatedAt which may be nil.
//
// Article은 모든 소스의 뉴스 기사를 나타냅니다.
// UpdatedAt을 제외한 모든 필드는 필수입니다.
type Article struct {
  ID    string
  Title string
  Body  string
}

// CrawlJob represents a crawling task to be processed by workers.
//
// CrawlJob은 worker가 처리할 크롤링 작업을 나타냅니다.
type CrawlJob struct {
  ID     string
  URL    string
  Source string
}
```

### Function Documentation

```go
// FetchArticle retrieves and parses an article from the given URL.
// It returns ErrNotFound if the article doesn't exist.
//
// FetchArticle은 주어진 URL에서 기사를 가져와 파싱합니다.
// 기사가 존재하지 않으면 ErrNotFound를 반환합니다.
//
// Example:
//   article, err := FetchArticle(ctx, "https://example.com/news/123")
//   if errors.Is(err, ErrNotFound) {
//     // Handle not found
//   }
func FetchArticle(ctx context.Context, url string) (*Article, error) {
  // ...
}
```

### Example Code

```go
// Example usage of the crawler.
//
// Crawler 사용 예시.
func ExampleCrawler_Fetch() {
  crawler := NewCrawler(Options{
    Timeout: 30 * time.Second,
  })

  article, err := crawler.Fetch(context.Background(), "https://example.com")
  if err != nil {
    log.Fatal(err)
  }

  fmt.Println(article.Title)
  // Output: Example Article Title
}
```

### README Structure

**README.md** (Root - English):
```markdown
# IssueTracker

**[한국어](docs/ko/README.md)** | English

> Global Issue Collection and Analysis System

## Overview

IssueTracker is a system that collects issues from news, communities,
and social media worldwide, and identifies major issues through
embedding and clustering.

## Key Features

- Real-time data collection from various sources
- Kafka-based distributed processing pipeline
- ML-based issue clustering
- Country/language-based trend analysis

## Getting Started

### Requirements

- Go 1.21+
- PostgreSQL 15+
- Apache Kafka 3.5+
- Redis 7+

### Installation

\`\`\`bash
git clone https://github.com/example/issuetracker
cd issuetracker
make install
\`\`\`

## Documentation

**English**:
- [Architecture](docs/en/architecture.md)
- [Crawler Implementation](docs/en/crawler-implementation.md)
- [Code Style](docs/en/code-style.md)

**한국어**:
- [아키텍처](docs/ko/architecture.md)
- [크롤러 구현](docs/ko/crawler-implementation.md)
- [코드 스타일](docs/ko/code-style.md)

## License

MIT
```

**docs/ko/README.md** (Korean Translation):
```markdown
# IssueTracker

한국어 | **[English](../../README.md)**

> 글로벌 이슈 수집 및 분석 시스템

## 개요

IssueTracker는 전 세계의 뉴스, 커뮤니티, 소셜 미디어에서 이슈를 수집하고
임베딩 및 클러스터링을 통해 주요 이슈를 식별하는 시스템입니다.

## 주요 기능

- 다양한 소스에서 실시간 데이터 수집
- Kafka 기반 분산 처리 파이프라인
- ML 기반 이슈 클러스터링
- 국가별/언어별 트렌드 분석

## 시작하기

### 요구사항

- Go 1.21+
- PostgreSQL 15+
- Apache Kafka 3.5+
- Redis 7+

### 설치

\`\`\`bash
git clone https://github.com/example/issuetracker
cd issuetracker
make install
\`\`\`

## 문서

**English**:
- [Architecture](../en/architecture.md)
- [Crawler Implementation](../en/crawler-implementation.md)
- [Code Style](../en/code-style.md)

**한국어**:
- [아키텍처](architecture.md)
- [크롤러 구현](crawler-implementation.md)
- [코드 스타일](code-style.md)

## 라이선스

MIT
```

### Documentation Best Practices

1. **Write English First**
   - All documentation **MUST** be written in English first
   - English version is the source of truth
   - Store in `docs/en/` directory

2. **Translate to Korean**
   - Create Korean translation after English is complete
   - Store in `docs/ko/` directory
   - Maintain same file structure as English
   - Keep synchronized with English updates

3. **Directory Organization**
   ```
   project/
   ├── README.md              # English (root level)
   ├── docs/
   │   ├── en/                # English documentation (source)
   │   │   ├── README.md
   │   │   ├── architecture.md
   │   │   ├── api.md
   │   │   ├── deployment.md
   │   │   └── troubleshooting.md
   │   └── ko/                # Korean translation
   │       ├── README.md
   │       ├── architecture.md
   │       ├── api.md
   │       ├── deployment.md
   │       └── troubleshooting.md
   └── .claude/
       └── rules/             # Development rules (English only)
   ```

4. **Code Comments**
   - Use English for GoDoc (exported functions/types)
   - Add Korean explanation below English
   - Mix languages naturally in inline comments

5. **API Documentation**
   - Generate with `godoc` (English)
   - Provide Korean translation in `docs/ko/api.md`
   - Include Korean in code comments for developers

6. **Architecture Docs**
   - Write in English first (`docs/en/architecture.md`)
   - Translate to Korean (`docs/ko/architecture.md`)
   - Include English technical terms in Korean version

7. **Changelogs**
   - Write in English (version control standard)
   - Provide Korean translation in separate section
   ```markdown
   ## v1.2.0 (2024-01-15)

   ### Added
   - Kafka consumer pool for crawler workers

   ### Fixed
   - Rate limiting issue in HTTP client

   ---

   ## v1.2.0 (2024-01-15) - 한국어

   ### 추가
   - 크롤러 워커를 위한 Kafka consumer pool

   ### 수정
   - HTTP 클라이언트의 rate limiting 문제
   ```

8. **Translation Workflow**
   - Step 1: Write complete English documentation
   - Step 2: Review and approve English version
   - Step 3: Translate to Korean, maintaining structure
   - Step 4: Review Korean translation for accuracy
   - Step 5: Link both versions with language selector

9. **Language Selector**
   - Include at the top of every document
   - English: `**[한국어](../ko/same-file.md)** | English`
   - Korean: `한국어 | **[English](../en/same-file.md)**`

10. **Update Policy**
    - When updating documentation:
      1. Update English version first
      2. Update Korean translation to match
      3. Mark in commit message: "docs: update [feature] (en+ko)"

## Code Review Checklist

Before submitting code for review, ensure:

- [ ] No commented-out code
- [ ] No unnecessary variables
- [ ] No magic numbers
- [ ] Error handling is complete
- [ ] Functions are small (< 50 lines)
- [ ] No deep nesting (< 4 levels)
- [ ] Tests are included
- [ ] Logging includes context
- [ ] Code is self-documenting
- [ ] Comments explain WHY, not WHAT
