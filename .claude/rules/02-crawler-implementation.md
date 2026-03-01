# Crawler Implementation Rules

## Core Crawler Interface

### Base Crawler Interface
```go
type Crawler interface {
    // Metadata
    Name() string
    Source() SourceInfo

    // Lifecycle
    Initialize(ctx context.Context, config Config) error
    Start(ctx context.Context) error
    Stop(ctx context.Context) error

    // Crawling
    Fetch(ctx context.Context, target Target) (*RawContent, error)

    // Health
    HealthCheck(ctx context.Context) error
}

type SourceInfo struct {
    Country     string    // ISO 3166-1 alpha-2 (US, KR)
    Type        SourceType // News, Community, Social
    Name        string
    BaseURL     string
    Language    string    // ISO 639-1 (en, ko)
}
```

### Rules for All Crawlers

1. **Context Handling**
   - MUST respect context cancellation
   - MUST implement timeout for all HTTP requests
   - Default timeout: 30 seconds per request

2. **Rate Limiting**
   - MUST implement per-source rate limiting
   - Use token bucket or sliding window algorithm
   - Respect robots.txt and crawl-delay
   - Implement exponential backoff on rate limit errors

3. **Error Handling**
   - MUST return typed errors (NotFound, RateLimit, NetworkError, ParseError)
   - MUST NOT panic - recover and log
   - MUST retry with backoff (max 3 retries)
   - Log all errors with context

4. **User Agent**
   - MUST use identifiable User-Agent with contact info
   - Format: `IssueTracker/1.0 (+https://example.com/bot) Go-http-client`
   - Allow configuration per source if needed

## HTTP Client Standards

### Client Configuration
```go
// Use a single HTTP client with connection pooling
var defaultTransport = &http.Transport{
    MaxIdleConns:        100,
    MaxIdleConnsPerHost: 10,
    IdleConnTimeout:     90 * time.Second,
    DisableCompression:  false,
    ForceAttemptHTTP2:   true,
}

// Apply timeouts at request level, not client level
```

### Request Best Practices

1. **Headers**
   - MUST set User-Agent
   - MUST set Accept-Language based on target country
   - SHOULD set Accept-Encoding for compression
   - SHOULD set Referer when following links

2. **Response Handling**
   - MUST check Content-Type before parsing
   - MUST limit response body size (default 10MB)
   - MUST close response bodies
   - Use `io.LimitReader` to prevent memory issues

3. **Encoding**
   - Auto-detect charset from Content-Type or HTML meta
   - Convert all text to UTF-8
   - Use `golang.org/x/text/encoding` for conversion

## Country-Specific Crawlers

### US News Sources (Priority Order)

1. **Major News Outlets**
   - CNN, New York Times, Washington Post
   - RSS feeds preferred over HTML scraping
   - Category: Politics, World, Business, Technology

2. **Regional News**
   - AP News, Reuters (wire services)
   - Local newspaper aggregation

3. **Communities**
   - Reddit: r/news, r/worldnews, r/politics
   - Hacker News: top stories

### South Korea Sources (Priority Order)

1. **Major News Portals**
   - Naver News, Daum News
   - Use official APIs where available
   - Parse portal aggregation pages

2. **Direct News Sources**
   - Yonhap, KBS, MBC, SBS
   - Chosun, JoongAng, Dong-A

3. **Communities**
   - Naver Cafe (selected political/social cafes)
   - DCInside galleries (major galleries only)
   - TheQoo, Clien (popular communities)

## Parsing Strategy

### HTML Parsing

1. **Library**: Use `github.com/PuerkitoBio/goquery`
   - jQuery-like syntax
   - Efficient CSS selectors

2. **Selectors**
   - Store selectors in configuration, not code
   - Version selectors (detect page changes)
   - Implement fallback selectors

3. **Content Extraction**
   ```go
   type ContentExtractor struct {
       TitleSelector    string
       BodySelector     string
       DateSelector     string
       AuthorSelector   string
       CategorySelector string
   }
   ```

4. **Data Cleaning**
   - Remove scripts, styles, ads
   - Normalize whitespace
   - Extract main content only (use readability algorithms)

### RSS/Atom Feeds

1. **Library**: Use `github.com/mmcdole/gofeed`
2. **Refresh Interval**: 15-60 minutes based on source
3. **Deduplication**: Check GUID/Link before processing

### API-Based Sources

1. **Prefer Official APIs**
   - More reliable than scraping
   - Better rate limit management
   - Structured data

2. **API Client Pattern**
   ```go
   type APIClient interface {
       Authenticate(ctx context.Context) error
       FetchArticles(ctx context.Context, params QueryParams) ([]Article, error)
       GetRateLimit(ctx context.Context) (*RateLimit, error)
   }
   ```

## Data Models

### Raw Content Model
```go
type RawContent struct {
    ID          string    // UUID
    SourceInfo  SourceInfo
    FetchedAt   time.Time
    URL         string
    HTML        string    // Original HTML
    StatusCode  int
    Headers     map[string]string
    Metadata    map[string]interface{}
}
```

### Parsed Article Model
```go
type Article struct {
    ID           string
    SourceID     string
    Country      string
    Language     string

    // Content
    Title        string
    Body         string
    Summary      string

    // Metadata
    Author       string
    PublishedAt  time.Time
    UpdatedAt    *time.Time
    Category     string
    Tags         []string

    // Technical
    URL          string
    CanonicalURL string
    ImageURLs    []string

    // Quality
    ContentHash  string // For deduplication
    WordCount    int

    CreatedAt    time.Time
}
```

## Crawler Types

### 1. RSS Crawler
- Poll RSS feeds at intervals
- Parse feed items
- Follow links if needed for full content
- Store feed metadata (etag, last-modified)

### 2. Sitemap Crawler
- Parse XML sitemaps
- Prioritize by lastmod
- Filter by news sitemap tags

### 3. HTML Crawler
- Navigate category/section pages
- Extract article links
- Parse individual articles
- Handle pagination

### 4. API Crawler
- Use official APIs
- Handle authentication/tokens
- Respect rate limits strictly

## Anti-Bot Handling

### Detection Avoidance

1. **Request Patterns**
   - Randomize delays between requests (within limits)
   - Vary request order
   - Don't crawl in strict alphabetical/sequential order

2. **Browser Emulation** (Use sparingly)
   - For JavaScript-heavy sites use headless browser
   - Library: `github.com/chromedp/chromedp`
   - Expensive - cache aggressively

3. **Proxy Rotation** (Future)
   - Design with proxy support in mind
   - Interface for proxy providers
   - Not implemented initially

### CAPTCHA Handling

- Log CAPTCHA encounters
- Implement fallback strategies
- Consider human-in-the-loop for critical sources
- DO NOT use CAPTCHA solving services (ethical concerns)

## Scheduling and Job Management

### Job Types

1. **Periodic Jobs**
   - RSS feed polls: Every 15-30 min
   - Sitemap checks: Every 6-12 hours
   - Full crawls: Daily

2. **Triggered Jobs**
   - Breaking news alerts
   - Keyword-based crawls
   - Backfill operations

### Job Queue Pattern with Kafka

```go
type CrawlJob struct {
    ID          string
    CrawlerName string
    Target      Target
    Priority    int
    ScheduledAt time.Time
    Timeout     time.Duration
    Retry       RetryPolicy
    Partition   int    // Kafka partition for ordering
}

type Target struct {
    URL      string
    Type     TargetType // Feed, Sitemap, Article, Category
    Metadata map[string]interface{}
}
```

### Kafka Topic Structure

1. **Topic Naming Convention**
   ```
   issuetracker.crawl.{priority}      # crawl-high, crawl-normal, crawl-low
   issuetracker.raw.{country}         # raw-us, raw-kr
   issuetracker.processing.stage      # processing stages
   issuetracker.dlq                   # Dead letter queue
   ```

2. **Partitioning Strategy**
   - Partition by source domain for ordering
   - Enables parallel processing per source
   - Maintains crawl order within same source

   ```go
   // Partition key: domain name
   func getPartitionKey(url string) string {
       u, _ := url.Parse(url)
       return u.Host  // e.g., "cnn.com"
   }
   ```

3. **Message Format**
   ```go
   type KafkaMessage struct {
       Key       string                 // Partition key
       Value     []byte                 // JSON serialized job
       Headers   map[string]string      // Metadata
       Timestamp time.Time
   }

   // Headers include:
   // - source: crawler name
   // - country: US/KR
   // - type: feed/article/sitemap
   // - retry-count: number of retries
   ```

### Concurrency Control

1. **Kafka Consumer Groups**
   - Each worker type forms a consumer group
   - Kafka handles load balancing automatically
   - Multiple instances auto-scale

   ```go
   // Consumer group configuration
   config := &kafka.ConfigMap{
       "bootstrap.servers": "localhost:9092",
       "group.id":          "issuetracker-crawler-workers",
       "auto.offset.reset": "earliest",
       "enable.auto.commit": false,  // Manual commit after processing
   }
   ```

2. **Worker Pools per Consumer**
   - Each consumer instance runs multiple goroutines
   - Process messages concurrently within instance
   - Configurable worker count per instance

   ```go
   type KafkaConsumerPool struct {
       consumer    *kafka.Consumer
       workerCount int
       jobs        chan *kafka.Message
       wg          sync.WaitGroup
   }

   func (p *KafkaConsumerPool) Start(ctx context.Context) {
       // Start worker goroutines
       for i := 0; i < p.workerCount; i++ {
           p.wg.Add(1)
           go p.worker(ctx)
       }

       // Poll messages and distribute to workers
       go p.pollMessages(ctx)
   }
   ```

3. **Semaphores per Domain**
   - Limit concurrent requests per domain
   - Prevent overwhelming sources
   - Use local or Redis-based semaphores

4. **Distributed Locking**
   - Use Redis for distributed locks
   - Prevent duplicate crawls across instances
   - Lock key format: `lock:crawl:{url_hash}`

5. **Graceful Shutdown**
   ```go
   func (p *KafkaConsumerPool) Stop(ctx context.Context) error {
       // Stop accepting new messages
       close(p.jobs)

       // Wait for in-flight jobs with timeout
       done := make(chan struct{})
       go func() {
           p.wg.Wait()
           close(done)
       }()

       select {
       case <-done:
           log.Info().Msg("all workers finished")
       case <-ctx.Done():
           log.Warn().Msg("shutdown timeout, force closing")
       }

       return p.consumer.Close()
   }
   ```

## Storage Strategy

### Raw Data Storage

1. **Database**: PostgreSQL
   - Metadata and structured fields
   - Indexes on: source, country, published_at, url_hash

2. **Object Storage**: S3-compatible
   - Original HTML/JSON responses
   - Organized by: `{country}/{source}/{date}/{id}.html`

### Deduplication

1. **URL Normalization**
   - Remove tracking parameters
   - Normalize protocol, www, trailing slashes
   - Use canonical URLs when available

2. **Content Hashing**
   - SHA-256 of normalized content
   - Check before inserting
   - Update if content changed significantly

3. **Fuzzy Matching**
   - Use MinHash or SimHash for near-duplicates
   - Apply at processing stage, not crawl stage

## Monitoring and Metrics

### Key Metrics to Track

1. **Crawler Health**
   - Success rate per source
   - Average response time
   - Error rate by type
   - Items fetched per hour

2. **Data Quality**
   - Parse success rate
   - Empty/invalid content rate
   - Duplicate rate

3. **System Health**
   - Queue depth
   - Worker utilization
   - Memory/CPU usage

### Alerting Conditions

- Source down for > 1 hour
- Parse success rate < 80%
- Queue backup > 10,000 jobs
- Error rate > 5% over 15 minutes

## Code Organization

### Per-Source Modules

```
internal/crawler/news/us/
├── cnn/
│   ├── crawler.go      # Implements Crawler interface
│   ├── parser.go       # HTML parsing logic
│   ├── config.go       # Source configuration
│   └── crawler_test.go
├── nytimes/
│   └── ...
└── registry.go         # Register all US news crawlers
```

### Common Patterns

1. **Builder Pattern** for crawler construction
2. **Strategy Pattern** for parsing logic
3. **Repository Pattern** for data access
4. **Factory Pattern** for crawler instantiation
