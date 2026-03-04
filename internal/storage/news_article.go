// storage 패키지는 데이터 접근 계층의 인터페이스와 공유 타입을 정의합니다.
// 구현체는 하위 패키지(postgres/)에 위치합니다.
package storage

import (
	"context"
	"time"
)

// NewsArticleRecord는 news_articles 테이블의 단일 행을 나타냅니다.
//
// NewsArticleRecord represents a single row in the news_articles table.
type NewsArticleRecord struct {
	ID          string
	SourceName  string // 크롤러 도메인: naver, yonhap
	SourceType  string // news, community, social
	Country     string // ISO 3166-1 alpha-2: KR, US
	Language    string // ISO 639-1: ko, en
	URL         string
	Title       string
	Body        string
	Summary     string
	Author      string
	Category    string
	Tags        []string
	ImageURLs   []string
	PublishedAt *time.Time // nil: 발행 시각 불명
	FetchedAt   time.Time
	CreatedAt   time.Time
}

// NewsArticleRepository는 news_articles 테이블에 대한 데이터 접근 인터페이스입니다.
// 모든 구현체는 goroutine-safe해야 합니다.
// pgx/v5 구현체: internal/storage/postgres/news_article.go
//
// NewsArticleRepository is the data access interface for the news_articles table.
type NewsArticleRepository interface {
	// Insert는 기사를 저장합니다.
	// URL이 이미 존재하는 경우 ON CONFLICT DO NOTHING으로 건너뜁니다 (nil 반환).
	Insert(ctx context.Context, article *NewsArticleRecord) error

	// GetByURL은 URL로 기사를 조회합니다.
	// 존재하지 않으면 ErrNotFound를 반환합니다.
	GetByURL(ctx context.Context, url string) (*NewsArticleRecord, error)

	// List는 필터 조건에 맞는 기사 목록을 published_at DESC 순으로 반환합니다.
	// Limit이 0이면 기본값(50)을 사용합니다.
	List(ctx context.Context, filter NewsArticleFilter) ([]*NewsArticleRecord, error)
}

// NewsArticleFilter는 List 조회 시 사용하는 필터 조건입니다.
//
// NewsArticleFilter holds optional filter criteria for listing news articles.
type NewsArticleFilter struct {
	Country    string // 빈 문자열이면 전체
	SourceName string // 빈 문자열이면 전체
	Limit      int    // 0이면 기본값(50) 적용
	Offset     int
}
