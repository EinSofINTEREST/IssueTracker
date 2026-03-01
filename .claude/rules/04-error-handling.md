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

Use `github.com/rs/zerolog` for structured logging:

```go
log.Info().
    Str("crawler", "cnn").
    Str("url", articleURL).
    Int("status_code", resp.StatusCode).
    Dur("duration", elapsed).
    Msg("article fetched successfully")

log.Error().
    Err(err).
    Str("crawler", "cnn").
    Str("url", articleURL).
    Str("error_code", "PARSE_001").
    Msg("failed to parse article")
```

### Log Levels

1. **DEBUG**: Detailed diagnostic information
   - Request/response details
   - Parsing steps
   - Processing decisions

2. **INFO**: General informational messages
   - Successful operations
   - Startup/shutdown
   - Configuration loaded
   - Job completed

3. **WARN**: Warning messages
   - Retry attempts
   - Degraded performance
   - Deprecated usage
   - Quality issues

4. **ERROR**: Error messages
   - Failed operations
   - Validation failures
   - External service errors
   - Data corruption

5. **FATAL**: Critical errors requiring shutdown
   - Configuration errors
   - Fatal database errors
   - Unrecoverable state

### Log Context

Always include relevant context:

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

// Use context to carry log fields
func WithLogContext(ctx context.Context, lc LogContext) context.Context {
    logger := log.With().
        Str("request_id", lc.RequestID).
        Str("crawler", lc.CrawlerName).
        Str("source", lc.Source).
        Str("country", lc.Country).
        Logger()

    return logger.WithContext(ctx)
}
```

### Log Storage

1. **Console**: Development and debugging
2. **File**: Structured JSON logs
   - Rotate daily
   - Compress old logs
   - Retain for 30 days
3. **Centralized**: Production logging
   - Use Loki, Elasticsearch, or CloudWatch
   - Enable full-text search
   - Set up alerts on error patterns

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
