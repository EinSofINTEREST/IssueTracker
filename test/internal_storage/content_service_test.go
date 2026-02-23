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

// newTestContent는 테스트용 기본 Content를 반환합니다.
func newTestContent() *core.Content {
  return &core.Content{
    ID:          "content-001",
    SourceID:    "source-001",
    SourceType:  core.SourceTypeNews,
    Country:     "US",
    Language:    "en",
    Title:       "Test Content",
    Body:        "Test body content with enough words for validation.",
    URL:         "https://example.com/content/001",
    ContentHash: "abc123hash",
    WordCount:   8,
    Reliability: 0.8,
    Extra:       map[string]interface{}{},
    PublishedAt: time.Now(),
    CreatedAt:   time.Now(),
  }
}

// ─────────────────────────────────────────────────────────────────────────────
// Store 테스트
// ─────────────────────────────────────────────────────────────────────────────

func TestContentService_Store_NewContent_SavesAndReturnsID(t *testing.T) {
  repo := new(MockContentRepository)
  svc := service.NewContentService(repo, newTestLogger())
  content := newTestContent()

  // GetByContentHash → ErrNotFound (새 컨텐츠)
  repo.On("GetByContentHash", mock.Anything, content.ContentHash).
    Return(nil, storage.ErrNotFound)
  // Save 성공
  repo.On("Save", mock.Anything, content).Return(nil)

  id, isDuplicate, err := svc.Store(context.Background(), content)

  assert.NoError(t, err)
  assert.Equal(t, content.ID, id)
  assert.False(t, isDuplicate)
  repo.AssertExpectations(t)
}

func TestContentService_Store_DuplicateByContentHash_ReturnsDuplicate(t *testing.T) {
  repo := new(MockContentRepository)
  svc := service.NewContentService(repo, newTestLogger())
  content := newTestContent()

  existing := &core.Content{ID: "existing-001", ContentHash: content.ContentHash}
  repo.On("GetByContentHash", mock.Anything, content.ContentHash).
    Return(existing, nil)

  id, isDuplicate, err := svc.Store(context.Background(), content)

  assert.NoError(t, err)
  assert.Equal(t, existing.ID, id)
  assert.True(t, isDuplicate)
  // Save는 호출되지 않아야 함
  repo.AssertNotCalled(t, "Save", mock.Anything, mock.Anything)
  repo.AssertExpectations(t)
}

func TestContentService_Store_EmptyContentHash_SkipsDedup(t *testing.T) {
  repo := new(MockContentRepository)
  svc := service.NewContentService(repo, newTestLogger())
  content := newTestContent()
  content.ContentHash = "" // ContentHash 없음

  repo.On("Save", mock.Anything, content).Return(nil)

  id, isDuplicate, err := svc.Store(context.Background(), content)

  assert.NoError(t, err)
  assert.Equal(t, content.ID, id)
  assert.False(t, isDuplicate)
  // GetByContentHash는 호출되지 않아야 함
  repo.AssertNotCalled(t, "GetByContentHash", mock.Anything, mock.Anything)
  repo.AssertExpectations(t)
}

func TestContentService_Store_GetByContentHashError_ReturnsError(t *testing.T) {
  repo := new(MockContentRepository)
  svc := service.NewContentService(repo, newTestLogger())
  content := newTestContent()

  dbErr := errors.New("connection refused")
  repo.On("GetByContentHash", mock.Anything, content.ContentHash).
    Return(nil, dbErr)

  id, isDuplicate, err := svc.Store(context.Background(), content)

  assert.Error(t, err)
  assert.ErrorIs(t, err, dbErr)
  assert.Empty(t, id)
  assert.False(t, isDuplicate)
  repo.AssertExpectations(t)
}

func TestContentService_Store_SaveError_ReturnsError(t *testing.T) {
  repo := new(MockContentRepository)
  svc := service.NewContentService(repo, newTestLogger())
  content := newTestContent()

  saveErr := errors.New("db write failed")
  repo.On("GetByContentHash", mock.Anything, content.ContentHash).
    Return(nil, storage.ErrNotFound)
  repo.On("Save", mock.Anything, content).Return(saveErr)

  id, isDuplicate, err := svc.Store(context.Background(), content)

  assert.Error(t, err)
  assert.ErrorIs(t, err, saveErr)
  assert.Empty(t, id)
  assert.False(t, isDuplicate)
  repo.AssertExpectations(t)
}

// ─────────────────────────────────────────────────────────────────────────────
// StoreBatch 테스트
// ─────────────────────────────────────────────────────────────────────────────

func TestContentService_StoreBatch_MixedResults_ReturnsPerItemResults(t *testing.T) {
  repo := new(MockContentRepository)
  svc := service.NewContentService(repo, newTestLogger())

  newContent := newTestContent()
  newContent.ID = "new-001"
  newContent.ContentHash = "hash-new"

  dupContent := newTestContent()
  dupContent.ID = "dup-002"
  dupContent.ContentHash = "hash-dup"

  existing := &core.Content{ID: "existing-dup", ContentHash: "hash-dup"}

  // newContent → 새 컨텐츠
  repo.On("GetByContentHash", mock.Anything, newContent.ContentHash).
    Return(nil, storage.ErrNotFound)
  repo.On("Save", mock.Anything, newContent).Return(nil)

  // dupContent → 중복
  repo.On("GetByContentHash", mock.Anything, dupContent.ContentHash).
    Return(existing, nil)

  results, err := svc.StoreBatch(context.Background(), []*core.Content{newContent, dupContent})

  assert.NoError(t, err)
  assert.Len(t, results, 2)

  assert.Equal(t, newContent.ID, results[0].ContentID)
  assert.False(t, results[0].IsDuplicate)
  assert.NoError(t, results[0].Err)

  assert.Equal(t, existing.ID, results[1].ContentID)
  assert.True(t, results[1].IsDuplicate)
  assert.NoError(t, results[1].Err)

  repo.AssertExpectations(t)
}

func TestContentService_StoreBatch_Empty_ReturnsEmptyResults(t *testing.T) {
  repo := new(MockContentRepository)
  svc := service.NewContentService(repo, newTestLogger())

  results, err := svc.StoreBatch(context.Background(), []*core.Content{})

  assert.NoError(t, err)
  assert.Empty(t, results)
  repo.AssertNotCalled(t, "Save", mock.Anything, mock.Anything)
}

// ─────────────────────────────────────────────────────────────────────────────
// GetByID 테스트
// ─────────────────────────────────────────────────────────────────────────────

func TestContentService_GetByID_Exists_ReturnsContent(t *testing.T) {
  repo := new(MockContentRepository)
  svc := service.NewContentService(repo, newTestLogger())
  content := newTestContent()

  repo.On("GetByID", mock.Anything, content.ID).Return(content, nil)

  result, err := svc.GetByID(context.Background(), content.ID)

  assert.NoError(t, err)
  assert.Equal(t, content.ID, result.ID)
  repo.AssertExpectations(t)
}

func TestContentService_GetByID_NotFound_ReturnsError(t *testing.T) {
  repo := new(MockContentRepository)
  svc := service.NewContentService(repo, newTestLogger())

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

func TestContentService_ListByCountry_ReturnsContents(t *testing.T) {
  repo := new(MockContentRepository)
  svc := service.NewContentService(repo, newTestLogger())

  contents := []*core.Content{newTestContent()}
  filter := storage.ContentFilter{Pagination: storage.Pagination{Limit: 10}}

  expectedFilter := filter
  expectedFilter.Country = "US"
  repo.On("List", mock.Anything, expectedFilter).Return(contents, nil)

  result, err := svc.ListByCountry(context.Background(), "US", filter)

  assert.NoError(t, err)
  assert.Len(t, result, 1)
  repo.AssertExpectations(t)
}

// ─────────────────────────────────────────────────────────────────────────────
// CountByCountry 테스트
// ─────────────────────────────────────────────────────────────────────────────

func TestContentService_CountByCountry_ReturnsCountMap(t *testing.T) {
  repo := new(MockContentRepository)
  svc := service.NewContentService(repo, newTestLogger())

  // US 카운트
  repo.On("Count", mock.Anything, mock.MatchedBy(func(f storage.ContentFilter) bool {
    return f.Country == "US" && f.PublishedAfter != nil
  })).Return(int64(42), nil)

  // KR 카운트
  repo.On("Count", mock.Anything, mock.MatchedBy(func(f storage.ContentFilter) bool {
    return f.Country == "KR" && f.PublishedAfter != nil
  })).Return(int64(17), nil)

  counts, err := svc.CountByCountry(context.Background(), 7)

  assert.NoError(t, err)
  assert.Equal(t, int64(42), counts["US"])
  assert.Equal(t, int64(17), counts["KR"])
  repo.AssertExpectations(t)
}

func TestContentService_CountByCountry_RepoError_ReturnsError(t *testing.T) {
  repo := new(MockContentRepository)
  svc := service.NewContentService(repo, newTestLogger())

  dbErr := errors.New("query failed")
  repo.On("Count", mock.Anything, mock.Anything).Return(int64(0), dbErr)

  counts, err := svc.CountByCountry(context.Background(), 7)

  assert.Error(t, err)
  assert.ErrorIs(t, err, dbErr)
  assert.Nil(t, counts)
}
