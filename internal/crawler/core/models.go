package core

import "time"

// SourceType은 데이터 소스의 타입을 나타냅니다.
type SourceType string

const (
  SourceTypeNews      SourceType = "news"
  SourceTypeCommunity SourceType = "community"
  SourceTypeSocial    SourceType = "social"
)

// SourceInfo는 데이터 소스의 정보를 담고 있습니다.
type SourceInfo struct {
  Country  string     // ISO 3166-1 alpha-2 (US, KR)
  Type     SourceType // News, Community, Social
  Name     string
  BaseURL  string
  Language string // ISO 639-1 (en, ko)
}

// TargetType은 크롤링 대상의 타입을 나타냅니다.
type TargetType string

const (
  TargetTypeFeed     TargetType = "feed"
  TargetTypeSitemap  TargetType = "sitemap"
  TargetTypeArticle  TargetType = "article"
  TargetTypeCategory TargetType = "category"
)

// Target은 크롤링 대상을 나타냅니다.
type Target struct {
  URL      string
  Type     TargetType
  Metadata map[string]interface{}
}

// RawContent는 크롤링한 원본 데이터를 나타냅니다.
type RawContent struct {
  ID         string
  SourceInfo SourceInfo
  FetchedAt  time.Time
  URL        string
  HTML       string
  StatusCode int
  Headers    map[string]string
  Metadata   map[string]interface{}
}

// Content는 파싱된 컨텐츠 데이터를 나타냅니다.
// 뉴스 기사, 커뮤니티 게시글, 소셜 미디어 포스트 등을 포함합니다.
// DB 저장 시 3개 테이블로 분리됩니다:
//   - contents: 핵심 메타데이터 (핫 경로)
//   - content_bodies: 본문 텍스트 (상세 조회 전용)
//   - content_meta: 확장 메타데이터 (파이프라인 업데이트)
type Content struct {
  // Identity
  ID         string
  SourceID   string
  SourceType SourceType // "news" | "community" | "social"
  Country    string
  Language   string

  // Content (content_bodies 테이블)
  Title   string
  Body    string    // 상세 조회 시에만 채워짐
  Summary string

  // Metadata
  Author      string
  PublishedAt time.Time
  UpdatedAt   *time.Time
  Category    string
  Tags        []string

  // Technical
  URL          string
  CanonicalURL string
  ImageURLs    []string // content_meta 테이블

  // Quality
  ContentHash string
  WordCount   int     // content_bodies 테이블
  Reliability float32 // 신뢰도 0.0~1.0 (0.0: 미검증)

  // Extension (content_meta 테이블)
  Extra map[string]interface{} // 유형별 메타데이터 (JSONB)

  CreatedAt time.Time
}

// Config는 크롤러 설정을 나타냅니다.
type Config struct {
  // HTTP Client 설정
  Timeout         time.Duration
  MaxIdleConns    int
  MaxConnsPerHost int
  UserAgent       string

  // Rate Limiting
  RequestsPerHour int
  BurstSize       int

  // Retry 설정
  MaxRetries   int
  RetryBackoff time.Duration

  // Source 설정
  SourceInfo SourceInfo
}

// DefaultConfig는 기본 크롤러 설정을 반환합니다.
func DefaultConfig() Config {
  return Config{
    Timeout:         30 * time.Second,
    MaxIdleConns:    100,
    MaxConnsPerHost: 10,
    UserAgent:       "EcoScrapper/1.0 (+https://example.com/bot) Go-http-client",
    RequestsPerHour: 100,
    BurstSize:       10,
    MaxRetries:      3,
    RetryBackoff:    1 * time.Second,
  }
}
