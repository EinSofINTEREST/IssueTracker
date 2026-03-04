package storage_test

import (
	"context"

	"github.com/stretchr/testify/mock"

	"issuetracker/internal/crawler/core"
	"issuetracker/internal/storage"
)

// MockContentRepository는 ContentRepository 인터페이스의 mock 구현체입니다.
type MockContentRepository struct {
	mock.Mock
}

func (m *MockContentRepository) Save(ctx context.Context, content *core.Content) error {
	args := m.Called(ctx, content)
	return args.Error(0)
}

func (m *MockContentRepository) SaveBatch(ctx context.Context, contents []*core.Content) error {
	args := m.Called(ctx, contents)
	return args.Error(0)
}

func (m *MockContentRepository) GetByID(ctx context.Context, id string) (*core.Content, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*core.Content), args.Error(1)
}

func (m *MockContentRepository) GetByURL(ctx context.Context, url string) (*core.Content, error) {
	args := m.Called(ctx, url)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*core.Content), args.Error(1)
}

func (m *MockContentRepository) GetByContentHash(ctx context.Context, hash string) (*core.Content, error) {
	args := m.Called(ctx, hash)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*core.Content), args.Error(1)
}

func (m *MockContentRepository) List(ctx context.Context, filter storage.ContentFilter) ([]*core.Content, error) {
	args := m.Called(ctx, filter)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*core.Content), args.Error(1)
}

func (m *MockContentRepository) Count(ctx context.Context, filter storage.ContentFilter) (int64, error) {
	args := m.Called(ctx, filter)
	return args.Get(0).(int64), args.Error(1)
}

func (m *MockContentRepository) Delete(ctx context.Context, id string) error {
	args := m.Called(ctx, id)
	return args.Error(0)
}

func (m *MockContentRepository) ExistsByURL(ctx context.Context, url string) (bool, error) {
	args := m.Called(ctx, url)
	return args.Bool(0), args.Error(1)
}
