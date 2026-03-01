# Code Style Guide

## Go Formatting Standards

### Indentation
- **2 spaces** (NOT tabs)
- Configure your editor to use 2 spaces for Go files

```go
// Good
func process() {
  if condition {
    doSomething()
  }
}

// Bad (tabs or 4 spaces)
func process() {
    if condition {
        doSomething()
    }
}
```

### Line Length
- **Maximum 100 characters**
- Break long lines at logical points

### Imports
Group and order imports:
1. Standard library
2. External packages
3. Internal packages

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
  "issuetracker/pkg/logger"
)
```

## Naming Conventions

### Variables
- **camelCase**, descriptive names
```go
// Good
articleCount := 10
maxRetries := 3
httpClient := &http.Client{}

// Bad
ac := 10
max_retries := 3
```

### Constants
- **CamelCase** (not SCREAMING_CASE)
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

### Functions
- **Exported**: Start with capital letter
- **Unexported**: Start with lowercase

```go
// Exported
func FetchArticle(url string) {}
func ParseHTML(html string) {}

// Unexported
func validateURL(url string) {}
func extractContent(doc *goquery.Document) {}
```

### Interfaces
- Short, descriptive names
- No "Interface" suffix

```go
// Good
type Crawler interface {
  Fetch(ctx context.Context, url string) error
}

// Bad
type CrawlerInterface interface {}
```

## Comments and Documentation

### Language Policy
- **Primary**: Korean (한국어) - for explanations
- **Secondary**: English - for technical terms
- **Mix naturally** for clarity

### Function Comments
Only for exported functions:

```go
// FetchArticle: 주어진 URL에서 article을 retrieves and parses 합니다.
// 기사가 존재하지 않으면 ErrNotFound를 반환합니다.
func FetchArticle(ctx context.Context, url string) (*Article, error) {
  // ...
}
```

### Inline Comments
Explain WHY, not WHAT:

```go
// Good - 이유를 설명
func retry() {
  // 서버 과부하 방지를 위해 재시도 전 대기
  time.Sleep(backoff)
}

// Bad - 당연한 내용
func retry() {
  // backoff 시간만큼 sleep
  time.Sleep(backoff)
}
```

### TODO Comments
```go
// Good
// TODO(username): rate limiter를 Redis 기반으로 변경 필요 (distributed 환경 대응)

// Bad
// TODO: fix this
```

## Error Handling

### Error Messages
- Lowercase, no punctuation
```go
// Good
return fmt.Errorf("failed to fetch article: %w", err)

// Bad
return fmt.Errorf("Failed to fetch article.", err)
```

### Early Returns
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

### Avoid Else After Return
```go
// Good
if err != nil {
  return err
}
process()

// Bad
if err != nil {
  return err
} else {
  process()
}
```

## Function Design

### Small Functions
- **Maximum 50 lines**
- One responsibility per function
- Extract complex logic

### Parameter Count
- **Maximum 5 parameters**
- Use structs for multiple parameters

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
  retries int, headers map[string]string) {}
```

### Context First
Always pass context as first parameter:

```go
// Good
func Fetch(ctx context.Context, url string) {}

// Bad
func Fetch(url string, ctx context.Context) {}
```

## Struct Design

### Field Ordering
Group related fields:

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

### Struct Tags
Align for readability:

```go
type Article struct {
  ID          string    `json:"id" db:"id"`
  Title       string    `json:"title" db:"title"`
  PublishedAt time.Time `json:"published_at" db:"published_at"`
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
      doSomething()
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
doSomething()
```

### 3. Unnecessary Variables
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

### 4. Commented-Out Code
```go
// Bad
func process() {
  // oldImplementation()
  newImplementation()
}

// Good - just delete it
func process() {
  newImplementation()
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
Use structured fields, not string interpolation:

```go
// Good
log.Info().
  Str("crawler", "cnn").
  Str("url", url).
  Int("status", resp.StatusCode).
  Dur("duration", elapsed).
  Msg("article fetched")

// Bad
log.Info().Msgf("Fetched %s from %s with status %d", url, "cnn", resp.StatusCode)
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
  server := httptest.NewServer(...)
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
    name     string
    input    string
    expected string
    wantErr  bool
  }{
    {
      name:     "removes www",
      input:    "https://www.example.com",
      expected: "https://example.com",
    },
    {
      name:    "invalid url",
      input:   "not-a-url",
      wantErr: true,
    },
  }

  for _, tt := range tests {
    t.Run(tt.name, func(t *testing.T) {
      got, err := NormalizeURL(tt.input)

      if tt.wantErr {
        assert.Error(t, err)
        return
      }

      assert.NoError(t, err)
      assert.Equal(t, tt.expected, got)
    })
  }
}
```

## Code Review Checklist

Before submitting code:

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
- [ ] 2-space indentation
- [ ] Test coverage >= 70%
