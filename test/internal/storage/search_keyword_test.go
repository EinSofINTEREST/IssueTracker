package storage_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"issuetracker/internal/storage"
)

// memSearchKeywordRepo 는 SearchKeywordRepository 의 in-memory 구현 — 단위 테스트 / phase 2
// 통합 테스트 fixture 로 재사용 가능. 실제 postgres 구현체와 동일한 contract.
type memSearchKeywordRepo struct {
	mu     sync.Mutex
	nextID int64
	byID   map[int64]*storage.SearchKeywordRecord
	byKey  map[string]int64
}

func newMemSearchKeywordRepo() *memSearchKeywordRepo {
	return &memSearchKeywordRepo{
		nextID: 1,
		byID:   map[int64]*storage.SearchKeywordRecord{},
		byKey:  map[string]int64{},
	}
}

func (r *memSearchKeywordRepo) ListEnabled(_ context.Context, language, region string) ([]*storage.SearchKeywordRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*storage.SearchKeywordRecord
	for _, rec := range r.byID {
		if !rec.Enabled {
			continue
		}
		if language != "" && rec.Language != language {
			continue
		}
		if region != "" && rec.Region != region {
			continue
		}
		copyRec := *rec
		out = append(out, &copyRec)
	}
	return out, nil
}

func (r *memSearchKeywordRepo) Insert(_ context.Context, rec *storage.SearchKeywordRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if rec.Keyword == "" {
		return storage.ErrInvalid
	}
	if _, exists := r.byKey[rec.Keyword]; exists {
		return storage.ErrDuplicate
	}
	if rec.Source == "" {
		rec.Source = storage.SearchKeywordSourceManual
	}
	rec.ID = r.nextID
	r.nextID++
	now := time.Now().UTC()
	rec.CreatedAt = now
	rec.UpdatedAt = now
	stored := *rec
	r.byID[rec.ID] = &stored
	r.byKey[rec.Keyword] = rec.ID
	return nil
}

func (r *memSearchKeywordRepo) Update(_ context.Context, rec *storage.SearchKeywordRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	stored, ok := r.byID[rec.ID]
	if !ok {
		return storage.ErrNotFound
	}
	stored.Enabled = rec.Enabled
	if rec.Source == "" {
		stored.Source = storage.SearchKeywordSourceManual
	} else {
		stored.Source = rec.Source
	}
	stored.Language = rec.Language
	stored.Region = rec.Region
	stored.Notes = rec.Notes
	stored.UpdatedAt = time.Now().UTC()
	rec.UpdatedAt = stored.UpdatedAt
	return nil
}

func (r *memSearchKeywordRepo) Delete(_ context.Context, id int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if rec, ok := r.byID[id]; ok {
		delete(r.byKey, rec.Keyword)
		delete(r.byID, id)
	}
	return nil
}

func (r *memSearchKeywordRepo) MarkSearched(_ context.Context, id int64, t time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	stored, ok := r.byID[id]
	if !ok {
		return storage.ErrNotFound
	}
	tCopy := t
	stored.LastSearchedAt = &tCopy
	stored.UpdatedAt = time.Now().UTC()
	return nil
}

// 컴파일 타임 contract 보증.
var _ storage.SearchKeywordRepository = (*memSearchKeywordRepo)(nil)

func TestSearchKeywordRepo_InsertAndList(t *testing.T) {
	t.Parallel()
	repo := newMemSearchKeywordRepo()
	ctx := context.Background()

	rec := &storage.SearchKeywordRecord{
		Keyword:  "AI 규제",
		Enabled:  true,
		Language: "ko",
		Region:   "kr",
		Notes:    "tech policy",
	}
	assert.NoError(t, repo.Insert(ctx, rec))
	assert.Greater(t, rec.ID, int64(0))
	assert.Equal(t, storage.SearchKeywordSourceManual, rec.Source, "Insert 가 빈 source 를 manual 로 default 처리해야 함")

	all, err := repo.ListEnabled(ctx, "", "")
	assert.NoError(t, err)
	assert.Len(t, all, 1)

	ko, err := repo.ListEnabled(ctx, "ko", "")
	assert.NoError(t, err)
	assert.Len(t, ko, 1)

	en, err := repo.ListEnabled(ctx, "en", "")
	assert.NoError(t, err)
	assert.Empty(t, en)
}

func TestSearchKeywordRepo_DuplicateKeywordRejected(t *testing.T) {
	t.Parallel()
	repo := newMemSearchKeywordRepo()
	ctx := context.Background()

	first := &storage.SearchKeywordRecord{Keyword: "duplicate", Enabled: true}
	assert.NoError(t, repo.Insert(ctx, first))

	second := &storage.SearchKeywordRecord{Keyword: "duplicate", Enabled: true}
	err := repo.Insert(ctx, second)
	assert.ErrorIs(t, err, storage.ErrDuplicate)
}

func TestSearchKeywordRepo_EmptyKeywordRejected(t *testing.T) {
	t.Parallel()
	repo := newMemSearchKeywordRepo()
	err := repo.Insert(context.Background(), &storage.SearchKeywordRecord{Enabled: true})
	assert.ErrorIs(t, err, storage.ErrInvalid)
}

func TestSearchKeywordRepo_DisabledKeywordsHidden(t *testing.T) {
	t.Parallel()
	repo := newMemSearchKeywordRepo()
	ctx := context.Background()

	on := &storage.SearchKeywordRecord{Keyword: "on-keyword", Enabled: true}
	off := &storage.SearchKeywordRecord{Keyword: "off-keyword", Enabled: false}
	assert.NoError(t, repo.Insert(ctx, on))
	assert.NoError(t, repo.Insert(ctx, off))

	enabled, err := repo.ListEnabled(ctx, "", "")
	assert.NoError(t, err)
	assert.Len(t, enabled, 1)
	assert.Equal(t, "on-keyword", enabled[0].Keyword)
}

func TestSearchKeywordRepo_UpdateAndDelete(t *testing.T) {
	t.Parallel()
	repo := newMemSearchKeywordRepo()
	ctx := context.Background()

	rec := &storage.SearchKeywordRecord{Keyword: "edit-me", Enabled: true}
	assert.NoError(t, repo.Insert(ctx, rec))

	rec.Enabled = false
	rec.Notes = "updated"
	assert.NoError(t, repo.Update(ctx, rec))

	got, err := repo.ListEnabled(ctx, "", "")
	assert.NoError(t, err)
	assert.Empty(t, got, "disabled keyword 은 ListEnabled 에서 제외")

	missing := &storage.SearchKeywordRecord{ID: 99999, Keyword: "edit-me"}
	assert.ErrorIs(t, repo.Update(ctx, missing), storage.ErrNotFound)

	assert.NoError(t, repo.Delete(ctx, rec.ID))
	assert.NoError(t, repo.Delete(ctx, rec.ID), "Delete 는 idempotent — 미존재 ID 도 nil")
}

func TestSearchKeywordRepo_MarkSearched(t *testing.T) {
	t.Parallel()
	repo := newMemSearchKeywordRepo()
	ctx := context.Background()

	rec := &storage.SearchKeywordRecord{Keyword: "mark-me", Enabled: true}
	assert.NoError(t, repo.Insert(ctx, rec))
	assert.Nil(t, rec.LastSearchedAt)

	now := time.Now().UTC()
	assert.NoError(t, repo.MarkSearched(ctx, rec.ID, now))

	got, err := repo.ListEnabled(ctx, "", "")
	assert.NoError(t, err)
	assert.Len(t, got, 1)
	assert.NotNil(t, got[0].LastSearchedAt)
	assert.True(t, got[0].LastSearchedAt.Equal(now))

	assert.ErrorIs(t, repo.MarkSearched(ctx, 99999, now), storage.ErrNotFound)
}
