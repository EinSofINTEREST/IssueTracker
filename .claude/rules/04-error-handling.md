# Error Handling and Monitoring Rules

## Error Handling Principles

### Core Principles

1. **Fail Gracefully**
   - NEVER panic in production code
   - Use `recover()` only in top-level handlers (HTTP, workers)
   - Return errors, don't swallow them
   - Log errors with full context

2. **Error Context**
   - Wrap errors with context using `fmt.Errorf` with `%w`
   - Include relevant information (URL, article ID, source)
   - Preserve error chain for debugging

3. **Error Types**
   - Use custom error types for different categories
   - Enable type-based handling
   - Include machine-readable error codes

## Error Taxonomy

### Error Categories

```go
type ErrorCategory string

const (
    // Temporary errors - retry possible
    ErrCategoryNetwork      ErrorCategory = "network"
    ErrCategoryRateLimit    ErrorCategory = "rate_limit"
    ErrCategoryTimeout      ErrorCategory = "timeout"

    // Permanent errors - retry useless
    ErrCategoryNotFound     ErrorCategory = "not_found"
    ErrCategoryForbidden    ErrorCategory = "forbidden"
    ErrCategoryParse        ErrorCategory = "parse"
    ErrCategoryValidation   ErrorCategory = "validation"

    // System errors
    ErrCategoryDatabase     ErrorCategory = "database"
    ErrCategoryQueue        ErrorCategory = "queue"
    ErrCategoryStorage      ErrorCategory = "storage"

    // Logic errors
    ErrCategoryConfig       ErrorCategory = "config"
    ErrCategoryInternal     ErrorCategory = "internal"
)
```

### Custom Error Types

```go
type CrawlerError struct {
    Category    ErrorCategory
    Code        string
    Message     string
    Source      string      // Source name
    URL         string      // Target URL
    StatusCode  int         // HTTP status if applicable
    Retryable   bool
    Err         error       // Wrapped error
}

func (e *CrawlerError) Error() string {
    return fmt.Sprintf("[%s:%s] %s: %v", e.Category, e.Code, e.Message, e.Err)
}

func (e *CrawlerError) Unwrap() error {
    return e.Err
}

func (e *CrawlerError) Is(target error) bool {
    t, ok := target.(*CrawlerError)
    if !ok {
        return false
    }
    return e.Code == t.Code || e.Category == t.Category
}
```

### Error Codes

Use consistent error codes across the system:

```
NET_001: Connection refused
NET_002: Connection timeout
NET_003: DNS resolution failed

HTTP_400: Bad request
HTTP_403: Forbidden
HTTP_404: Not found
HTTP_429: Rate limited
HTTP_500: Server error
HTTP_503: Service unavailable

PARSE_001: Invalid HTML structure
PARSE_002: Missing required selector
PARSE_003: Encoding error
PARSE_004: Invalid JSON

VAL_001: Missing required field
VAL_002: Invalid field format
VAL_003: Content too short
VAL_004: Content too long
VAL_005: Quality threshold not met

DB_001: Connection failed
DB_002: Query timeout
DB_003: Constraint violation
DB_004: Deadlock detected

QUEUE_001: Publish failed
QUEUE_002: Queue full
QUEUE_003: Message malformed

EMB_001: API rate limited
EMB_002: Token limit exceeded
EMB_003: Model error
```

## Retry Logic

### Retry Strategy

```go
type RetryPolicy struct {
    MaxAttempts     int
    InitialDelay    time.Duration
    MaxDelay        time.Duration
    Multiplier      float64
    Jitter          bool
    RetryableErrors []ErrorCategory
}

// Default policies
var (
    NetworkRetryPolicy = RetryPolicy{
        MaxAttempts:  3,
        InitialDelay: 1 * time.Second,
        MaxDelay:     30 * time.Second,
        Multiplier:   2.0,
        Jitter:       true,
        RetryableErrors: []ErrorCategory{
            ErrCategoryNetwork,
            ErrCategoryTimeout,
        },
    }

    RateLimitRetryPolicy = RetryPolicy{
        MaxAttempts:  5,
        InitialDelay: 10 * time.Second,
        MaxDelay:     5 * time.Minute,
        Multiplier:   2.0,
        Jitter:       true,
        RetryableErrors: []ErrorCategory{
            ErrCategoryRateLimit,
        },
    }
)
```

### Retry Implementation

```go
func WithRetry(ctx context.Context, policy RetryPolicy, fn func() error) error {
    var lastErr error

    for attempt := 0; attempt < policy.MaxAttempts; attempt++ {
        if attempt > 0 {
            delay := calculateBackoff(policy, attempt)
            select {
            case <-ctx.Done():
                return ctx.Err()
            case <-time.After(delay):
            }
        }

        err := fn()
        if err == nil {
            return nil
        }

        // Check if error is retryable
        var crawlerErr *CrawlerError
        if errors.As(err, &crawlerErr) && !crawlerErr.Retryable {
            return err
        }

        lastErr = err
        // Log retry attempt
        log.Warn().
            Err(err).
            Int("attempt", attempt+1).
            Int("max_attempts", policy.MaxAttempts).
            Msg("retrying after error")
    }

    return fmt.Errorf("max retries exceeded: %w", lastErr)
}
```

### Circuit Breaker

Implement circuit breaker for external services:

```go
type CircuitBreaker struct {
    MaxFailures     int
    Timeout         time.Duration
    ResetTimeout    time.Duration

    state           State  // Closed, Open, HalfOpen
    failures        int
    lastFailureTime time.Time
    mu              sync.RWMutex
}

func (cb *CircuitBreaker) Call(fn func() error) error {
    if !cb.canAttempt() {
        return ErrCircuitOpen
    }

    err := fn()

    if err != nil {
        cb.recordFailure()
        return err
    }

    cb.recordSuccess()
    return nil
}
```

Apply circuit breakers to:
- External API calls (embedding APIs)
- Database connections
- Third-party services

## Logging

### Structured Logging

Use `pkg/logger` (zerolog 기반 wrapper)로 구조화된 로깅을 수행합니다.
로그 메시지는 **반드시 영어**로 작성합니다. (주석·문서는 한국어 허용, 로그 msg 문자열은 영어)

```go
// Good — pkg/logger wrapper API
log.WithFields(map[string]interface{}{
    "crawler":     "cnn",
    "url":         articleURL,
    "status_code": resp.StatusCode,
    "duration_ms": elapsed.Milliseconds(),
}).Info("article fetched successfully")

log.WithFields(map[string]interface{}{
    "crawler":    "cnn",
    "url":        articleURL,
    "error_code": "PARSE_001",
}).WithError(err).Error("failed to parse article")

// Bad — 로그 메시지에 한국어 사용 금지
log.WithError(err).Error("기사 파싱 실패")
```

### Log Levels

레벨 선택 기준:

| Level | 사용 시점 | 예시 시나리오 |
|-------|-----------|--------------|
| **DEBUG** | 개발 또는 트러블슈팅 시에만 필요한 내부 상세 정보 | HTTP 요청 시작, 파싱 단계, 중복 감지, 재시도 결정 |
| **INFO**  | 운영자가 프로덕션에서 확인하고 싶은 정상 동작 마일스톤 | Job 시작/완료, 서비스 연결 성공, Worker pool 기동, 정기 purge 완료 |
| **WARN**  | 예상치 못했지만 gracefully 처리된 상황 — 시스템이 계속 동작 | 재시도 시도, fallback 프로토콜 전환, shutdown timeout |
| **ERROR** | 작업 실패 — 주의가 필요하지만 다른 요청은 계속 처리 가능 | Job 처리 실패, 메시지 발행 실패, 파싱 실패 |
| **FATAL** | 복구 불가능한 오류 — 프로세스 종료 필요 | 설정 파싱 실패, 기동 시 필수 DB 연결 불가 |

**빠른 판단 기준:**
- 운영자가 알림을 받아야 하면: **ERROR** 또는 **FATAL**
- 처리됐지만 운영자가 알아야 하면: **WARN**
- 정상 동작 확인: **INFO**
- 개발자 트레이싱 용: **DEBUG**

```go
// DEBUG — 운영 환경에서는 필터링됨
log.WithField("url", url).Debug("starting HTTP GET request")
log.WithField("attempt", attempt).Debug("error not retryable, skipping retry")

// INFO — 정상 완료 마일스톤
log.WithFields(map[string]interface{}{"job_id": id, "crawler": name}).Info("crawl job started")
log.WithFields(map[string]interface{}{"deleted_count": n, "cutoff": t}).Info("raw content purge completed")

// WARN — 예상 외 상황이지만 처리됨
log.WithFields(map[string]interface{}{"attempt": a, "max_attempts": m, "delay_ms": d}).Warn("retrying after error")
log.WithError(err).Warn("primary classifier failed, falling back to secondary protocol")

// ERROR — 작업 실패
log.WithFields(map[string]interface{}{"job_id": id, "crawler": name}).WithError(err).Error("job processing failed")
log.WithError(err).Error("failed to send message to dlq")
```

### Field Naming Conventions

모든 구조화 필드 키는 **snake_case**를 사용합니다. 아래 표에 정의된 표준 이름을 반드시 사용합니다.

| Field Key        | Type     | 설명 |
|-----------------|----------|------|
| `crawler`       | string   | Crawler 이름 (예: `"cnn"`, `"naver"`) |
| `source`        | string   | `SourceInfo.Name`과 동일한 소스 이름 |
| `country`       | string   | ISO 3166-1 alpha-2 (예: `"US"`, `"KR"`) |
| `url`           | string   | 처리 대상 URL |
| `job_id`        | string   | CrawlJob UUID |
| `request_id`    | string   | 인바운드 요청 추적 ID |
| `error_code`    | string   | 에러 코드 상수 (예: `"NET_001"`, `"PARSE_001"`) |
| `status_code`   | int      | HTTP 응답 상태 코드 |
| `duration_ms`   | int64    | 경과 시간 (밀리초 단위) |
| `attempt`       | int      | 현재 시도 횟수 (1-based) |
| `max_attempts`  | int      | 최대 시도 횟수 |
| `delay_ms`      | int64    | 재시도 backoff 대기 시간 (밀리초) |
| `worker_count`  | int      | 워커 goroutine 수 |
| `priority`      | int      | CrawlJob 우선순위 레벨 |
| `topic`         | string   | Kafka 토픽 이름 |
| `existing_id`   | string   | 중복 감지 시 기존 레코드 ID |
| `content_hash`  | string   | 중복 감지용 SHA-256 콘텐츠 해시 |
| `deleted_count` | int64    | 일괄 삭제된 레코드 수 |
| `cutoff`        | string   | RFC3339 형식 purge 기준 시각 |
| `host`          | string   | DB 호스트 |
| `port`          | int      | DB 포트 |
| `database`      | string   | DB 이름 |
| `max_conns`     | int32    | 커넥션 풀 최대 크기 |

**규칙:**
- 임의의 키 이름 사용 금지 (예: `"db_name"` 대신 `"database"` 사용)
- 시간 값은 반드시 밀리초 int64로, 키는 `duration_ms` 또는 `delay_ms`
- 불리언 플래그는 긍정형 이름 사용 (예: `"retryable"`, `"is_duplicate"`)

### Per-Component Required Fields

컴포넌트별로 모든 로그 항목에 반드시 포함해야 하는 최소 필드입니다.

**HTTP Client** (`internal/crawler/core/http_client.go`):
- 모든 로그: `url`
- 요청 완료 시: `status_code`, `duration_ms`
- 에러 발생 시: `error_code`

**Retry Logic** (`internal/crawler/core/retry.go`):
- 재시도 시: `attempt`, `max_attempts`, `delay_ms`
- 에러 포함: `.WithError(err)` 체이닝

**Worker Pool** (`internal/crawler/worker/`):
- Job 처리 로그: `job_id`, `crawler`
- Job 시작/완료 로그: `job_id`, `crawler`, `url`
- Pool 라이프사이클 로그: `priority`, `worker_count`
- 메시지 발행 로그: `job_id`, `crawler`, `priority`, `topic`
- DLQ/재큐잉 에러: `job_id`, `crawler`

**Storage Service** (`internal/storage/service/`):
- 중복 감지 로그: `existing_id`, `url` 또는 `content_hash`
- 일괄 삭제 완료: `deleted_count`, `cutoff`

**Database** (`internal/storage/postgres/`):
- 연결 성공: `host`, `port`, `database`, `max_conns`

**Classifier** (`internal/classifier/`):
- fallback 전환 시: `.WithError(err)` 체이닝

### Log Context

컴포넌트 초기화 시 scoped logger를 생성하여 반복 필드 중복을 방지합니다:

```go
type LogContext struct {
    RequestID   string
    CrawlerName string
    Source      string
    Country     string
    URL         string
    ArticleID   string
    JobID       string
}

// pkg/logger를 사용하여 context에 필드 추가
func buildLogger(ctx context.Context, lc LogContext) *logger.Logger {
    return logger.FromContext(ctx).WithFields(map[string]interface{}{
        "request_id": lc.RequestID,
        "crawler":    lc.CrawlerName,
        "source":     lc.Source,
        "country":    lc.Country,
    })
}
```

### Log Storage

1. **Console**: 개발 및 디버깅 (Pretty mode)
2. **File**: 구조화된 JSON 로그
   - 일별 로테이션
   - 오래된 로그 압축
   - 30일 보존
3. **Centralized**: 프로덕션 로깅
   - Loki, Elasticsearch, 또는 CloudWatch 사용
   - 전문 검색 활성화
   - 에러 패턴 알림 설정

## Monitoring

### Metrics Collection

Use Prometheus for metrics:

```go
var (
    // Crawler metrics
    articlesScraped = promauto.NewCounterVec(
        prometheus.CounterOpts{
            Name: "issuetracker_articles_scraped_total",
            Help: "Total number of articles scraped",
        },
        []string{"country", "source", "status"},
    )

    scrapeDuration = promauto.NewHistogramVec(
        prometheus.HistogramOpts{
            Name:    "issuetracker_scrape_duration_seconds",
            Help:    "Duration of scrape operations",
            Buckets: prometheus.DefBuckets,
        },
        []string{"country", "source"},
    )

    activeWorkers = promauto.NewGaugeVec(
        prometheus.GaugeOpts{
            Name: "issuetracker_active_workers",
            Help: "Number of active crawler workers",
        },
        []string{"worker_type"},
    )

    queueDepth = promauto.NewGaugeVec(
        prometheus.GaugeOpts{
            Name: "issuetracker_queue_depth",
            Help: "Number of items in processing queue",
        },
        []string{"queue_name"},
    )

    // Processing metrics
    processingDuration = promauto.NewHistogramVec(
        prometheus.HistogramOpts{
            Name:    "issuetracker_processing_duration_seconds",
            Help:    "Duration of processing stages",
            Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5},
        },
        []string{"stage"},
    )

    // Embedding metrics
    embeddingRequests = promauto.NewCounterVec(
        prometheus.CounterOpts{
            Name: "issuetracker_embedding_requests_total",
            Help: "Total embedding API requests",
        },
        []string{"status"},
    )

    // Clustering metrics
    clustersCreated = promauto.NewCounter(
        prometheus.CounterOpts{
            Name: "issuetracker_clusters_created_total",
            Help: "Total number of issue clusters created",
        },
    )

    clusterSize = promauto.NewHistogram(
        prometheus.HistogramOpts{
            Name:    "issuetracker_cluster_size",
            Help:    "Distribution of cluster sizes",
            Buckets: []float64{1, 5, 10, 25, 50, 100, 250, 500},
        },
    )
)
```

### Health Checks

Implement health check endpoints:

```go
type HealthCheck struct {
    Component   string
    Status      Status  // Healthy, Degraded, Unhealthy
    Message     string
    Latency     time.Duration
    LastChecked time.Time
}

type HealthChecker interface {
    Check(ctx context.Context) HealthCheck
}

// Implement for each component
type DatabaseHealthChecker struct {
    db *sql.DB
}

func (h *DatabaseHealthChecker) Check(ctx context.Context) HealthCheck {
    start := time.Now()
    err := h.db.PingContext(ctx)
    latency := time.Since(start)

    if err != nil {
        return HealthCheck{
            Component:   "database",
            Status:      Unhealthy,
            Message:     err.Error(),
            Latency:     latency,
            LastChecked: time.Now(),
        }
    }

    status := Healthy
    if latency > 100*time.Millisecond {
        status = Degraded
    }

    return HealthCheck{
        Component:   "database",
        Status:      status,
        Latency:     latency,
        LastChecked: time.Now(),
    }
}
```

Health check for:
- Database connectivity
- Queue connectivity
- Vector DB connectivity
- External APIs (embedding service)
- Disk space
- Memory usage

### Alerting Rules

Define alerting conditions:

```yaml
# Prometheus alerting rules
groups:
  - name: issuetracker
    interval: 30s
    rules:
      # High error rate
      - alert: HighCrawlErrorRate
        expr: |
          rate(issuetracker_articles_scraped_total{status="error"}[5m]) /
          rate(issuetracker_articles_scraped_total[5m]) > 0.1
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "High error rate in crawler"
          description: "{{ $labels.source }} has {{ $value }}% error rate"

      # Queue backup
      - alert: QueueBacklog
        expr: issuetracker_queue_depth > 10000
        for: 10m
        labels:
          severity: warning
        annotations:
          summary: "Queue backlog detected"
          description: "{{ $labels.queue_name }} has {{ $value }} items"

      # Source down
      - alert: CrawlerSourceDown
        expr: |
          absent_over_time(
            issuetracker_articles_scraped_total{status="success"}[1h]
          )
        labels:
          severity: critical
        annotations:
          summary: "Crawler source appears down"
          description: "No successful scrapes from {{ $labels.source }} in 1h"

      # Embedding API issues
      - alert: EmbeddingAPIErrors
        expr: |
          rate(issuetracker_embedding_requests_total{status="error"}[5m]) > 10
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "High embedding API error rate"

      # Low throughput
      - alert: LowThroughput
        expr: |
          rate(issuetracker_articles_scraped_total{status="success"}[1h]) < 100
        for: 30m
        labels:
          severity: info
        annotations:
          summary: "Low crawling throughput"
```

### Dashboards

Create Grafana dashboards for:

1. **Overview Dashboard**
   - Articles scraped per country/source (timeseries)
   - Success/error rates
   - Active workers
   - Queue depths

2. **Crawler Dashboard**
   - Per-source success rates
   - Response time distributions
   - Error breakdown by type
   - Rate limit incidents

3. **Processing Dashboard**
   - Processing latency per stage
   - Validation failure reasons
   - Quality score distribution
   - Throughput per stage

4. **Clustering Dashboard**
   - Active clusters over time
   - Cluster size distribution
   - Trending issues (top 10)
   - Cross-country issue links

5. **System Dashboard**
   - CPU/Memory usage
   - Database connections
   - API quota usage
   - Disk space

## Incident Response

### On-Call Procedures

1. **Alert Response**
   - Check Grafana dashboards
   - Review recent logs
   - Identify affected components
   - Assess impact (affected sources/countries)

2. **Mitigation Steps**
   - For source errors: Disable crawler temporarily
   - For queue backup: Scale up workers
   - For API errors: Switch to fallback or reduce rate
   - For database issues: Check connections, optimize queries

3. **Escalation**
   - P0 (Critical): System down, data loss
   - P1 (High): Major source down, >50% error rate
   - P2 (Medium): Single source down, degraded performance
   - P3 (Low): Minor issues, quality concerns

### Debugging Tools

1. **Request Tracing**
   - Generate unique request ID per article
   - Trace through entire pipeline
   - Store in logs and database

2. **Replay Capability**
   - Store raw HTTP responses
   - Ability to replay processing on raw data
   - Test fixes without re-crawling

3. **Debug Endpoints**
   ```
   GET /debug/crawler/:source     - Crawler status
   GET /debug/article/:id/trace   - Processing trace
   GET /debug/queue/stats         - Queue statistics
   POST /debug/article/:id/reprocess - Reprocess article
   ```

## Data Integrity

### Consistency Checks

1. **Referential Integrity**
   - Verify all article IDs in clusters exist
   - Check orphaned embeddings
   - Validate entity references

2. **Data Validation**
   - Random sampling of stored data
   - Verify encoding consistency
   - Check for corruption

3. **Reconciliation**
   - Compare crawled count vs stored count
   - Detect missing data
   - Identify duplicates

### Backup and Recovery

1. **Backup Strategy**
   - Database: Daily full backup, hourly incremental
   - Vector DB: Weekly snapshot
   - Raw data: Already in object storage (S3)
   - Config: Version controlled in git

2. **Recovery Procedures**
   - Document restore procedures
   - Test recovery quarterly
   - Maintain runbooks

3. **Data Retention**
   - Raw HTML: 90 days
   - Processed articles: Indefinite
   - Logs: 30 days
   - Metrics: 1 year (downsampled after 30 days)
