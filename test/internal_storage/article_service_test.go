package storage_test

import (
  "context"
  "errors"
  "testing"
  "time"

  "github.com/stretchr/testify/assert"
  "github.com/stretchr/testify/mock"

  "ecoscrapper/internal/crawler/core"
  "ecoscrapper/internal/storage"
  "ecoscrapper/internal/storage/service"
  "ecoscrapper/pkg/logger"
)

// newTestLogger는 테스트용 no-op logger를 반환합니다.
func newTestLogger() *logger.Logger {
  cfg := logger.DefaultConfig()
  cfg.Level = logger.LevelError // 테스트 중 로그 억제
  return logger.New(cfg)
}

// newTestArticle은 테스트용 기본 Article을 반환합니다.
func newTestArticle() *core.Article {
  return &core.Article{
    ID:          "article-001",
    SourceID:    "source-001",
    Country:     "US",
    Language:    "en",
    Title:       "Test Article",
    Body:        "Test body content with enough words for validation.",
    URL:         "https://example.com/article/001",
    ContentHash: "abc123hash",
    WordCount:   8,
    PublishedAt: time.Now(),
    CreatedAt:   time.Now(),
  }
}

// ─────────────────────────────────────────────────────────────────────────────
// Store 테스트
// ─────────────────────────────────────────────────────────────────────────────

func TestArticleService_Store_NewArticle_SavesAndReturnsID(t *testing.T) {
  repo := new(MockArticleRepository)
  svc := service.NewArticleService(repo, newTestLogger())
  article := newTestArticle()

  // GetByContentHash → ErrNotFound (새 기사)
  repo.On("GetByContentHash", mock.Anything, article.ContentHash).
    Return(nil, storage.ErrNotFound)
  // Save 성공
  repo.On("Save", mock.Anything, article).Return(nil)

  id, isDuplicate, err := svc.Store(context.Background(), article)

  assert.NoError(t, err)
  assert.Equal(t, article.ID, id)
  assert.False(t, isDuplicate)
  repo.AssertExpectations(t)
}

func TestArticleService_Store_DuplicateByContentHash_ReturnsDuplicate(t *testing.T) {
  repo := new(MockArticleRepository)
  svc := service.NewArticleService(repo, newTestLogger())
  article := newTestArticle()

  existing := &core.Article{ID: "existing-001", ContentHash: article.ContentHash}
  repo.On("GetByContentHash", mock.Anything, article.ContentHash).
    Return(existing, nil)

  id, isDuplicate, err := svc.Store(context.Background(), article)

  assert.NoError(t, err)
  assert.Equal(t, existing.ID, id)
  assert.True(t, isDuplicate)
  // Save는 호출되지 않아야 함
  repo.AssertNotCalled(t, "Save", mock.Anything, mock.Anything)
  repo.AssertExpectations(t)
}

func TestArticleService_Store_EmptyContentHash_SkipsDedup(t *testing.T) {
  repo := new(MockArticleRepository)
  svc := service.NewArticleService(repo, newTestLogger())
  article := newTestArticle()
  article.ContentHash = "" // ContentHash 없음

  repo.On("Save", mock.Anything, article).Return(nil)

  id, isDuplicate, err := svc.Store(context.Background(), article)

  assert.NoError(t, err)
  assert.Equal(t, article.ID, id)
  assert.False(t, isDuplicate)
  // GetByContentHash는 호출되지 않아야 함
  repo.AssertNotCalled(t, "GetByContentHash", mock.Anything, mock.Anything)
  repo.AssertExpectations(t)
}

func TestArticleService_Store_GetByContentHashError_ReturnsError(t *testing.T) {
  repo := new(MockArticleRepository)
  svc := service.NewArticleService(repo, newTestLogger())
  article := newTestArticle()

  dbErr := errors.New("connection refused")
  repo.On("GetByContentHash", mock.Anything, article.ContentHash).
    Return(nil, dbErr)

  id, isDuplicate, err := svc.Store(context.Background(), article)

  assert.Error(t, err)
  assert.ErrorIs(t, err, dbErr)
  assert.Empty(t, id)
  assert.False(t, isDuplicate)
  repo.AssertExpectations(t)
}

func TestArticleService_Store_SaveError_ReturnsError(t *testing.T) {
  repo := new(MockArticleRepository)
  svc := service.NewArticleService(repo, newTestLogger())
  article := newTestArticle()

  saveErr := errors.New("db write failed")
  repo.On("GetByContentHash", mock.Anything, article.ContentHash).
    Return(nil, storage.ErrNotFound)
  repo.On("Save", mock.Anything, article).Return(saveErr)

  id, isDuplicate, err := svc.Store(context.Background(), article)

  assert.Error(t, err)
  assert.ErrorIs(t, err, saveErr)
  assert.Empty(t, id)
  assert.False(t, isDuplicate)
  repo.AssertExpectations(t)
}

// ─────────────────────────────────────────────────────────────────────────────
// StoreBatch 테스트
// ─────────────────────────────────────────────────────────────────────────────

func TestArticleService_StoreBatch_MixedResults_ReturnsPerItemResults(t *testing.T) {
  repo := new(MockArticleRepository)
  svc := service.NewArticleService(repo, newTestLogger())

  newArticle := newTestArticle()
  newArticle.ID = "new-001"
  newArticle.ContentHash = "hash-new"

  dupArticle := newTestArticle()
  dupArticle.ID = "dup-002"
  dupArticle.ContentHash = "hash-dup"

  existing := &core.Article{ID: "existing-dup", ContentHash: "hash-dup"}

  // newArticle → 새 기사
  repo.On("GetByContentHash", mock.Anything, newArticle.ContentHash).
    Return(nil, storage.ErrNotFound)
  repo.On("Save", mock.Anything, newArticle).Return(nil)

  // dupArticle → 중복
  repo.On("GetByContentHash", mock.Anything, dupArticle.ContentHash).
    Return(existing, nil)

  results, err := svc.StoreBatch(context.Background(), []*core.Article{newArticle, dupArticle})

  assert.NoError(t, err)
  assert.Len(t, results, 2)

  assert.Equal(t, newArticle.ID, results[0].ArticleID)
  assert.False(t, results[0].IsDuplicate)
  assert.NoError(t, results[0].Err)

  assert.Equal(t, existing.ID, results[1].ArticleID)
  assert.True(t, results[1].IsDuplicate)
  assert.NoError(t, results[1].Err)

  repo.AssertExpectations(t)
}

func TestArticleService_StoreBatch_Empty_ReturnsEmptyResults(t *testing.T) {
  repo := new(MockArticleRepository)
  svc := service.NewArticleService(repo, newTestLogger())

  results, err := svc.StoreBatch(context.Background(), []*core.Article{})

  assert.NoError(t, err)
  assert.Empty(t, results)
  repo.AssertNotCalled(t, "Save", mock.Anything, mock.Anything)
}

// ─────────────────────────────────────────────────────────────────────────────
// GetByID 테스트
// ─────────────────────────────────────────────────────────────────────────────

func TestArticleService_GetByID_Exists_ReturnsArticle(t *testing.T) {
  repo := new(MockArticleRepository)
  svc := service.NewArticleService(repo, newTestLogger())
  article := newTestArticle()

  repo.On("GetByID", mock.Anything, article.ID).Return(article, nil)

  result, err := svc.GetByID(context.Background(), article.ID)

  assert.NoError(t, err)
  assert.Equal(t, article.ID, result.ID)
  repo.AssertExpectations(t)
}

func TestArticleService_GetByID_NotFound_ReturnsError(t *testing.T) {
  repo := new(MockArticleRepository)
  svc := service.NewArticleService(repo, newTestLogger())

  repo.On("GetByID", mock.Anything, "missing-id").
    Return(nil, storage.ErrNotFound)

  result, err := svc.GetByID(context.Background(), "missing-id")

  assert.Error(t, err)
  assert.ErrorIs(t, err, storage.ErrNotFound)
  assert.Nil(t, result)
  repo.AssertExpectations(t)
}

// ─────────────────────────────────────────────────────────────────────────────
// ListByCountry 테스트
// ─────────────────────────────────────────────────────────────────────────────

func TestArticleService_ListByCountry_ReturnsArticles(t *testing.T) {
  repo := new(MockArticleRepository)
  svc := service.NewArticleService(repo, newTestLogger())

  articles := []*core.Article{newTestArticle()}
  filter := storage.ArticleFilter{Pagination: storage.Pagination{Limit: 10}}

  expectedFilter := filter
  expectedFilter.Country = "US"
  repo.On("List", mock.Anything, expectedFilter).Return(articles, nil)

  result, err := svc.ListByCountry(context.Background(), "US", filter)

  assert.NoError(t, err)
  assert.Len(t, result, 1)
  repo.AssertExpectations(t)
}

// ─────────────────────────────────────────────────────────────────────────────
// CountByCountry 테스트
// ─────────────────────────────────────────────────────────────────────────────

func TestArticleService_CountByCountry_ReturnsCountMap(t *testing.T) {
  repo := new(MockArticleRepository)
  svc := service.NewArticleService(repo, newTestLogger())

  // US 카운트
  repo.On("Count", mock.Anything, mock.MatchedBy(func(f storage.ArticleFilter) bool {
    return f.Country == "US" && f.PublishedAfter != nil
  })).Return(int64(42), nil)

  // KR 카운트
  repo.On("Count", mock.Anything, mock.MatchedBy(func(f storage.ArticleFilter) bool {
    return f.Country == "KR" && f.PublishedAfter != nil
  })).Return(int64(17), nil)

  counts, err := svc.CountByCountry(context.Background(), 7)

  assert.NoError(t, err)
  assert.Equal(t, int64(42), counts["US"])
  assert.Equal(t, int64(17), counts["KR"])
  repo.AssertExpectations(t)
}

func TestArticleService_CountByCountry_RepoError_ReturnsError(t *testing.T) {
  repo := new(MockArticleRepository)
  svc := service.NewArticleService(repo, newTestLogger())

  dbErr := errors.New("query failed")
  repo.On("Count", mock.Anything, mock.Anything).Return(int64(0), dbErr)

  counts, err := svc.CountByCountry(context.Background(), 7)

  assert.Error(t, err)
  assert.ErrorIs(t, err, dbErr)
  assert.Nil(t, counts)
}
