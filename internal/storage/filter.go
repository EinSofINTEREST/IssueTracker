package storage

import "time"

// Pagination은 offset 기반 페이지네이션 파라미터를 나타냅니다.
// Limit가 0이면 repository 구현체에서 기본값(50)으로 처리합니다.
type Pagination struct {
  Limit  int
  Offset int
}

// DefaultPagination은 기본 페이지네이션 설정(limit=50, offset=0)을 반환합니다.
func DefaultPagination() Pagination {
  return Pagination{Limit: 50, Offset: 0}
}

// ArticleFilter는 Article 조회 조건을 나타냅니다.
// 제로값 필드는 WHERE 조건에서 제외됩니다.
type ArticleFilter struct {
  Country  string // ISO 3166-1 alpha-2 (예: "US", "KR")
  Language string // ISO 639-1 (예: "en", "ko")
  Category string
  Source   string // source_id 기반 필터

  PublishedAfter  *time.Time
  PublishedBefore *time.Time

  Tags         []string // 해당 태그를 모두 포함하는 기사 (AND 조건)
  MinWordCount int

  Pagination Pagination
}

// RawContentFilter는 RawContent 조회 조건을 나타냅니다.
// 제로값 필드는 WHERE 조건에서 제외됩니다.
type RawContentFilter struct {
  Country    string
  SourceName string

  FetchedAfter  *time.Time
  FetchedBefore *time.Time
  StatusCode    int // 0이면 무시

  Pagination Pagination
}
