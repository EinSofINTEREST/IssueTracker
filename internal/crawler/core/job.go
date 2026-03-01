package core

import (
  "encoding/json"
  "time"
)

// Priority는 크롤 job의 우선순위를 나타냅니다.
//
// Priority defines the processing urgency of a crawl job.
// Higher priority jobs are placed in separate Kafka topics for faster processing.
type Priority int

const (
  PriorityHigh   Priority = 1
  PriorityNormal Priority = 2
  PriorityLow    Priority = 3
)

// CrawlJob은 크롤러 워커가 처리할 크롤링 작업을 나타냅니다.
//
// CrawlJob represents a crawling task consumed by worker goroutines from Kafka.
// It is serialized as JSON and published to crawl topics (issuetracker.crawl.*).
type CrawlJob struct {
  ID          string        `json:"id"`
  CrawlerName string        `json:"crawler_name"`
  Target      Target        `json:"target"`
  Priority    Priority      `json:"priority"`
  ScheduledAt time.Time     `json:"scheduled_at"`
  Timeout     time.Duration `json:"timeout"`
  RetryCount  int           `json:"retry_count"`
  MaxRetries  int           `json:"max_retries"`
}

// ProcessingMessage는 파이프라인 처리 단계 간 전달되는 메시지를 나타냅니다.
//
// ProcessingMessage wraps article data as it flows through the Kafka pipeline.
// Each processing stage (normalize → validate → enrich → embed) wraps its output
// in this type before publishing to the next topic.
type ProcessingMessage struct {
  ID         string                 `json:"id"`
  Timestamp  time.Time              `json:"timestamp"`
  Country    string                 `json:"country"`
  Stage      string                 `json:"stage"`
  Data       json.RawMessage        `json:"data"`
  Metadata   map[string]interface{} `json:"metadata"`
  RetryCount int                    `json:"retry_count"`
}

// Marshal은 CrawlJob을 JSON으로 직렬화합니다.
func (j *CrawlJob) Marshal() ([]byte, error) {
  return json.Marshal(j)
}

// UnmarshalCrawlJob은 JSON 바이트에서 CrawlJob을 역직렬화합니다.
func UnmarshalCrawlJob(data []byte) (*CrawlJob, error) {
  var job CrawlJob
  if err := json.Unmarshal(data, &job); err != nil {
    return nil, err
  }

  return &job, nil
}
