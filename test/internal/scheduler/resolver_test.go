package scheduler_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/scheduler"
	"issuetracker/internal/storage/model"
	"issuetracker/internal/storage/repository"
	"issuetracker/pkg/logger"
)

// fakeSchedulerEntryRepo 는 ListEnabled 호출 횟수와 응답을 통제하는 stub.
type fakeSchedulerEntryRepo struct {
	mu      sync.Mutex
	records []*model.SchedulerEntryRecord
	calls   int32
	err     error
}

func (r *fakeSchedulerEntryRepo) ListEnabled(_ context.Context, _ model.SchedulerCategory) ([]*model.SchedulerEntryRecord, error) {
	atomic.AddInt32(&r.calls, 1)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return nil, r.err
	}
	out := make([]*model.SchedulerEntryRecord, len(r.records))
	copy(out, r.records)
	return out, nil
}

func (r *fakeSchedulerEntryRepo) Insert(_ context.Context, _ *model.SchedulerEntryRecord) error {
	return nil
}
func (r *fakeSchedulerEntryRepo) Update(_ context.Context, _ *model.SchedulerEntryRecord) error {
	return nil
}
func (r *fakeSchedulerEntryRepo) Delete(_ context.Context, _ int64) error { return nil }

func (r *fakeSchedulerEntryRepo) callCount() int { return int(atomic.LoadInt32(&r.calls)) }

func (r *fakeSchedulerEntryRepo) setRecords(recs []*model.SchedulerEntryRecord) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records = recs
}

func (r *fakeSchedulerEntryRepo) setErr(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.err = err
}

func newTestResolver(t *testing.T, repo repository.SchedulerEntryRepository, ttl time.Duration) scheduler.EntryResolver {
	t.Helper()
	conv := scheduler.NewDefaultEntryConverter(30 * time.Second)
	r, err := scheduler.NewEntryResolver(repo, conv, logger.New(logger.DefaultConfig()), ttl)
	require.NoError(t, err)
	return r
}

func TestEntryResolver_Resolve_HitsAndCaches(t *testing.T) {
	repo := &fakeSchedulerEntryRepo{
		records: []*model.SchedulerEntryRecord{
			{ID: 1, Category: model.SchedulerCategoryNews, SourceName: "naver",
				URL: "https://news.naver.com/section/100", TargetType: "category",
				Interval: 30 * time.Minute, Priority: 2, Enabled: true},
		},
	}
	r := newTestResolver(t, repo, 5*time.Minute)

	entries, err := r.Resolve(context.Background())
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "https://news.naver.com/section/100", entries[0].URL)
	assert.Equal(t, 30*time.Minute, entries[0].Interval)
	assert.Equal(t, 1, repo.callCount(), "첫 Resolve 에서 DB 1회 hit")

	// 두 번째 호출은 cache hit — DB 호출 안 일어남.
	_, err = r.Resolve(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, repo.callCount(), "cache hit 으로 DB 호출 추가 발생 X")
}

func TestEntryResolver_Resolve_DBError_KeepsLastSnapshot(t *testing.T) {
	repo := &fakeSchedulerEntryRepo{
		records: []*model.SchedulerEntryRecord{
			{ID: 1, Category: model.SchedulerCategoryNews, SourceName: "naver",
				URL: "https://x.com/", TargetType: "category",
				Interval: time.Minute, Priority: 2, Enabled: true},
		},
	}
	// 짧은 TTL 로 강제 만료 후 DB error 케이스 진입.
	r := newTestResolver(t, repo, 30*time.Millisecond)

	entries, _ := r.Resolve(context.Background())
	require.Len(t, entries, 1)

	repo.setErr(errors.New("db down"))
	time.Sleep(50 * time.Millisecond)

	// fail-open: 마지막 snapshot 유지.
	entries, err := r.Resolve(context.Background())
	require.NoError(t, err)
	assert.Len(t, entries, 1, "DB 일시 장애 시 마지막 snapshot 유지")
}

func TestEntryResolver_Invalidate_ImmediatelyDropsCache(t *testing.T) {
	repo := &fakeSchedulerEntryRepo{
		records: []*model.SchedulerEntryRecord{
			{ID: 1, Category: model.SchedulerCategoryNews, SourceName: "naver",
				URL: "https://x.com/", TargetType: "category",
				Interval: time.Minute, Priority: 2, Enabled: true},
		},
	}
	r := newTestResolver(t, repo, 5*time.Minute)

	_, _ = r.Resolve(context.Background())
	assert.Equal(t, 1, repo.callCount())

	r.Invalidate()
	repo.setRecords([]*model.SchedulerEntryRecord{
		{ID: 1, Category: model.SchedulerCategoryNews, SourceName: "naver",
			URL: "https://x.com/", TargetType: "category",
			Interval: 5 * time.Minute, Priority: 2, Enabled: true},
		{ID: 2, Category: model.SchedulerCategoryNews, SourceName: "daum",
			URL: "https://y.com/", TargetType: "category",
			Interval: time.Minute, Priority: 2, Enabled: true},
	})

	entries, _ := r.Resolve(context.Background())
	assert.Len(t, entries, 2, "Invalidate 후 즉시 새 record 반영")
	assert.Equal(t, 2, repo.callCount())
}

func TestNewEntryResolver_NilRepo_Errors(t *testing.T) {
	conv := scheduler.NewDefaultEntryConverter(30 * time.Second)
	_, err := scheduler.NewEntryResolver(nil, conv, logger.New(logger.DefaultConfig()), 0)
	require.Error(t, err)
}
