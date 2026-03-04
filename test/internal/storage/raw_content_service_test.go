package storage_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"issuetracker/internal/crawler/core"
	"issuetracker/internal/storage"
	"issuetracker/internal/storage/service"
)

// newTestRawContent는 테스트용 기본 RawContent를 반환합니다.
func newTestRawContent() *core.RawContent {
	return &core.RawContent{
		ID:         "raw-001",
		URL:        "https://example.com/raw/001",
		HTML:       "<html><body>Test content</body></html>",
		StatusCode: 200,
		FetchedAt:  time.Now(),
		SourceInfo: core.SourceInfo{
			Country:  "US",
			Type:     core.SourceTypeNews,
			Name:     "test-source",
			BaseURL:  "https://example.com",
			Language: "en",
		},
		Headers:  map[string]string{"Content-Type": "text/html"},
		Metadata: map[string]interface{}{"crawl_depth": 1},
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Store 테스트
// ─────────────────────────────────────────────────────────────────────────────

func TestRawContentService_Store_NewContent_SavesAndReturnsID(t *testing.T) {
	repo := new(MockRawContentRepository)
	svc := service.NewRawContentService(repo, newTestLogger())
	raw := newTestRawContent()

	repo.On("Save", mock.Anything, raw).Return(nil)

	id, isDuplicate, err := svc.Store(context.Background(), raw)

	assert.NoError(t, err)
	assert.Equal(t, raw.ID, id)
	assert.False(t, isDuplicate)
	repo.AssertExpectations(t)
}

func TestRawContentService_Store_DuplicateURL_ReturnsDuplicate(t *testing.T) {
	repo := new(MockRawContentRepository)
	svc := service.NewRawContentService(repo, newTestLogger())
	raw := newTestRawContent()

	existing := &core.RawContent{ID: "existing-raw-001", URL: raw.URL}

	// Save → ErrDuplicate (동일 URL 존재)
	repo.On("Save", mock.Anything, raw).Return(storage.ErrDuplicate)
	// GetByURL로 기존 레코드 조회
	repo.On("GetByURL", mock.Anything, raw.URL).Return(existing, nil)

	id, isDuplicate, err := svc.Store(context.Background(), raw)

	assert.NoError(t, err)
	assert.Equal(t, existing.ID, id)
	assert.True(t, isDuplicate)
	repo.AssertExpectations(t)
}

func TestRawContentService_Store_SaveError_ReturnsError(t *testing.T) {
	repo := new(MockRawContentRepository)
	svc := service.NewRawContentService(repo, newTestLogger())
	raw := newTestRawContent()

	saveErr := errors.New("network error")
	repo.On("Save", mock.Anything, raw).Return(saveErr)

	id, isDuplicate, err := svc.Store(context.Background(), raw)

	assert.Error(t, err)
	assert.ErrorIs(t, err, saveErr)
	assert.Empty(t, id)
	assert.False(t, isDuplicate)
	// GetByURL은 호출되지 않아야 함 (ErrDuplicate가 아닌 일반 에러)
	repo.AssertNotCalled(t, "GetByURL", mock.Anything, mock.Anything)
	repo.AssertExpectations(t)
}

func TestRawContentService_Store_DuplicateGetByURLError_ReturnsError(t *testing.T) {
	repo := new(MockRawContentRepository)
	svc := service.NewRawContentService(repo, newTestLogger())
	raw := newTestRawContent()

	repo.On("Save", mock.Anything, raw).Return(storage.ErrDuplicate)
	repo.On("GetByURL", mock.Anything, raw.URL).Return(nil, errors.New("lookup failed"))

	id, isDuplicate, err := svc.Store(context.Background(), raw)

	assert.Error(t, err)
	assert.Empty(t, id)
	assert.False(t, isDuplicate)
	repo.AssertExpectations(t)
}

// ─────────────────────────────────────────────────────────────────────────────
// GetByID 테스트
// ─────────────────────────────────────────────────────────────────────────────

func TestRawContentService_GetByID_Exists_ReturnsContent(t *testing.T) {
	repo := new(MockRawContentRepository)
	svc := service.NewRawContentService(repo, newTestLogger())
	raw := newTestRawContent()

	repo.On("GetByID", mock.Anything, raw.ID).Return(raw, nil)

	result, err := svc.GetByID(context.Background(), raw.ID)

	assert.NoError(t, err)
	assert.Equal(t, raw.ID, result.ID)
	repo.AssertExpectations(t)
}

func TestRawContentService_GetByID_NotFound_ReturnsError(t *testing.T) {
	repo := new(MockRawContentRepository)
	svc := service.NewRawContentService(repo, newTestLogger())

	repo.On("GetByID", mock.Anything, "missing-id").
		Return(nil, storage.ErrNotFound)

	result, err := svc.GetByID(context.Background(), "missing-id")

	assert.Error(t, err)
	assert.ErrorIs(t, err, storage.ErrNotFound)
	assert.Nil(t, result)
	repo.AssertExpectations(t)
}

// ─────────────────────────────────────────────────────────────────────────────
// List 테스트
// ─────────────────────────────────────────────────────────────────────────────

func TestRawContentService_List_WithFilter_ReturnsList(t *testing.T) {
	repo := new(MockRawContentRepository)
	svc := service.NewRawContentService(repo, newTestLogger())

	raws := []*core.RawContent{newTestRawContent()}
	filter := storage.RawContentFilter{
		Country:    "US",
		Pagination: storage.Pagination{Limit: 20},
	}

	repo.On("List", mock.Anything, filter).Return(raws, nil)

	result, err := svc.List(context.Background(), filter)

	assert.NoError(t, err)
	assert.Len(t, result, 1)
	repo.AssertExpectations(t)
}

func TestRawContentService_List_RepoError_ReturnsError(t *testing.T) {
	repo := new(MockRawContentRepository)
	svc := service.NewRawContentService(repo, newTestLogger())

	dbErr := errors.New("list failed")
	repo.On("List", mock.Anything, mock.Anything).Return(nil, dbErr)

	result, err := svc.List(context.Background(), storage.RawContentFilter{})

	assert.Error(t, err)
	assert.ErrorIs(t, err, dbErr)
	assert.Nil(t, result)
	repo.AssertExpectations(t)
}

// ─────────────────────────────────────────────────────────────────────────────
// PurgeOlderThan 테스트
// ─────────────────────────────────────────────────────────────────────────────

func TestRawContentService_PurgeOlderThan_DeletesAndReturnsCount(t *testing.T) {
	repo := new(MockRawContentRepository)
	svc := service.NewRawContentService(repo, newTestLogger())

	cutoff := time.Now().AddDate(0, 0, -90)
	repo.On("DeleteBefore", mock.Anything, cutoff).Return(int64(150), nil)

	count, err := svc.PurgeOlderThan(context.Background(), cutoff)

	assert.NoError(t, err)
	assert.Equal(t, int64(150), count)
	repo.AssertExpectations(t)
}

func TestRawContentService_PurgeOlderThan_RepoError_ReturnsError(t *testing.T) {
	repo := new(MockRawContentRepository)
	svc := service.NewRawContentService(repo, newTestLogger())

	purgeErr := errors.New("delete failed")
	cutoff := time.Now().AddDate(0, 0, -90)
	repo.On("DeleteBefore", mock.Anything, cutoff).Return(int64(0), purgeErr)

	count, err := svc.PurgeOlderThan(context.Background(), cutoff)

	assert.Error(t, err)
	assert.ErrorIs(t, err, purgeErr)
	assert.Equal(t, int64(0), count)
	repo.AssertExpectations(t)
}
