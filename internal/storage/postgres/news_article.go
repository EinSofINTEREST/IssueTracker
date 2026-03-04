package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"issuetracker/internal/storage"
	"issuetracker/pkg/logger"
)

// pgNewsArticleRepository는 pgx/v5 기반 NewsArticleRepository 구현체입니다.
type pgNewsArticleRepository struct {
	pool *pgxpool.Pool
	log  *logger.Logger
}

// NewNewsArticleRepository는 pgxpool을 사용하는 NewsArticleRepository를 생성합니다.
//
// NewNewsArticleRepository creates a NewsArticleRepository backed by pgxpool.
func NewNewsArticleRepository(pool *pgxpool.Pool, log *logger.Logger) storage.NewsArticleRepository {
	return &pgNewsArticleRepository{pool: pool, log: log}
}

const sqlInsertNewsArticle = `
INSERT INTO news_articles (
  source_name, source_type, country, language,
  url, title, body, summary, author,
  category, tags, image_urls, published_at, fetched_at
) VALUES (
  $1, $2, $3, $4,
  $5, $6, $7, $8, $9,
  $10, $11, $12, $13, $14
)
ON CONFLICT (url) DO NOTHING
`

// Insert는 기사를 저장합니다. URL이 이미 존재하면 건너뜁니다.
func (r *pgNewsArticleRepository) Insert(ctx context.Context, a *storage.NewsArticleRecord) error {
	tags := a.Tags
	if tags == nil {
		tags = []string{}
	}

	imageURLs := a.ImageURLs
	if imageURLs == nil {
		imageURLs = []string{}
	}

	_, err := r.pool.Exec(ctx, sqlInsertNewsArticle,
		a.SourceName, a.SourceType, a.Country, a.Language,
		a.URL, a.Title, a.Body, a.Summary, a.Author,
		a.Category, tags, imageURLs, a.PublishedAt, a.FetchedAt,
	)
	if err != nil {
		return fmt.Errorf("insert news article: %w", err)
	}

	return nil
}

const sqlGetByURL = `
SELECT
  id, source_name, source_type, country, language,
  url, title, body, summary, author,
  category, tags, image_urls, published_at, fetched_at, created_at
FROM news_articles
WHERE url = $1
`

// GetByURL은 URL로 기사를 조회합니다. 존재하지 않으면 ErrNotFound를 반환합니다.
func (r *pgNewsArticleRepository) GetByURL(ctx context.Context, url string) (*storage.NewsArticleRecord, error) {
	row := r.pool.QueryRow(ctx, sqlGetByURL, url)

	a, err := scanNewsArticle(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, storage.ErrNotFound
		}
		return nil, fmt.Errorf("get news article by url: %w", err)
	}

	return a, nil
}

// List는 필터 조건에 맞는 기사 목록을 published_at DESC 순으로 반환합니다.
func (r *pgNewsArticleRepository) List(ctx context.Context, f storage.NewsArticleFilter) ([]*storage.NewsArticleRecord, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}

	query := `
SELECT
  id, source_name, source_type, country, language,
  url, title, body, summary, author,
  category, tags, image_urls, published_at, fetched_at, created_at
FROM news_articles
WHERE 1=1`

	args := make([]any, 0, 4)
	argIdx := 1

	if f.Country != "" {
		query += fmt.Sprintf(" AND country = $%d", argIdx)
		args = append(args, f.Country)
		argIdx++
	}

	if f.SourceName != "" {
		query += fmt.Sprintf(" AND source_name = $%d", argIdx)
		args = append(args, f.SourceName)
		argIdx++
	}

	query += fmt.Sprintf(
		" ORDER BY published_at DESC NULLS LAST LIMIT $%d OFFSET $%d",
		argIdx, argIdx+1,
	)
	args = append(args, limit, f.Offset)

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list news articles: %w", err)
	}
	defer rows.Close()

	var articles []*storage.NewsArticleRecord
	for rows.Next() {
		a, err := scanNewsArticle(rows)
		if err != nil {
			return nil, fmt.Errorf("scan news article row: %w", err)
		}
		articles = append(articles, a)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate news article rows: %w", err)
	}

	return articles, nil
}

// scanNewsArticle은 pgx Row/Rows에서 NewsArticleRecord를 스캔합니다.
type scanner interface {
	Scan(dest ...any) error
}

func scanNewsArticle(s scanner) (*storage.NewsArticleRecord, error) {
	a := &storage.NewsArticleRecord{}

	err := s.Scan(
		&a.ID, &a.SourceName, &a.SourceType, &a.Country, &a.Language,
		&a.URL, &a.Title, &a.Body, &a.Summary, &a.Author,
		&a.Category, &a.Tags, &a.ImageURLs, &a.PublishedAt, &a.FetchedAt, &a.CreatedAt,
	)
	if err != nil {
		return nil, err
	}

	return a, nil
}
