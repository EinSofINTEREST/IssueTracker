package storage_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/storage"
)

// fakeBlacklistRepo 는 invalidatingBlacklistRepo decorator 단위 테스트용 in-memory BlacklistRepository.
type fakeBlacklistRepo struct {
	rows         []*storage.BlacklistRecord
	getByIDCalls int64
	insertErr    error
	getByIDErr   error
	updateErr    error
	deleteErr    error
}

func (r *fakeBlacklistRepo) Insert(_ context.Context, rec *storage.BlacklistRecord) error {
	if r.insertErr != nil {
		return r.insertErr
	}
	rec.ID = int64(len(r.rows) + 1)
	r.rows = append(r.rows, rec)
	return nil
}

func (r *fakeBlacklistRepo) Update(_ context.Context, rec *storage.BlacklistRecord) error {
	if r.updateErr != nil {
		return r.updateErr
	}
	for _, existing := range r.rows {
		if existing.ID == rec.ID {
			existing.Reason = rec.Reason
			existing.Source = rec.Source
			existing.Enabled = rec.Enabled
			return nil
		}
	}
	return storage.ErrNotFound
}

func (r *fakeBlacklistRepo) Delete(_ context.Context, id int64) error {
	if r.deleteErr != nil {
		return r.deleteErr
	}
	out := r.rows[:0]
	for _, existing := range r.rows {
		if existing.ID != id {
			out = append(out, existing)
		}
	}
	r.rows = out
	return nil
}

func (r *fakeBlacklistRepo) GetByID(_ context.Context, id int64) (*storage.BlacklistRecord, error) {
	atomic.AddInt64(&r.getByIDCalls, 1)
	if r.getByIDErr != nil {
		return nil, r.getByIDErr
	}
	for _, existing := range r.rows {
		if existing.ID == id {
			return existing, nil
		}
	}
	return nil, storage.ErrNotFound
}

func (r *fakeBlacklistRepo) FindEnabledByHost(_ context.Context, _ string) ([]*storage.BlacklistRecord, error) {
	return nil, nil
}

func (r *fakeBlacklistRepo) List(_ context.Context, _ storage.BlacklistFilter) ([]*storage.BlacklistRecord, error) {
	return r.rows, nil
}

// recordingBlacklistInvalidator 는 Invalidate 호출을 기록하는 fake BlacklistInvalidator.
type recordingBlacklistInvalidator struct {
	mu    sync.Mutex
	hosts []string
}

func (r *recordingBlacklistInvalidator) Invalidate(host string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hosts = append(r.hosts, host)
}

func (r *recordingBlacklistInvalidator) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.hosts))
	copy(out, r.hosts)
	return out
}

// ─────────────────────────────────────────────────────────────────────────────
// invalidatingBlacklistRepo decorator
// ─────────────────────────────────────────────────────────────────────────────

func TestInvalidatingBlacklistRepo_Insert_Success_Invalidates(t *testing.T) {
	inner := &fakeBlacklistRepo{}
	inv := &recordingBlacklistInvalidator{}
	repo := storage.WrapBlacklistWithInvalidator(inner, inv)

	err := repo.Insert(context.Background(), &storage.BlacklistRecord{
		HostPattern: "ads.example.com", Source: storage.BlacklistSourceManual, Enabled: true,
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"ads.example.com"}, inv.snapshot())
}

func TestInvalidatingBlacklistRepo_Insert_DuplicateAlsoInvalidates(t *testing.T) {
	inner := &fakeBlacklistRepo{insertErr: storage.ErrDuplicate}
	inv := &recordingBlacklistInvalidator{}
	repo := storage.WrapBlacklistWithInvalidator(inner, inv)

	err := repo.Insert(context.Background(), &storage.BlacklistRecord{
		HostPattern: "ads.example.com", Source: storage.BlacklistSourceManual, Enabled: true,
	})
	require.ErrorIs(t, err, storage.ErrDuplicate)
	assert.Equal(t, []string{"ads.example.com"}, inv.snapshot(), "ErrDuplicate 도 invalidate")
}

func TestInvalidatingBlacklistRepo_Update_Success_Invalidates(t *testing.T) {
	inner := &fakeBlacklistRepo{rows: []*storage.BlacklistRecord{
		{ID: 1, HostPattern: "ads.example.com", Enabled: true},
	}}
	inv := &recordingBlacklistInvalidator{}
	repo := storage.WrapBlacklistWithInvalidator(inner, inv)

	err := repo.Update(context.Background(), &storage.BlacklistRecord{
		ID: 1, HostPattern: "ads.example.com", Reason: "updated", Enabled: false,
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"ads.example.com"}, inv.snapshot())
}

// Update 는 host 변경 안 하므로 호출자가 rec.HostPattern 을 비워 보낼 수 있음.
// 사전 GetByID 로 authoritative host 를 얻어 invalidate 해야 cache 누락 0.
func TestInvalidatingBlacklistRepo_Update_EmptyHostInArg_StillInvalidatesActualHost(t *testing.T) {
	inner := &fakeBlacklistRepo{rows: []*storage.BlacklistRecord{
		{ID: 1, HostPattern: "ads.example.com", Enabled: true},
	}}
	inv := &recordingBlacklistInvalidator{}
	repo := storage.WrapBlacklistWithInvalidator(inner, inv)

	err := repo.Update(context.Background(), &storage.BlacklistRecord{
		ID: 1, Reason: "updated", Enabled: false,
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"ads.example.com"}, inv.snapshot(),
		"empty HostPattern arg 여도 사전 GetByID 로 실제 host 발견 후 invalidate")
}

func TestInvalidatingBlacklistRepo_Delete_PrefetchesAndInvalidates(t *testing.T) {
	inner := &fakeBlacklistRepo{rows: []*storage.BlacklistRecord{
		{ID: 1, HostPattern: "ads.example.com", Enabled: true},
	}}
	inv := &recordingBlacklistInvalidator{}
	repo := storage.WrapBlacklistWithInvalidator(inner, inv)

	err := repo.Delete(context.Background(), 1)
	require.NoError(t, err)
	assert.Equal(t, []string{"ads.example.com"}, inv.snapshot())
}

func TestInvalidatingBlacklistRepo_Delete_PrefetchFails_NoInvalidate(t *testing.T) {
	inner := &fakeBlacklistRepo{getByIDErr: errors.New("db down")}
	inv := &recordingBlacklistInvalidator{}
	repo := storage.WrapBlacklistWithInvalidator(inner, inv)

	err := repo.Delete(context.Background(), 1)
	require.NoError(t, err, "Delete 자체는 성공할 수 있음")
	assert.Empty(t, inv.snapshot(), "pre-fetch 실패 시 invalidate skip — TTL fallback")
}

func TestInvalidatingBlacklistRepo_NilInvalidator_NoOp(t *testing.T) {
	inner := &fakeBlacklistRepo{}
	repo := storage.WrapBlacklistWithInvalidator(inner, nil)

	err := repo.Insert(context.Background(), &storage.BlacklistRecord{
		HostPattern: "ads.example.com", Source: storage.BlacklistSourceManual, Enabled: true,
	})
	require.NoError(t, err)
}
