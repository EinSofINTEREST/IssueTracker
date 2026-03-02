# Testing and Quality Assurance Rules

## Testing Philosophy

### Core Principles

1. **Test Coverage**
   - Minimum 70% code coverage for core packages
   - 90%+ coverage for critical paths (crawler logic, processing)
   - 100% coverage for error handling paths

2. **Test Pyramid**
   ```
        E2E Tests (5%)
       ┌─────────────┐
       │Integration  │ (25%)
       ├─────────────┤
       │   Unit      │ (70%)
       └─────────────┘
   ```

3. **Test Isolation**
   - Tests MUST NOT depend on external services
   - Use mocks/stubs for external dependencies
   - Tests MUST be deterministic
   - Tests MUST be parallelizable

4. **Test Naming**
   ```go
   // Pattern: Test<Function>_<Scenario>_<Expected>
   func TestFetchArticle_ValidURL_ReturnsContent(t *testing.T)
   func TestFetchArticle_InvalidURL_ReturnsError(t *testing.T)
   func TestFetchArticle_Timeout_ReturnsTimeoutError(t *testing.T)
   ```

## Unit Testing

### Testing Frameworks

1. **Standard Library**: `testing` package
2. **Assertions**: `github.com/stretchr/testify/assert`
3. **Mocking**: `github.com/stretchr/testify/mock`
4. **HTTP Mocking**: `httptest` package

### Unit Test Structure

```go
func TestCrawler_FetchArticle_Success(t *testing.T) {
    // Arrange
    server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
        w.Write([]byte("<html><body>Test Article</body></html>"))
    }))
    defer server.Close()

    crawler := NewCrawler(Config{
        Timeout: 5 * time.Second,
    })

    // Act
    content, err := crawler.Fetch(context.Background(), server.URL)

    // Assert
    assert.NoError(t, err)
    assert.NotNil(t, content)
    assert.Contains(t, content.HTML, "Test Article")
}
```

### What to Test

1. **Crawler Package**
   - HTTP request construction
   - Response parsing
   - Error handling (timeouts, 404s, 500s)
   - Rate limiting behavior
   - Retry logic
   - User-Agent setting

2. **Parser Package**
   - HTML parsing with various selectors
   - Missing elements handling
   - Malformed HTML handling
   - Encoding conversion
   - Content extraction accuracy

3. **Processor Package**
   - Normalization logic
   - Validation rules
   - Entity extraction
   - Keyword extraction
   - Content quality scoring

4. **Embedding Package**
   - Vector operations
   - Similarity calculations
   - Batch processing
   - Error handling (API failures)

5. **Storage Package**
   - CRUD operations
   - Query correctness
   - Transaction handling
   - Constraint violations

### Test Data

1. **Fixtures**
   - Store sample HTML in `testdata/` directories
   - Use realistic examples from actual sources
   - Include edge cases (empty, malformed, non-English)

   ```
   testdata/
   ├── html/
   │   ├── cnn_article.html
   │   ├── naver_article.html
   │   ├── malformed.html
   │   └── empty.html
   ├── json/
   │   ├── valid_article.json
   │   └── invalid_article.json
   └── rss/
       ├── valid_feed.xml
       └── invalid_feed.xml
   ```

2. **Builders**
   - Use builder pattern for test objects
   ```go
   func NewTestArticle() *Article {
       return &Article{
           ID:          "test-123",
           Title:       "Test Article",
           Body:        "Test body content",
           PublishedAt: time.Now(),
           Country:     "US",
           Language:    "en",
       }
   }

   func (a *Article) WithTitle(title string) *Article {
       a.Title = title
       return a
   }
   ```

### Table-Driven Tests

Use for testing multiple scenarios:

```go
func TestNormalizeURL(t *testing.T) {
    tests := []struct {
        name     string
        input    string
        expected string
        wantErr  bool
    }{
        {
            name:     "http to https",
            input:    "http://example.com",
            expected: "https://example.com",
            wantErr:  false,
        },
        {
            name:     "remove www",
            input:    "https://www.example.com",
            expected: "https://example.com",
            wantErr:  false,
        },
        {
            name:     "remove trailing slash",
            input:    "https://example.com/path/",
            expected: "https://example.com/path",
            wantErr:  false,
        },
        {
            name:     "invalid URL",
            input:    "not-a-url",
            expected: "",
            wantErr:  true,
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            result, err := NormalizeURL(tt.input)

            if tt.wantErr {
                assert.Error(t, err)
            } else {
                assert.NoError(t, err)
                assert.Equal(t, tt.expected, result)
            }
        })
    }
}
```

## Integration Testing

### Database Integration Tests

```go
func TestArticleRepository_Insert(t *testing.T) {
    // Use testcontainers for real PostgreSQL
    ctx := context.Background()

    postgres, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
        ContainerRequest: testcontainers.ContainerRequest{
            Image:        "postgres:15-alpine",
            ExposedPorts: []string{"5432/tcp"},
            Env: map[string]string{
                "POSTGRES_PASSWORD": "test",
                "POSTGRES_DB":       "test",
            },
            WaitingFor: wait.ForLog("database system is ready to accept connections"),
        },
        Started: true,
    })
    require.NoError(t, err)
    defer postgres.Terminate(ctx)

    // Get connection string and run migrations
    connStr := getConnectionString(postgres)
    db := setupTestDB(t, connStr)

    // Test repository
    repo := NewArticleRepository(db)
    article := NewTestArticle()

    err = repo.Insert(ctx, article)
    assert.NoError(t, err)

    // Verify
    retrieved, err := repo.GetByID(ctx, article.ID)
    assert.NoError(t, err)
    assert.Equal(t, article.Title, retrieved.Title)
}
```

### Kafka Integration Tests

```go
func TestKafka_PublishConsume(t *testing.T) {
    // Start Kafka container with testcontainers
    ctx := context.Background()

    kafkaContainer, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
        ContainerRequest: testcontainers.ContainerRequest{
            Image:        "confluentinc/cp-kafka:7.5.0",
            ExposedPorts: []string{"9093/tcp"},
            Env: map[string]string{
                "KAFKA_BROKER_ID":                          "1",
                "KAFKA_LISTENER_SECURITY_PROTOCOL_MAP":     "PLAINTEXT:PLAINTEXT,PLAINTEXT_HOST:PLAINTEXT",
                "KAFKA_ADVERTISED_LISTENERS":               "PLAINTEXT://localhost:9093",
                "KAFKA_OFFSETS_TOPIC_REPLICATION_FACTOR":   "1",
                "KAFKA_TRANSACTION_STATE_LOG_MIN_ISR":      "1",
                "KAFKA_TRANSACTION_STATE_LOG_REPLICATION_FACTOR": "1",
            },
            WaitingFor: wait.ForLog("started (kafka.server.KafkaServer)"),
        },
        Started: true,
    })
    require.NoError(t, err)
    defer kafkaContainer.Terminate(ctx)

    // Get Kafka broker address
    broker, err := kafkaContainer.Host(ctx)
    require.NoError(t, err)
    port, err := kafkaContainer.MappedPort(ctx, "9093")
    require.NoError(t, err)

    brokerAddr := fmt.Sprintf("%s:%s", broker, port.Port())

    // Create producer
    producer, err := kafka.NewProducer(&kafka.ConfigMap{
        "bootstrap.servers": brokerAddr,
    })
    require.NoError(t, err)
    defer producer.Close()

    // Create consumer
    consumer, err := kafka.NewConsumer(&kafka.ConfigMap{
        "bootstrap.servers": brokerAddr,
        "group.id":          "test-group",
        "auto.offset.reset": "earliest",
    })
    require.NoError(t, err)
    defer consumer.Close()

    // Subscribe to topic
    topic := "test-topic"
    err = consumer.Subscribe(topic, nil)
    require.NoError(t, err)

    // Publish message
    testJob := &CrawlJob{ID: "test-job", URL: "https://example.com"}
    msgBytes, _ := json.Marshal(testJob)

    err = producer.Produce(&kafka.Message{
        TopicPartition: kafka.TopicPartition{
            Topic:     &topic,
            Partition: kafka.PartitionAny,
        },
        Value: msgBytes,
    }, nil)
    require.NoError(t, err)
    producer.Flush(5000)

    // Consume message
    msg, err := consumer.ReadMessage(10 * time.Second)
    require.NoError(t, err)

    var received CrawlJob
    err = json.Unmarshal(msg.Value, &received)
    require.NoError(t, err)
    assert.Equal(t, testJob.ID, received.ID)
    assert.Equal(t, testJob.URL, received.URL)
}
```

### Processing Pipeline Integration Test

```go
func TestProcessingPipeline_EndToEnd(t *testing.T) {
    if testing.Short() {
        t.Skip("Skipping E2E pipeline test in short mode")
    }

    ctx := context.Background()

    // Set up Kafka
    kafka := setupKafka(t)
    defer kafka.Terminate(ctx)

    // Set up database
    db := setupTestDB(t)

    // Create topics
    topics := []string{
        "issuetracker.raw.us",
        "issuetracker.normalized",
        "issuetracker.validated",
    }
    createTopics(t, kafka.Broker(), topics)

    // Start processing workers
    normalizer := NewNormalizer(kafka.Broker(), db)
    go normalizer.Start(ctx)

    validator := NewValidator(kafka.Broker(), db)
    go validator.Start(ctx)

    // Publish raw article
    rawArticle := &RawContent{
        ID:  "test-123",
        URL: "https://example.com/article",
        HTML: loadTestData("testdata/html/sample_article.html"),
    }

    publishToKafka(t, kafka.Broker(), "issuetracker.raw.us", rawArticle)

    // Wait for processing and verify
    time.Sleep(5 * time.Second)

    // Check normalized article
    normalized := consumeFromKafka(t, kafka.Broker(), "issuetracker.normalized", 5*time.Second)
    assert.NotNil(t, normalized)

    // Check validated article
    validated := consumeFromKafka(t, kafka.Broker(), "issuetracker.validated", 5*time.Second)
    assert.NotNil(t, validated)

    // Verify in database
    repo := NewArticleRepository(db)
    article, err := repo.GetByID(ctx, rawArticle.ID)
    require.NoError(t, err)
    assert.Equal(t, "test-123", article.ID)
}
```

### End-to-End Crawler Tests

```go
func TestCrawler_E2E_FetchAndParse(t *testing.T) {
    if testing.Short() {
        t.Skip("Skipping E2E test in short mode")
    }

    // Set up test server with realistic HTML
    server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        html := loadTestData("testdata/html/cnn_article.html")
        w.Write(html)
    }))
    defer server.Close()

    // Create crawler with real dependencies
    crawler := NewCNNCrawler(Config{
        BaseURL: server.URL,
    })
    parser := NewCNNParser()

    // Fetch
    raw, err := crawler.Fetch(context.Background(), server.URL+"/article")
    require.NoError(t, err)

    // Parse
    article, err := parser.Parse(raw)
    require.NoError(t, err)

    // Validate results
    assert.NotEmpty(t, article.Title)
    assert.NotEmpty(t, article.Body)
    assert.Greater(t, article.WordCount, 100)
}
```

## Testing Strategies

### Mocking External Services

1. **HTTP Clients**
   ```go
   type HTTPClient interface {
       Do(req *http.Request) (*http.Response, error)
   }

   type MockHTTPClient struct {
       mock.Mock
   }

   func (m *MockHTTPClient) Do(req *http.Request) (*http.Response, error) {
       args := m.Called(req)
       return args.Get(0).(*http.Response), args.Error(1)
   }

   // In test
   func TestWithMock(t *testing.T) {
       mockClient := new(MockHTTPClient)
       mockClient.On("Do", mock.Anything).Return(&http.Response{
           StatusCode: 200,
           Body:       io.NopCloser(strings.NewReader("<html></html>")),
       }, nil)

       crawler := NewCrawler(mockClient)
       // Test crawler
   }
   ```

2. **Database**
   - Use `sqlmock` for unit tests
   - Use testcontainers for integration tests
   - Use in-memory SQLite for lightweight tests

3. **Embedding APIs**
   ```go
   type EmbeddingService interface {
       Embed(ctx context.Context, text string) ([]float32, error)
   }

   type MockEmbeddingService struct {
       mock.Mock
   }

   func (m *MockEmbeddingService) Embed(ctx context.Context, text string) ([]float32, error) {
       args := m.Called(ctx, text)
       return args.Get(0).([]float32), args.Error(1)
   }
   ```

### Testing Async/Concurrent Code

```go
func TestWorkerPool_ProcessJobs(t *testing.T) {
    pool := NewWorkerPool(5)
    jobs := make(chan Job, 10)
    results := make(chan Result, 10)

    // Start workers
    pool.Start(context.Background(), jobs, results)

    // Send jobs
    for i := 0; i < 10; i++ {
        jobs <- Job{ID: fmt.Sprintf("job-%d", i)}
    }
    close(jobs)

    // Collect results
    var count int
    timeout := time.After(5 * time.Second)
    for {
        select {
        case <-results:
            count++
            if count == 10 {
                assert.Equal(t, 10, count)
                return
            }
        case <-timeout:
            t.Fatal("Test timed out")
        }
    }
}
```

### Testing Rate Limiting

```go
func TestRateLimiter_EnforcesLimit(t *testing.T) {
    limiter := NewRateLimiter(10, time.Second) // 10 requests per second

    start := time.Now()
    var wg sync.WaitGroup

    // Try to make 20 requests
    for i := 0; i < 20; i++ {
        wg.Add(1)
        go func() {
            defer wg.Done()
            limiter.Wait(context.Background())
        }()
    }

    wg.Wait()
    elapsed := time.Since(start)

    // Should take at least 1 second (second batch)
    assert.GreaterOrEqual(t, elapsed, 1*time.Second)
}
```

## Benchmarking

### Performance Benchmarks

```go
func BenchmarkParser_ParseArticle(b *testing.B) {
    html := loadTestData("testdata/html/large_article.html")
    parser := NewParser()

    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        _, err := parser.Parse(html)
        if err != nil {
            b.Fatal(err)
        }
    }
}

func BenchmarkEmbedding_Generate(b *testing.B) {
    service := NewEmbeddingService()
    text := strings.Repeat("test ", 100)

    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        _, err := service.Embed(context.Background(), text)
        if err != nil {
            b.Fatal(err)
        }
    }
}

// Test with different input sizes
func BenchmarkParser_Sizes(b *testing.B) {
    sizes := []int{1000, 5000, 10000, 50000}

    for _, size := range sizes {
        b.Run(fmt.Sprintf("size=%d", size), func(b *testing.B) {
            html := strings.Repeat("<p>test</p>", size/10)
            parser := NewParser()

            b.ResetTimer()
            for i := 0; i < b.N; i++ {
                parser.Parse(html)
            }
        })
    }
}
```

### Memory Profiling

```bash
# Run with memory profiling
go test -bench=. -benchmem -memprofile=mem.prof

# Analyze
go tool pprof mem.prof
```

## Quality Gates

### Pre-Commit Checks

Create `.pre-commit-config.yaml`:

```yaml
repos:
  - repo: local
    hooks:
      - id: go-test
        name: Go Tests
        entry: go test ./...
        language: system
        pass_filenames: false

      - id: go-vet
        name: Go Vet
        entry: go vet ./...
        language: system
        pass_filenames: false

      - id: golangci-lint
        name: golangci-lint
        entry: golangci-lint run
        language: system
        pass_filenames: false
```

### Linting

Use `golangci-lint` with strict config:

```yaml
# .golangci.yml
linters:
  enable:
    - gofmt
    - govet
    - errcheck
    - staticcheck
    - unused
    - gosimple
    - structcheck
    - varcheck
    - ineffassign
    - deadcode
    - typecheck
    - gosec        # Security
    - gocritic     # Opinionated checks
    - gocyclo      # Complexity
    - dupl         # Duplicate code
    - misspell     # Spelling

linters-settings:
  gocyclo:
    min-complexity: 15
  govet:
    check-shadowing: true
  errcheck:
    check-blank: true

issues:
  max-same-issues: 0
  exclude-use-default: false
```

### Code Coverage

```bash
# Generate coverage report
go test -coverprofile=coverage.out ./...

# View in browser
go tool cover -html=coverage.out

# Check minimum coverage
go test -cover ./... | grep "coverage:" | awk '{if ($2 < 70.0) exit 1}'
```

## Test Organization

### Directory Structure

모든 테스트 파일은 `test/` 디렉토리 아래에 위치하며, **서비스 아키텍처와 동일한 경로 구조**를 따른다.

**규칙:**
- `internal/<pkg-path>/` 의 테스트 → `test/internal/<pkg-path>/`
- `pkg/<pkg-path>/` 의 테스트 → `test/pkg/<pkg-path>/`
- 소스 파일과 같은 디렉토리에 테스트 파일을 두지 않는다.

```
test/                               # 모든 테스트 파일의 루트
├── internal/                       # internal/ 패키지 테스트
│   ├── classifier/                 # ← internal/classifier/
│   │   ├── handler_test.go
│   │   └── http/                  # ← internal/classifier/http/
│   │       └── client_test.go
│   ├── crawler_core/               # ← internal/crawler/core/
│   │   ├── errors_test.go
│   │   ├── http_client_test.go
│   │   ├── models_test.go
│   │   ├── rate_limiter_test.go
│   │   └── retry_test.go
│   └── storage/                    # ← internal/storage/
│       ├── content_service_test.go
│       └── raw_content_service_test.go
└── pkg/                            # pkg/ 패키지 테스트
    ├── config/                     # ← pkg/config/
    │   └── config_test.go
    └── logger/                     # ← pkg/logger/
        └── logger_test.go
```

**새 패키지에 테스트 추가 시:**
- 소스 경로 `internal/foo/bar/` → 테스트 경로 `test/internal/foo/bar/`
- 소스 경로 `pkg/foo/` → 테스트 경로 `test/pkg/foo/`
- 패키지 선언은 `package <name>_test` 형식 사용 (외부 테스트 패키지)

```go
// test/internal/classifier/handler_test.go
package classifier_test

import "issuetracker/internal/classifier"
```

### Test Suites

Group related tests:

```go
type CrawlerTestSuite struct {
    suite.Suite
    crawler *Crawler
    server  *httptest.Server
}

func (s *CrawlerTestSuite) SetupTest() {
    s.server = httptest.NewServer(http.HandlerFunc(s.handleRequest))
    s.crawler = NewCrawler(Config{BaseURL: s.server.URL})
}

func (s *CrawlerTestSuite) TearDownTest() {
    s.server.Close()
}

func (s *CrawlerTestSuite) TestFetch_Success() {
    result, err := s.crawler.Fetch(context.Background(), "/article")
    s.NoError(err)
    s.NotNil(result)
}

func TestCrawlerSuite(t *testing.T) {
    suite.Run(t, new(CrawlerTestSuite))
}
```

## Continuous Integration

### GitHub Actions Workflow

```yaml
# .github/workflows/test.yml
name: Tests

on: [push, pull_request]

jobs:
  test:
    runs-on: ubuntu-latest

    services:
      postgres:
        image: postgres:15
        env:
          POSTGRES_PASSWORD: test
        options: >-
          --health-cmd pg_isready
          --health-interval 10s

    steps:
      - uses: actions/checkout@v3

      - name: Set up Go
        uses: actions/setup-go@v4
        with:
          go-version: '1.21'

      - name: Cache Go modules
        uses: actions/cache@v3
        with:
          path: ~/go/pkg/mod
          key: ${{ runner.os }}-go-${{ hashFiles('**/go.sum') }}

      - name: Run tests
        run: go test -race -coverprofile=coverage.out ./...

      - name: Check coverage
        run: |
          coverage=$(go tool cover -func=coverage.out | grep total | awk '{print $3}' | sed 's/%//')
          if (( $(echo "$coverage < 70" | bc -l) )); then
            echo "Coverage $coverage% is below 70%"
            exit 1
          fi

      - name: Run linters
        uses: golangci/golangci-lint-action@v3
        with:
          version: latest

      - name: Upload coverage
        uses: codecov/codecov-action@v3
        with:
          files: ./coverage.out
```

## Smoke Testing

### Production Smoke Tests

Run after deployment:

```go
func TestSmoke_CrawlerEndpoint(t *testing.T) {
    if os.Getenv("ENV") != "production" {
        t.Skip("Smoke tests only run in production")
    }

    resp, err := http.Get("https://api.issuetracker.com/health")
    assert.NoError(t, err)
    assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestSmoke_CanFetchArticle(t *testing.T) {
    // Test actual crawling of known stable source
    // Use canary URLs that rarely change
}
```

## Load Testing

### Locust Configuration

```python
# locustfile.py
from locust import HttpUser, task, between

class CrawlerUser(HttpUser):
    wait_time = between(1, 3)

    @task
    def fetch_article(self):
        self.client.get("/api/articles/latest")

    @task(3)
    def search_issues(self):
        self.client.get("/api/issues?country=US&limit=10")
```

Run load tests before major releases:

```bash
locust -f locustfile.py --host=https://api.issuetracker.com
```

## Test Documentation

### Test Plans

Document test scenarios in `docs/testing/`:

```
docs/testing/
├── test-plan.md           # Overall test strategy
├── crawler-tests.md       # Crawler-specific scenarios
├── processing-tests.md    # Processing pipeline tests
└── edge-cases.md          # Known edge cases and handling
```

### Known Issues

Track known issues and limitations:

```markdown
# Known Test Limitations

1. **Korean Text Parsing**
   - Issue: Some Naver articles have nested ad containers
   - Workaround: Additional selector filtering
   - Test: `TestNaverParser_NestedAds`

2. **Rate Limiting**
   - Issue: Cannot fully test rate limiting in unit tests
   - Approach: Integration tests with mock server
   - Test: `TestRateLimiter_Integration`
```
