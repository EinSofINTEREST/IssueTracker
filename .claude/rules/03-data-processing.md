# Data Processing and Embedding Rules

## Processing Pipeline

### Pipeline Stages

```
Raw Article → Normalize → Validate → Enrich → Embed → Cluster → Store
```

Each stage MUST be:
- Idempotent (safe to retry)
- Isolated (failure doesn't affect other stages)
- Logged (track data lineage)
- Versioned (schema changes tracked)

## Normalization

### Text Normalization

1. **Language Detection**
   - Use `github.com/pemistahl/lingua-go`
   - Verify detected language matches source language
   - Flag mismatches for review

2. **Encoding**
   - Convert all text to UTF-8
   - Handle mixed encodings
   - Preserve special characters (emoji, symbols)

3. **Content Cleaning**
   ```go
   type TextCleaner interface {
       RemoveHTML(text string) string
       NormalizeWhitespace(text string) string
       RemoveBoilerplate(text string) string
       ExtractMainContent(html string) string
   }
   ```

4. **Cleaning Rules**
   - Remove HTML tags and attributes
   - Normalize unicode (NFC normalization)
   - Remove zero-width characters
   - Collapse multiple newlines/spaces
   - Remove navigation, ads, footers
   - Keep paragraph structure

### Metadata Normalization

1. **Date/Time**
   - Parse various formats to UTC
   - Handle timezone conversions
   - Validate reasonable date ranges (not future, not too old)
   - Use `published_at` as primary, fallback to `crawled_at`

2. **URLs**
   - Normalize to canonical form
   - Remove tracking parameters
   - Validate URL structure
   - Store both original and canonical

3. **Categories/Tags**
   - Map source-specific categories to standard taxonomy
   - Normalize capitalization
   - Remove duplicates
   - Limit to top N tags

### Standard Schema

```go
type NormalizedArticle struct {
    // Identity
    ID              string
    SourceID        string
    CanonicalURL    string

    // Location/Language
    Country         string    // ISO 3166-1 alpha-2
    Language        string    // ISO 639-1
    Region          *string   // Optional: state/province

    // Content
    Title           string
    Body            string    // Plain text, cleaned
    Summary         string    // First paragraph or meta description

    // Metadata
    Author          []string  // Multiple authors
    PublishedAt     time.Time
    ModifiedAt      *time.Time

    // Classification
    Categories      []string  // Normalized categories
    Tags            []string  // Extracted/provided tags

    // Metrics
    WordCount       int
    ReadingTime     int       // Minutes

    // Quality Indicators
    HasImage        bool
    ImageCount      int
    VideoCount      int
    LinkCount       int

    // Processing
    ProcessedAt     time.Time
    SchemaVersion   int
}
```

## Validation

### Content Validation Rules

1. **Required Fields**
   - Title: 10-500 characters
   - Body: 100-50000 characters
   - URL: Valid HTTP(S) URL
   - PublishedAt: Within last 30 days (configurable)
   - Language: Must be non-empty

2. **Quality Checks**
   ```go
   type QualityCheck struct {
       MinWordCount      int  // Default: 50
       MaxWordCount      int  // Default: 10000
       MinTitleLength    int  // Default: 10
       MaxTitleLength    int  // Default: 500
       RequireDate       bool // Default: true
       RequireAuthor     bool // Default: false
   }
   ```

3. **Content Quality Score**
   - Calculate based on:
     - Word count
     - Paragraph structure
     - Presence of metadata
     - Image/media presence
     - Source reputation
   - Range: 0.0-1.0
   - Threshold for processing: 0.5

4. **Spam/Low-Quality Detection**
   - Excessive capitalization (>20%)
   - Excessive punctuation
   - Known spam patterns
   - Duplicate content (>90% similar to existing)
   - Auto-generated content signals

### Validation Output

```go
type ValidationResult struct {
    IsValid       bool
    QualityScore  float64
    Errors        []ValidationError
    Warnings      []ValidationWarning
    Metrics       map[string]interface{}
}

type ValidationError struct {
    Field    string
    Rule     string
    Message  string
    Severity Severity // Error, Warning, Info
}
```

## Enrichment

### Entity Extraction

1. **Named Entity Recognition (NER)**
   - Extract: People, Organizations, Locations, Dates
   - Use: ML-based NER or rule-based for specific entities
   - Language-specific models (English, Korean)

2. **Entity Storage**
   ```go
   type Entity struct {
       Type       EntityType  // Person, Org, Location, Event
       Name       string
       Mentions   int
       Confidence float64
       Positions  []Position  // Where in text
       Metadata   map[string]interface{}
   }
   ```

3. **Entity Linking**
   - Link to knowledge base (Wikipedia, Wikidata)
   - Disambiguate common names
   - Store entity IDs for cross-referencing

### Topic Extraction

1. **Keyword Extraction**
   - TF-IDF for important terms
   - TextRank algorithm
   - Language-specific stopwords
   - Top 10-20 keywords per article

2. **Topic Classification**
   - Predefined taxonomy:
     - Politics (국내정치, 국제정치)
     - Economy (경제, 금융, 부동산)
     - Society (사회, 교육, 복지)
     - Technology (IT, 과학)
     - Culture (문화, 연예, 스포츠)
     - Environment (환경, 기후)
   - Multi-label classification
   - Confidence scores per label

### Sentiment Analysis

1. **Document-Level Sentiment**
   - Positive, Negative, Neutral
   - Confidence score
   - Language-specific models

2. **Aspect-Based Sentiment**
   - Sentiment toward specific entities/topics
   - Extract opinion targets
   - Store sentiment per aspect

### Metadata Enrichment

1. **Geographic Data**
   - Extract mentioned locations
   - Geocode to coordinates
   - Add administrative hierarchy (city → state → country)

2. **Temporal Data**
   - Extract mentioned dates/events
   - Create timeline references
   - Link to historical events

3. **Source Metadata**
   - Source credibility score
   - Political lean (if applicable)
   - Readership demographics

## Embedding Generation

### Text Embedding Strategy

1. **Model Selection**
   - **Multilingual**: Use multilingual models for cross-language similarity
   - **Options**:
     - OpenAI `text-embedding-3-large` (API)
     - `intfloat/multilingual-e5-large` (Self-hosted)
     - `sentence-transformers/paraphrase-multilingual-mpnet-base-v2`
   - Korean-specific: Consider `jhgan/ko-sbert-nli` for Korean text

2. **Embedding Dimensions**
   - Target: 768 or 1024 dimensions
   - Trade-off: accuracy vs storage vs speed
   - Normalize vectors (unit length)

3. **Text Preparation for Embedding**
   ```go
   type EmbeddingInput struct {
       Title    string
       Body     string  // Truncate if needed
       MaxTokens int    // Model-specific limit
   }

   // Combine title and body with appropriate weighting
   func PrepareForEmbedding(article NormalizedArticle) string {
       // Title repeated 2x for emphasis
       return fmt.Sprintf("%s. %s. %s",
           article.Title,
           article.Title,
           truncateTokens(article.Body, 512))
   }
   ```

4. **Batch Processing**
   - Batch size: 16-32 articles per API call
   - Implement retry with exponential backoff
   - Cache embeddings (content hash → embedding)

### Vector Storage

1. **Vector Database Choice**
   - **Primary**: Qdrant
     - Good Go client
     - HNSW index (fast similarity search)
     - Filtering support
     - Self-hostable

2. **Index Structure**
   ```go
   type VectorEntry struct {
       ID         string
       Vector     []float32  // Embedding
       Payload    map[string]interface{} {
           "article_id": string,
           "country": string,
           "language": string,
           "published_at": timestamp,
           "category": []string,
           "quality_score": float,
       }
   }
   ```

3. **Index Configuration**
   - Distance metric: Cosine similarity
   - HNSW parameters:
     - m: 16 (number of connections)
     - ef_construct: 100 (build-time search)
   - Enable payload indexing for filtering

4. **Search Strategy**
   - Hybrid search: Vector + filters
   - Filter by: country, date range, category, quality
   - Return top K with minimum similarity threshold

## Clustering and Issue Detection

### Similarity Clustering

1. **Algorithm**: HDBSCAN or DBSCAN
   - Density-based clustering
   - No need to specify cluster count
   - Handles noise (outliers)

2. **Clustering Pipeline**
   ```
   Time Window → Vector Search → Cluster → Label → Store
   ```

3. **Time-Based Windows**
   - Real-time: Last 24 hours
   - Short-term: Last 7 days
   - Long-term: Last 30 days
   - Re-cluster periodically as new articles arrive

4. **Clustering Parameters**
   ```go
   type ClusterConfig struct {
       MinClusterSize    int     // Minimum articles per cluster
       MinSamples        int     // HDBSCAN parameter
       SimilarityThresh  float64 // Minimum cosine similarity
       TimeWindow        time.Duration
       MaxClusters       int     // Prevent explosion
   }
   ```

### Issue Identification

1. **Cluster Labeling**
   - Extract most common keywords from cluster
   - Use most representative article title
   - Generate summary with LLM (GPT-4 or Claude)
   - Format: "이슈: {핵심 키워드} - {간단한 설명}"

2. **Cluster Metadata**
   ```go
   type IssueCluster struct {
       ID            string
       Label         string
       Summary       string

       // Articles
       ArticleIDs    []string
       ArticleCount  int
       RepresentativeArticleID string  // Centroid

       // Characteristics
       Countries     []string
       Languages     []string
       Dominant      string    // Most common country/lang

       // Temporal
       FirstSeen     time.Time
       LastSeen      time.Time
       PeakTime      time.Time

       // Metrics
       Velocity      float64   // Articles per hour
       Spread        float64   // Geographic/source spread
       Sentiment     Sentiment

       // Classification
       Categories    []string
       Entities      []Entity
       Keywords      []string

       CreatedAt     time.Time
       UpdatedAt     time.Time
   }
   ```

3. **Issue Evolution Tracking**
   - Track cluster changes over time
   - Merge related clusters
   - Split diverging clusters
   - Maintain cluster lineage

4. **Cross-Country Issue Linking**
   - Find similar clusters across countries
   - Use multilingual embeddings
   - Link related issues (same event, different coverage)
   - Score cross-country similarity

### Trending Detection

1. **Velocity Metrics**
   - Article rate: count/hour
   - Acceleration: change in rate
   - Anomaly detection for sudden spikes

2. **Trend Score**
   ```go
   TrendScore = (Velocity * 0.4) +
                (Acceleration * 0.3) +
                (Spread * 0.2) +
                (Quality * 0.1)
   ```

3. **Ranking**
   - Top trending issues per country
   - Top cross-country issues
   - Update every 15 minutes

## Processing Infrastructure

### Pipeline Orchestration with Kafka

1. **Kafka Topic-Based Pipeline**
   ```
   Crawler → [issuetracker.raw.{country}] → Normalizer →
   [issuetracker.normalized] → Validator →
   [issuetracker.validated] → Enricher →
   [issuetracker.enriched] → Embedder →
   [issuetracker.embedded] → Clusterer → [Done]
   ```

2. **Topic Configuration**
   ```go
   var Topics = map[string]TopicConfig{
       "issuetracker.raw.us": {
           Partitions:     16,  // Parallel processing
           ReplicationFactor: 3,
           RetentionMs:    86400000,  // 24 hours
       },
       "issuetracker.raw.kr": {
           Partitions:     16,
           ReplicationFactor: 3,
           RetentionMs:    86400000,
       },
       "issuetracker.normalized": {
           Partitions:     32,
           ReplicationFactor: 3,
           RetentionMs:    172800000,  // 48 hours
       },
       "issuetracker.validated": {
           Partitions:     32,
           ReplicationFactor: 3,
           RetentionMs:    172800000,
       },
       "issuetracker.enriched": {
           Partitions:     32,
           ReplicationFactor: 3,
           RetentionMs:    259200000,  // 72 hours
       },
       "issuetracker.embedded": {
           Partitions:     16,
           ReplicationFactor: 3,
           RetentionMs:    259200000,
       },
       "issuetracker.dlq": {
           Partitions:     8,
           ReplicationFactor: 3,
           RetentionMs:    604800000,  // 7 days
       },
   }
   ```

3. **Consumer Groups per Stage**
   ```go
   // Each processing stage is a consumer group
   var ConsumerGroups = []string{
       "issuetracker-normalizers",
       "issuetracker-validators",
       "issuetracker-enrichers",
       "issuetracker-embedders",
       "issuetracker-clusterers",
   }
   ```

4. **Worker Pools per Consumer**
   - Each consumer group has multiple instances
   - Each instance processes messages concurrently
   - Scale independently based on lag

   ```go
   type ProcessingWorker struct {
       consumer    *kafka.Consumer
       producer    *kafka.Producer
       stage       ProcessingStage
       workerCount int
   }

   func (w *ProcessingWorker) Process(msg *kafka.Message) error {
       // 1. Deserialize
       input, err := w.stage.Deserialize(msg.Value)
       if err != nil {
           return w.sendToDLQ(msg, err)
       }

       // 2. Process
       output, err := w.stage.Process(context.Background(), input)
       if err != nil {
           return w.handleError(msg, err)
       }

       // 3. Produce to next topic
       nextTopic := w.stage.NextTopic()
       return w.produce(nextTopic, output, msg.Key)
   }
   ```

5. **Message Format**
   ```go
   type ProcessingMessage struct {
       ID          string                 `json:"id"`
       Timestamp   time.Time              `json:"timestamp"`
       Country     string                 `json:"country"`
       Stage       string                 `json:"stage"`
       Data        json.RawMessage        `json:"data"`
       Metadata    map[string]interface{} `json:"metadata"`
       RetryCount  int                    `json:"retry_count"`
   }
   ```

6. **Failure Handling with Kafka**
   - **Retry Logic**: Re-publish to same topic with incremented retry count
   - **Dead Letter Queue**: After 3 retries, send to `issuetracker.dlq`
   - **Error Headers**: Include error info in Kafka headers

   ```go
   func (w *ProcessingWorker) handleError(msg *kafka.Message, err error) error {
       retryCount := getRetryCount(msg.Headers)

       if retryCount >= 3 {
           return w.sendToDLQ(msg, err)
       }

       // Re-publish with incremented retry count
       headers := msg.Headers
       headers = append(headers, kafka.Header{
           Key:   "retry-count",
           Value: []byte(fmt.Sprintf("%d", retryCount+1)),
       })
       headers = append(headers, kafka.Header{
           Key:   "last-error",
           Value: []byte(err.Error()),
       })

       return w.producer.Produce(&kafka.Message{
           TopicPartition: kafka.TopicPartition{
               Topic:     msg.TopicPartition.Topic,
               Partition: kafka.PartitionAny,
           },
           Key:     msg.Key,
           Value:   msg.Value,
           Headers: headers,
       }, nil)
   }

   func (w *ProcessingWorker) sendToDLQ(msg *kafka.Message, err error) error {
       dlqMsg := &kafka.Message{
           TopicPartition: kafka.TopicPartition{
               Topic:     "issuetracker.dlq",
               Partition: kafka.PartitionAny,
           },
           Key:   msg.Key,
           Value: msg.Value,
           Headers: append(msg.Headers,
               kafka.Header{Key: "original-topic", Value: []byte(*msg.TopicPartition.Topic)},
               kafka.Header{Key: "error", Value: []byte(err.Error())},
               kafka.Header{Key: "timestamp", Value: []byte(time.Now().Format(time.RFC3339))},
           ),
       }

       return w.producer.Produce(dlqMsg, nil)
   }
   ```

7. **Offset Management**
   - Manual commit after successful processing
   - Commit per batch for performance
   - Enable exactly-once semantics where critical

   ```go
   func (w *ProcessingWorker) processMessages(ctx context.Context) {
       batch := make([]*kafka.Message, 0, 100)
       ticker := time.NewTicker(5 * time.Second)

       for {
           select {
           case <-ctx.Done():
               w.commitBatch(batch)
               return

           case <-ticker.C:
               if len(batch) > 0 {
                   w.commitBatch(batch)
                   batch = batch[:0]
               }

           default:
               msg, err := w.consumer.ReadMessage(100 * time.Millisecond)
               if err != nil {
                   continue
               }

               if err := w.Process(msg); err != nil {
                   log.Error().Err(err).Msg("processing failed")
                   continue
               }

               batch = append(batch, msg)
               if len(batch) >= 100 {
                   w.commitBatch(batch)
                   batch = batch[:0]
               }
           }
       }
   }

   func (w *ProcessingWorker) commitBatch(batch []*kafka.Message) error {
       if len(batch) == 0 {
           return nil
       }

       lastMsg := batch[len(batch)-1]
       _, err := w.consumer.CommitMessage(lastMsg)
       return err
   }
   ```

### Batch vs Stream Processing

1. **Stream Processing** (Real-time)
   - Individual articles as they arrive
   - Immediate embedding and clustering
   - For breaking news and trending topics
   - Kafka Streams for stateful processing

   ```go
   // Example: Real-time clustering with Kafka Streams
   type StreamProcessor struct {
       streams *kstream.StreamBuilder
   }

   func (s *StreamProcessor) BuildPipeline() {
       // Source: embedded articles
       embedded := s.streams.Stream("issuetracker.embedded")

       // Transform and aggregate
       embedded.
           GroupByKey().
           WindowedBy(kstream.TimeWindow(1 * time.Hour)).
           Aggregate(
               func() *Cluster { return NewCluster() },
               func(key string, value *Article, agg *Cluster) *Cluster {
                   return agg.AddArticle(value)
               },
           ).
           ToStream().
           To("issuetracker.clusters")
   }
   ```

2. **Batch Processing** (Scheduled)
   - Full re-clustering daily
   - Entity linking updates
   - Topic model updates
   - Archive old clusters
   - Use Kafka Connect for batch exports

### Performance Optimization

1. **Caching Strategy**
   - Cache embeddings by content hash
   - Cache NER results
   - Cache URL normalizations
   - TTL: 30 days

2. **Database Optimization**
   - Bulk inserts for batch operations
   - Prepared statements
   - Connection pooling
   - Index optimization

3. **Parallel Processing**
   - Process independent articles concurrently
   - Batch embedding generation
   - Parallel validation checks

## Data Quality Monitoring

### Metrics to Track

1. **Processing Metrics**
   - Articles processed per stage
   - Processing time per stage
   - Failure rate per stage
   - Kafka consumer lag per topic
   - Messages in DLQ

2. **Quality Metrics**
   - Average quality score
   - Validation failure reasons
   - Duplicate detection rate
   - Entity extraction success rate

3. **Clustering Metrics**
   - Cluster count over time
   - Average cluster size
   - Outlier rate (unclustered articles)
   - Cluster stability

### Quality Assurance

1. **Sampling and Review**
   - Random sample 0.1% of articles daily
   - Manual review of quality
   - Verify entity extraction accuracy
   - Check clustering coherence

2. **A/B Testing**
   - Test new models/algorithms on subset
   - Compare metrics before rolling out
   - Gradual rollout (canary deployment)

3. **Feedback Loop**
   - Allow marking incorrect clusters
   - Collect user feedback on quality
   - Retrain models with feedback data
