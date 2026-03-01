package storage_test

import (
  "context"
  "time"

  "github.com/stretchr/testify/mock"

  "issuetracker/internal/crawler/core"
  "issuetracker/internal/storage"
)

// MockRawContentRepository는 RawContentRepository 인터페이스의 mock 구현체입니다.
type MockRawContentRepository struct {
  mock.Mock
}

func (m *MockRawContentRepository) Save(ctx context.Context, raw *core.RawContent) error {
  args := m.Called(ctx, raw)
  return args.Error(0)
}

func (m *MockRawContentRepository) GetByID(ctx context.Context, id string) (*core.RawContent, error) {
  args := m.Called(ctx, id)
  if args.Get(0) == nil {
    return nil, args.Error(1)
  }
  return args.Get(0).(*core.RawContent), args.Error(1)
}

func (m *MockRawContentRepository) GetByURL(ctx context.Context, url string) (*core.RawContent, error) {
  args := m.Called(ctx, url)
  if args.Get(0) == nil {
    return nil, args.Error(1)
  }
  return args.Get(0).(*core.RawContent), args.Error(1)
}

func (m *MockRawContentRepository) List(ctx context.Context, filter storage.RawContentFilter) ([]*core.RawContent, error) {
  args := m.Called(ctx, filter)
  if args.Get(0) == nil {
    return nil, args.Error(1)
  }
  return args.Get(0).([]*core.RawContent), args.Error(1)
}

func (m *MockRawContentRepository) Delete(ctx context.Context, id string) error {
  args := m.Called(ctx, id)
  return args.Error(0)
}

func (m *MockRawContentRepository) DeleteBefore(ctx context.Context, before time.Time) (int64, error) {
  args := m.Called(ctx, before)
  return args.Get(0).(int64), args.Error(1)
}
