package rate_limiter_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	ratelimiter "issuetracker/internal/processor/fetcher/rate_limiter"
	"issuetracker/internal/storage"
	"issuetracker/pkg/logger"
)

// ─────────────────────────────────────────────────────────────────────────────
// fakeFetcherRuleRepo — SourceConfigResolver 테스트용 stub
// FetcherRuleRepository 인터페이스의 GetByHost 만 의미 있게 구현, 나머지는 zero stub.
// ─────────────────────────────────────────────────────────────────────────────

type fakeFetcherRuleRepo struct {
	mu       sync.Mutex
	records  map[string]*storage.FetcherRuleRecord
	getCalls int32
	getErr   error
}

func newFakeFetcherRuleRepo() *fakeFetcherRuleRepo {
	return &fakeFetcherRuleRepo{records: make(map[string]*storage.FetcherRuleRecord)}
}

func (r *fakeFetcherRuleRepo) put(host string, rec *storage.FetcherRuleRecord) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records[host] = rec
}

func (r *fakeFetcherRuleRepo) GetByHost(_ context.Context, host string) (*storage.FetcherRuleRecord, error) {
	atomic.AddInt32(&r.getCalls, 1)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.getErr != nil {
		return nil, r.getErr
	}
	rec, ok := r.records[host]
	if !ok {
		return nil, storage.ErrNotFound
	}
	return rec, nil
}

func (r *fakeFetcherRuleRepo) callCount() int { return int(atomic.LoadInt32(&r.getCalls)) }

func (r *fakeFetcherRuleRepo) Upsert(_ context.Context, _ string, _ storage.FetcherKind, _ string) error {
	return nil
}
func (r *fakeFetcherRuleRepo) List(_ context.Context) ([]*storage.FetcherRuleRecord, error) {
	return nil, nil
}
func (r *fakeFetcherRuleRepo) Delete(_ context.Context, _ string) error { return nil }
func (r *fakeFetcherRuleRepo) BulkDowngradeAutoUpgraded(_ context.Context) ([]string, error) {
	return nil, nil
}

// ─────────────────────────────────────────────────────────────────────────────

func TestNewSourceConfigResolver_NilRepo_Errors(t *testing.T) {
	_, err := ratelimiter.NewSourceConfigResolver(nil, nil, time.Minute)
	require.Error(t, err)
}

func TestSourceConfigResolver_Resolve_HitsAndCaches(t *testing.T) {
	repo := newFakeFetcherRuleRepo()
	repo.put("naver.example.com", &storage.FetcherRuleRecord{
		HostPattern: "naver.example.com", RequestsPerHour: 200,
	})
	r, err := ratelimiter.NewSourceConfigResolver(repo, logger.New(logger.DefaultConfig()), 5*time.Minute)
	require.NoError(t, err)

	cfg, err := r.Resolve(context.Background(), "naver.example.com")
	require.NoError(t, err)
	assert.Equal(t, 200, cfg.RequestsPerHour)
	assert.Equal(t, 1, repo.callCount(), "첫 호출에서 DB 1회 hit")

	// 두 번째 호출은 cache hit — DB 호출 안 일어남.
	cfg, err = r.Resolve(context.Background(), "naver.example.com")
	require.NoError(t, err)
	assert.Equal(t, 200, cfg.RequestsPerHour)
	assert.Equal(t, 1, repo.callCount(), "cache hit 으로 DB 호출 추가 발생 X")
}

func TestSourceConfigResolver_Resolve_NotFound_ReturnsZero_AndCaches(t *testing.T) {
	repo := newFakeFetcherRuleRepo()
	r, err := ratelimiter.NewSourceConfigResolver(repo, logger.New(logger.DefaultConfig()), 5*time.Minute)
	require.NoError(t, err)

	cfg, err := r.Resolve(context.Background(), "missing.example.com")
	require.NoError(t, err, "ErrNotFound 은 silent — fail-open")
	assert.Equal(t, 0, cfg.RequestsPerHour)

	// negative cache 도 hit — DB 호출 1회만.
	_, _ = r.Resolve(context.Background(), "missing.example.com")
	assert.Equal(t, 1, repo.callCount(), "negative cache hit")
}

func TestSourceConfigResolver_Resolve_DBError_FailOpen_NoCacheStore(t *testing.T) {
	repo := newFakeFetcherRuleRepo()
	repo.getErr = errors.New("db down")
	r, err := ratelimiter.NewSourceConfigResolver(repo, logger.New(logger.DefaultConfig()), 5*time.Minute)
	require.NoError(t, err)

	cfg, err := r.Resolve(context.Background(), "transient.example.com")
	require.NoError(t, err, "DB 일시 장애는 fail-open 으로 silent — fetch 흐름 차단 회피")
	assert.Equal(t, 0, cfg.RequestsPerHour)

	// 두 번째 호출도 동일 DB 실패가 sticky 하게 cache 되지 않아 다시 DB 시도.
	repo.getErr = nil
	repo.put("transient.example.com", &storage.FetcherRuleRecord{RequestsPerHour: 100})

	cfg, err = r.Resolve(context.Background(), "transient.example.com")
	require.NoError(t, err)
	assert.Equal(t, 100, cfg.RequestsPerHour, "DB 복구 후 즉시 정상 값 반영")
}

func TestSourceConfigResolver_Resolve_TTLExpiry_RefetchesFromDB(t *testing.T) {
	repo := newFakeFetcherRuleRepo()
	repo.put("dynamic.example.com", &storage.FetcherRuleRecord{RequestsPerHour: 100})

	// 짧은 TTL 로 만료 시점 검증.
	r, err := ratelimiter.NewSourceConfigResolver(repo, logger.New(logger.DefaultConfig()), 50*time.Millisecond)
	require.NoError(t, err)

	cfg, _ := r.Resolve(context.Background(), "dynamic.example.com")
	assert.Equal(t, 100, cfg.RequestsPerHour)

	// 운영자가 UPDATE 한 상황 시뮬레이션.
	repo.put("dynamic.example.com", &storage.FetcherRuleRecord{RequestsPerHour: 50})

	// TTL 만료 전에는 stale 100 유지.
	cfg, _ = r.Resolve(context.Background(), "dynamic.example.com")
	assert.Equal(t, 100, cfg.RequestsPerHour, "TTL 내에는 stale cache")

	// TTL 만료 후 재 lookup → 새 값 반영.
	time.Sleep(60 * time.Millisecond)
	cfg, _ = r.Resolve(context.Background(), "dynamic.example.com")
	assert.Equal(t, 50, cfg.RequestsPerHour, "TTL 만료 후 새 값 자동 반영")
}

func TestSourceConfigResolver_Invalidate_ImmediatelyDropsCache(t *testing.T) {
	repo := newFakeFetcherRuleRepo()
	repo.put("flaky.example.com", &storage.FetcherRuleRecord{RequestsPerHour: 100})
	r, err := ratelimiter.NewSourceConfigResolver(repo, logger.New(logger.DefaultConfig()), 5*time.Minute)
	require.NoError(t, err)

	cfg, _ := r.Resolve(context.Background(), "flaky.example.com")
	assert.Equal(t, 100, cfg.RequestsPerHour)

	// 운영자 UPDATE + Invalidate — TTL 대기 없이 즉시 반영.
	repo.put("flaky.example.com", &storage.FetcherRuleRecord{RequestsPerHour: 50})
	r.Invalidate("flaky.example.com")

	cfg, _ = r.Resolve(context.Background(), "flaky.example.com")
	assert.Equal(t, 50, cfg.RequestsPerHour)
}

func TestSourceConfigResolver_Resolve_EmptyHost_ReturnsZero(t *testing.T) {
	repo := newFakeFetcherRuleRepo()
	r, err := ratelimiter.NewSourceConfigResolver(repo, logger.New(logger.DefaultConfig()), 5*time.Minute)
	require.NoError(t, err)

	cfg, err := r.Resolve(context.Background(), "")
	require.NoError(t, err)
	assert.Equal(t, 0, cfg.RequestsPerHour)
	assert.Equal(t, 0, repo.callCount(), "빈 host 는 DB hit 안 함")
}
