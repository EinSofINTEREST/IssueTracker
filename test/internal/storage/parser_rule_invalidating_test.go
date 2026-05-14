package storage_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/storage"
	"issuetracker/internal/storage/decorator"
	"issuetracker/internal/storage/model"
)

// recordingInvalidator 는 Invalidate 호출을 기록하는 fake CacheInvalidator 입니다.
type recordingInvalidator struct {
	mu    sync.Mutex
	calls []invalidateCall
}

type invalidateCall struct {
	Host string
	Type model.TargetType
}

func (i *recordingInvalidator) Invalidate(host string, t model.TargetType) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.calls = append(i.calls, invalidateCall{Host: host, Type: t})
}

func (i *recordingInvalidator) callCount() int {
	i.mu.Lock()
	defer i.mu.Unlock()
	return len(i.calls)
}

func (i *recordingInvalidator) lastCall() invalidateCall {
	i.mu.Lock()
	defer i.mu.Unlock()
	if len(i.calls) == 0 {
		return invalidateCall{}
	}
	return i.calls[len(i.calls)-1]
}

// recordingRepo — invalidating_repo decorator 의 inner repo 시뮬레이션.
type recordingRepo struct {
	insertErr     error
	updateErr     error
	updatePathErr error
	getByIDResult *model.ParserRuleRecord
	getByIDErr    error

	insertCalls            int
	insertNextVersionCalls int
	updateCalls            int
	updatePathCalls        int
	deleteCalls            int
	getByIDCalls           int
}

func (r *recordingRepo) Insert(_ context.Context, _ *model.ParserRuleRecord) error {
	r.insertCalls++
	return r.insertErr
}
func (r *recordingRepo) InsertNextVersion(_ context.Context, rec *model.ParserRuleRecord) error {
	r.insertNextVersionCalls++
	if rec.Version == 0 {
		rec.Version = 1
	}
	return r.insertErr
}
func (r *recordingRepo) Update(_ context.Context, _ *model.ParserRuleRecord) error {
	r.updateCalls++
	return r.updateErr
}
func (r *recordingRepo) UpdatePathPattern(_ context.Context, _ int64, _, _ string) error {
	r.updatePathCalls++
	return r.updatePathErr
}
func (r *recordingRepo) Delete(_ context.Context, _ int64) error {
	r.deleteCalls++
	return nil
}
func (r *recordingRepo) GetByID(_ context.Context, _ int64) (*model.ParserRuleRecord, error) {
	r.getByIDCalls++
	return r.getByIDResult, r.getByIDErr
}
func (r *recordingRepo) FindActive(_ context.Context, _ string, _ model.TargetType) (*model.ParserRuleRecord, error) {
	return nil, storage.ErrNotFound
}
func (r *recordingRepo) FindActiveCandidates(_ context.Context, _ string, _ model.TargetType) ([]*model.ParserRuleRecord, error) {
	return nil, nil
}
func (r *recordingRepo) HasAnyRule(_ context.Context, _ string, _ model.TargetType) (bool, bool, error) {
	return false, false, nil
}
func (r *recordingRepo) FindByNaturalKey(_ context.Context, _, _, _ string, _ model.TargetType, _ int) (*model.ParserRuleRecord, error) {
	return nil, storage.ErrNotFound
}
func (r *recordingRepo) List(_ context.Context, _ model.ParserRuleFilter) ([]*model.ParserRuleRecord, error) {
	return nil, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Insert
// ─────────────────────────────────────────────────────────────────────────────

func TestInvalidatingRepo_Insert_Success_TriggersInvalidate(t *testing.T) {
	inner := &recordingRepo{}
	inv := &recordingInvalidator{}
	repo := decorator.WrapWithInvalidator(inner, inv)

	rec := &model.ParserRuleRecord{HostPattern: "example.com", TargetType: model.TargetTypePage}
	err := repo.Insert(context.Background(), rec)
	require.NoError(t, err)
	assert.Equal(t, 1, inv.callCount(), "Insert 성공 시 Invalidate 호출")
	assert.Equal(t, invalidateCall{Host: "example.com", Type: model.TargetTypePage}, inv.lastCall())
}

func TestInvalidatingRepo_Insert_ErrDuplicate_TriggersInvalidate(t *testing.T) {
	inner := &recordingRepo{insertErr: storage.ErrDuplicate}
	inv := &recordingInvalidator{}
	repo := decorator.WrapWithInvalidator(inner, inv)

	rec := &model.ParserRuleRecord{HostPattern: "example.com", TargetType: model.TargetTypePage}
	err := repo.Insert(context.Background(), rec)
	require.ErrorIs(t, err, storage.ErrDuplicate)
	assert.Equal(t, 1, inv.callCount(), "ErrDuplicate 도 invalidate — cache 가 stale 일 가능성")
}

// ─────────────────────────────────────────────────────────────────────────────
// InsertNextVersion (이슈 #282)
// ─────────────────────────────────────────────────────────────────────────────

func TestInvalidatingRepo_InsertNextVersion_Success_TriggersInvalidate(t *testing.T) {
	inner := &recordingRepo{}
	inv := &recordingInvalidator{}
	repo := decorator.WrapWithInvalidator(inner, inv)

	rec := &model.ParserRuleRecord{HostPattern: "stale.example.com", TargetType: model.TargetTypePage}
	err := repo.InsertNextVersion(context.Background(), rec)
	require.NoError(t, err)
	assert.Equal(t, 1, inner.insertNextVersionCalls)
	assert.Equal(t, 1, inv.callCount(), "version bump 후 cache invalidate")
	assert.Equal(t, invalidateCall{Host: "stale.example.com", Type: model.TargetTypePage}, inv.lastCall())
}

func TestInvalidatingRepo_InsertNextVersion_ErrDuplicate_TriggersInvalidate(t *testing.T) {
	inner := &recordingRepo{insertErr: storage.ErrDuplicate}
	inv := &recordingInvalidator{}
	repo := decorator.WrapWithInvalidator(inner, inv)

	err := repo.InsertNextVersion(context.Background(), &model.ParserRuleRecord{HostPattern: "x", TargetType: model.TargetTypePage})
	require.ErrorIs(t, err, storage.ErrDuplicate)
	assert.Equal(t, 1, inv.callCount(), "ErrDuplicate (race window) 도 invalidate")
}

func TestInvalidatingRepo_Insert_OtherError_NoInvalidate(t *testing.T) {
	inner := &recordingRepo{insertErr: errors.New("db down")}
	inv := &recordingInvalidator{}
	repo := decorator.WrapWithInvalidator(inner, inv)

	rec := &model.ParserRuleRecord{HostPattern: "example.com", TargetType: model.TargetTypePage}
	err := repo.Insert(context.Background(), rec)
	require.Error(t, err)
	assert.Equal(t, 0, inv.callCount(), "기타 에러 (DB 장애 등) 시 invalidate 안 함 — cache 보존")
}

// ─────────────────────────────────────────────────────────────────────────────
// Update
// ─────────────────────────────────────────────────────────────────────────────

func TestInvalidatingRepo_Update_Success_TriggersInvalidate(t *testing.T) {
	inner := &recordingRepo{}
	inv := &recordingInvalidator{}
	repo := decorator.WrapWithInvalidator(inner, inv)

	rec := &model.ParserRuleRecord{HostPattern: "example.com", TargetType: model.TargetTypeList}
	err := repo.Update(context.Background(), rec)
	require.NoError(t, err)
	assert.Equal(t, 1, inv.callCount())
	assert.Equal(t, invalidateCall{Host: "example.com", Type: model.TargetTypeList}, inv.lastCall())
}

func TestInvalidatingRepo_Update_Error_NoInvalidate(t *testing.T) {
	inner := &recordingRepo{updateErr: errors.New("constraint violation")}
	inv := &recordingInvalidator{}
	repo := decorator.WrapWithInvalidator(inner, inv)

	err := repo.Update(context.Background(), &model.ParserRuleRecord{})
	require.Error(t, err)
	assert.Equal(t, 0, inv.callCount())
}

// ─────────────────────────────────────────────────────────────────────────────
// UpdatePathPattern
// ─────────────────────────────────────────────────────────────────────────────

func TestInvalidatingRepo_UpdatePathPattern_Success_PrefetchesAndInvalidates(t *testing.T) {
	inner := &recordingRepo{
		getByIDResult: &model.ParserRuleRecord{
			ID: 42, HostPattern: "yna.co.kr", TargetType: model.TargetTypePage,
		},
	}
	inv := &recordingInvalidator{}
	repo := decorator.WrapWithInvalidator(inner, inv)

	err := repo.UpdatePathPattern(context.Background(), 42, "/news/.*", "refiner v1")
	require.NoError(t, err)
	assert.Equal(t, 1, inner.getByIDCalls, "host/type 미지로 사전 GetByID 1회")
	assert.Equal(t, 1, inner.updatePathCalls)
	assert.Equal(t, 1, inv.callCount())
	assert.Equal(t, invalidateCall{Host: "yna.co.kr", Type: model.TargetTypePage}, inv.lastCall())
}

func TestInvalidatingRepo_UpdatePathPattern_Error_NoInvalidate(t *testing.T) {
	inner := &recordingRepo{
		getByIDResult: &model.ParserRuleRecord{HostPattern: "x.com", TargetType: model.TargetTypePage},
		updatePathErr: errors.New("not found"),
	}
	inv := &recordingInvalidator{}
	repo := decorator.WrapWithInvalidator(inner, inv)

	err := repo.UpdatePathPattern(context.Background(), 1, "/", "")
	require.Error(t, err)
	assert.Equal(t, 0, inv.callCount(), "update 실패 시 invalidate 안 함")
}

func TestInvalidatingRepo_UpdatePathPattern_PrefetchFails_NoInvalidate(t *testing.T) {
	inner := &recordingRepo{
		getByIDErr: errors.New("db down"),
	}
	inv := &recordingInvalidator{}
	repo := decorator.WrapWithInvalidator(inner, inv)

	// pre-fetch 실패해도 update 자체는 진행 (성공 가정).
	err := repo.UpdatePathPattern(context.Background(), 1, "/", "")
	require.NoError(t, err)
	assert.Equal(t, 0, inv.callCount(), "pre-fetch 실패 시 host/type 미상으로 invalidate skip")
}

// ─────────────────────────────────────────────────────────────────────────────
// Delete
// ─────────────────────────────────────────────────────────────────────────────

func TestInvalidatingRepo_Delete_Success_PrefetchesAndInvalidates(t *testing.T) {
	inner := &recordingRepo{
		getByIDResult: &model.ParserRuleRecord{
			ID: 99, HostPattern: "delete.example.com", TargetType: model.TargetTypePage,
		},
	}
	inv := &recordingInvalidator{}
	repo := decorator.WrapWithInvalidator(inner, inv)

	err := repo.Delete(context.Background(), 99)
	require.NoError(t, err)
	assert.Equal(t, 1, inner.getByIDCalls, "host/type 미지로 사전 GetByID 1회")
	assert.Equal(t, 1, inner.deleteCalls)
	assert.Equal(t, 1, inv.callCount())
	assert.Equal(t, invalidateCall{Host: "delete.example.com", Type: model.TargetTypePage}, inv.lastCall())
}

func TestInvalidatingRepo_Delete_PrefetchFails_NoInvalidate(t *testing.T) {
	inner := &recordingRepo{
		getByIDErr: errors.New("db down"),
	}
	inv := &recordingInvalidator{}
	repo := decorator.WrapWithInvalidator(inner, inv)

	err := repo.Delete(context.Background(), 1)
	require.NoError(t, err)
	assert.Equal(t, 1, inner.deleteCalls, "Delete 자체는 진행")
	assert.Equal(t, 0, inv.callCount(),
		"pre-fetch 실패 시 host/type 미상으로 invalidate skip (TTL fallback)")
}

// ─────────────────────────────────────────────────────────────────────────────
// nil invalidator (wrapping 단계에서 비활성)
// ─────────────────────────────────────────────────────────────────────────────

func TestInvalidatingRepo_NilInvalidator_NoOp(t *testing.T) {
	inner := &recordingRepo{}
	repo := decorator.WrapWithInvalidator(inner, nil)

	err := repo.Insert(context.Background(), &model.ParserRuleRecord{HostPattern: "x", TargetType: model.TargetTypePage})
	require.NoError(t, err)
	assert.Equal(t, 1, inner.insertCalls, "inner 호출은 정상")
	// invalidator 가 nil 이라 panic 없이 통과 — 정상 동작 확인 만으로 충분.
}

func TestWrapWithInvalidator_NilInner_Panics(t *testing.T) {
	assert.Panics(t, func() {
		_ = decorator.WrapWithInvalidator(nil, &recordingInvalidator{})
	}, "nil inner 는 wiring 버그 — panic 으로 즉시 노출")
}
