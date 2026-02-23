package storage_test

import (
  "context"

  "github.com/stretchr/testify/mock"

  "ecoscrapper/internal/crawler/core"
  "ecoscrapper/internal/storage"
)

// MockArticleRepository는 ArticleRepository 인터페이스의 mock 구현체입니다.
type MockArticleRepository struct {
  mock.Mock
}

func (m *MockArticleRepository) Save(ctx context.Context, article *core.Article) error {
  args := m.Called(ctx, article)
  return args.Error(0)
}

func (m *MockArticleRepository) SaveBatch(ctx context.Context, articles []*core.Article) error {
  args := m.Called(ctx, articles)
  return args.Error(0)
}

func (m *MockArticleRepository) GetByID(ctx context.Context, id string) (*core.Article, error) {
  args := m.Called(ctx, id)
  if args.Get(0) == nil {
    return nil, args.Error(1)
  }
  return args.Get(0).(*core.Article), args.Error(1)
}

func (m *MockArticleRepository) GetByURL(ctx context.Context, url string) (*core.Article, error) {
  args := m.Called(ctx, url)
  if args.Get(0) == nil {
    return nil, args.Error(1)
  }
  return args.Get(0).(*core.Article), args.Error(1)
}

func (m *MockArticleRepository) GetByContentHash(ctx context.Context, hash string) (*core.Article, error) {
  args := m.Called(ctx, hash)
  if args.Get(0) == nil {
    return nil, args.Error(1)
  }
  return args.Get(0).(*core.Article), args.Error(1)
}

func (m *MockArticleRepository) List(ctx context.Context, filter storage.ArticleFilter) ([]*core.Article, error) {
  args := m.Called(ctx, filter)
  if args.Get(0) == nil {
    return nil, args.Error(1)
  }
  return args.Get(0).([]*core.Article), args.Error(1)
}

func (m *MockArticleRepository) Count(ctx context.Context, filter storage.ArticleFilter) (int64, error) {
  args := m.Called(ctx, filter)
  return args.Get(0).(int64), args.Error(1)
}

func (m *MockArticleRepository) Delete(ctx context.Context, id string) error {
  args := m.Called(ctx, id)
  return args.Error(0)
}

func (m *MockArticleRepository) ExistsByURL(ctx context.Context, url string) (bool, error) {
  args := m.Called(ctx, url)
  return args.Bool(0), args.Error(1)
}
