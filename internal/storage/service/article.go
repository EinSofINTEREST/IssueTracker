// service 패키지는 repository 인터페이스 위에 비즈니스 로직을 제공합니다.
// 중복 감지, 필터링 등 순수 CRUD 이상의 로직을 담당합니다.
package service

import (
  "context"
  "errors"
  "fmt"
  "time"

  "ecoscrapper/internal/crawler/core"
  "ecoscrapper/internal/storage"
  "ecoscrapper/pkg/logger"
)

// StoreResult는 StoreBatch에서 각 Article의 저장 결과를 나타냅니다.
type StoreResult struct {
  ArticleID   string
  IsDuplicate bool
  Err         error
}

// ArticleService는 Article에 대한 비즈니스 로직 인터페이스입니다.
// 모든 구현체는 goroutine-safe해야 합니다.
type ArticleService interface {
  // Store는 중복 감지를 포함하여 article을 저장합니다.
  // ContentHash가 동일한 기사가 이미 존재하면 저장하지 않고 기존 ID를 반환합니다.
  // 반환값: (id, isDuplicate, error)
  Store(ctx context.Context, article *core.Article) (id string, isDuplicate bool, err error)

  // StoreBatch는 여러 article을 항목별로 중복 감지하며 저장합니다.
  StoreBatch(ctx context.Context, articles []*core.Article) ([]StoreResult, error)

  // GetByID는 ID로 article을 조회합니다.
  GetByID(ctx context.Context, id string) (*core.Article, error)

  // ListByCountry는 특정 국가의 article을 최신순으로 반환합니다.
  ListByCountry(ctx context.Context, country string, filter storage.ArticleFilter) ([]*core.Article, error)

  // Search는 다양한 조건으로 article을 검색합니다.
  Search(ctx context.Context, filter storage.ArticleFilter) ([]*core.Article, error)

  // CountByCountry는 최근 N일간 국가별 article 수를 반환합니다.
  CountByCountry(ctx context.Context, days int) (map[string]int64, error)
}

// articleService는 ArticleService의 구현체입니다.
type articleService struct {
  repo storage.ArticleRepository
  log  *logger.Logger
}

// NewArticleService는 주어진 repository를 사용하는 ArticleService를 생성합니다.
func NewArticleService(repo storage.ArticleRepository, log *logger.Logger) ArticleService {
  return &articleService{repo: repo, log: log}
}

// Store는 중복 감지 후 article을 저장합니다.
//
// 중복 감지 순서:
//  1. ContentHash가 비어있지 않으면 GetByContentHash로 중복 확인
//  2. 기존 레코드 있으면 (existingID, true, nil) 반환
//  3. ErrNotFound면 repo.Save 후 (article.ID, false, nil) 반환
func (s *articleService) Store(ctx context.Context, article *core.Article) (string, bool, error) {
  // ContentHash 기반 중복 감지 (비어있으면 생략)
  if article.ContentHash != "" {
    existing, err := s.repo.GetByContentHash(ctx, article.ContentHash)
    if err == nil {
      // 동일 content_hash 존재 → 중복
      s.log.WithFields(map[string]interface{}{
        "existing_id":  existing.ID,
        "content_hash": article.ContentHash,
      }).Debug("duplicate article detected by content hash")
      return existing.ID, true, nil
    }

    if !errors.Is(err, storage.ErrNotFound) {
      return "", false, fmt.Errorf("check duplicate: %w", err)
    }
  }

  if err := s.repo.Save(ctx, article); err != nil {
    return "", false, fmt.Errorf("save article: %w", err)
  }

  return article.ID, false, nil
}

// StoreBatch는 각 article에 대해 독립적으로 중복 감지 후 저장합니다.
func (s *articleService) StoreBatch(ctx context.Context, articles []*core.Article) ([]StoreResult, error) {
  results := make([]StoreResult, 0, len(articles))

  for _, article := range articles {
    id, isDuplicate, err := s.Store(ctx, article)
    results = append(results, StoreResult{
      ArticleID:   id,
      IsDuplicate: isDuplicate,
      Err:         err,
    })
  }

  return results, nil
}

// GetByID는 ID로 article을 조회합니다.
func (s *articleService) GetByID(ctx context.Context, id string) (*core.Article, error) {
  article, err := s.repo.GetByID(ctx, id)
  if err != nil {
    return nil, fmt.Errorf("get article by id: %w", err)
  }

  return article, nil
}

// ListByCountry는 특정 국가의 article을 필터와 함께 조회합니다.
func (s *articleService) ListByCountry(
  ctx context.Context,
  country string,
  filter storage.ArticleFilter,
) ([]*core.Article, error) {
  filter.Country = country

  articles, err := s.repo.List(ctx, filter)
  if err != nil {
    return nil, fmt.Errorf("list articles by country %s: %w", country, err)
  }

  return articles, nil
}

// Search는 ArticleFilter 조건으로 article을 검색합니다.
func (s *articleService) Search(ctx context.Context, filter storage.ArticleFilter) ([]*core.Article, error) {
  articles, err := s.repo.List(ctx, filter)
  if err != nil {
    return nil, fmt.Errorf("search articles: %w", err)
  }

  return articles, nil
}

// CountByCountry는 최근 N일간 국가별 article 수를 반환합니다.
// 각 알려진 국가에 대해 Count를 호출하여 집계합니다.
func (s *articleService) CountByCountry(ctx context.Context, days int) (map[string]int64, error) {
  after := time.Now().AddDate(0, 0, -days)
  countries := []string{"US", "KR"}

  result := make(map[string]int64, len(countries))

  for _, country := range countries {
    count, err := s.repo.Count(ctx, storage.ArticleFilter{
      Country:        country,
      PublishedAfter: &after,
    })
    if err != nil {
      return nil, fmt.Errorf("count articles for country %s: %w", country, err)
    }

    result[country] = count
  }

  return result, nil
}
