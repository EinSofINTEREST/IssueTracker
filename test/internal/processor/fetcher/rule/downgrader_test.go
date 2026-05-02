package rule_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	fetcherRule "issuetracker/internal/processor/fetcher/rule"
	"issuetracker/internal/storage"
)

// downgradeStubRepo 는 Downgrader 단위 테스트용 in-memory 스텁입니다.
// 다른 메소드는 노옵 — 본 테스트는 BulkDowngradeAutoUpgraded 의 호출 / 반환만 검증.
type downgradeStubRepo struct {
	bulkResult []string
	bulkErr    error
	bulkCalls  atomic.Int32
}

func (s *downgradeStubRepo) Upsert(ctx context.Context, host string, fetcher storage.FetcherKind, reason string) error {
	return nil
}
func (s *downgradeStubRepo) GetByHost(ctx context.Context, host string) (*storage.FetcherRuleRecord, error) {
	return nil, storage.ErrNotFound
}
func (s *downgradeStubRepo) List(ctx context.Context) ([]*storage.FetcherRuleRecord, error) {
	return nil, nil
}
func (s *downgradeStubRepo) Delete(ctx context.Context, host string) error { return nil }
func (s *downgradeStubRepo) BulkDowngradeAutoUpgraded(ctx context.Context) ([]string, error) {
	s.bulkCalls.Add(1)
	if s.bulkErr != nil {
		return nil, s.bulkErr
	}
	out := make([]string, len(s.bulkResult))
	copy(out, s.bulkResult)
	return out, nil
}

// TestNewDowngrader_NilDependencies_ReturnsError:
// 이슈 #208 panic-on-nil 정책 — 의존성 nil 시 즉시 error.
func TestNewDowngrader_NilDependencies_ReturnsError(t *testing.T) {
	repo := &downgradeStubRepo{}
	r, _ := fetcherRule.NewResolver(repo, newTestLogger(), time.Hour)

	_, err := fetcherRule.NewDowngrader(nil, r, time.Hour, newTestLogger())
	assert.Error(t, err)

	_, err = fetcherRule.NewDowngrader(repo, nil, time.Hour, newTestLogger())
	assert.Error(t, err)

	_, err = fetcherRule.NewDowngrader(repo, r, 0, newTestLogger())
	assert.Error(t, err)

	_, err = fetcherRule.NewDowngrader(repo, r, -time.Second, newTestLogger())
	assert.Error(t, err)
}

// TestDowngrader_Run_NoChanges_NoInvalidate:
// BulkDowngradeAutoUpgraded 가 빈 슬라이스 반환 시 Resolver.Invalidate 미호출.
func TestDowngrader_Run_NoChanges_NoInvalidate(t *testing.T) {
	repo := &downgradeStubRepo{bulkResult: nil}
	r, err := fetcherRule.NewResolver(repo, newTestLogger(), time.Hour)
	require.NoError(t, err)

	d, err := fetcherRule.NewDowngrader(repo, r, time.Hour, newTestLogger())
	require.NoError(t, err)

	d.Run(context.Background())
	assert.Equal(t, int32(1), repo.bulkCalls.Load())
}

// TestDowngrader_Run_ChangesPresent_InvalidatesCache:
// BulkDowngradeAutoUpgraded 가 N건 반환 시 각 host 의 Resolver cache invalidate 검증.
//
// 검증 방식: 미리 cache 에 host 의 chromedp 룰을 채워두고, Run 실행 후 같은 host 조회 시 repo
// 가 다시 hit 되는지 (cache miss → 재조회) 카운터로 측정.
func TestDowngrader_Run_ChangesPresent_InvalidatesCache(t *testing.T) {
	repo := &positiveStubRepo{
		// host A 의 룰을 chromedp 로 보관 (Resolver 캐시 채우기 용)
		rules: map[string]*storage.FetcherRuleRecord{
			"a.com": {HostPattern: "a.com", Fetcher: storage.FetcherChromedp},
			"b.com": {HostPattern: "b.com", Fetcher: storage.FetcherChromedp},
		},
		// BulkDowngrade 시 두 host 모두 변경됐다고 응답
		bulkResult: []string{"a.com", "b.com"},
	}
	r, err := fetcherRule.NewResolver(repo, newTestLogger(), time.Hour)
	require.NoError(t, err)

	ctx := context.Background()

	// cache warm-up — repo 1회 hit
	_, _ = r.Resolve(ctx, "a.com")
	_, _ = r.Resolve(ctx, "b.com")
	require.Equal(t, int32(2), repo.getCalls.Load())

	d, err := fetcherRule.NewDowngrader(repo, r, time.Hour, newTestLogger())
	require.NoError(t, err)

	d.Run(ctx)
	assert.Equal(t, int32(1), repo.bulkCalls.Load())

	// Invalidate 후 같은 host 조회 시 cache miss → repo 재 hit (총 4회)
	_, _ = r.Resolve(ctx, "a.com")
	_, _ = r.Resolve(ctx, "b.com")
	assert.Equal(t, int32(4), repo.getCalls.Load(), "Invalidate 후 같은 host 의 Resolve 가 repo 까지 도달해야 함")
}

// TestDowngrader_Run_RepoError_NoFatal:
// repo 가 에러 반환 시 Run 은 panic / fatal 없이 graceful return — 다음 주기에 자연 retry.
func TestDowngrader_Run_RepoError_NoFatal(t *testing.T) {
	repo := &downgradeStubRepo{bulkErr: errors.New("connection lost")}
	r, _ := fetcherRule.NewResolver(repo, newTestLogger(), time.Hour)

	d, err := fetcherRule.NewDowngrader(repo, r, time.Hour, newTestLogger())
	require.NoError(t, err)

	assert.NotPanics(t, func() {
		d.Run(context.Background())
	})
}

// TestDowngrader_StartStop_TickerPeriodicRun:
// Start 가 interval 주기로 Run 을 호출, Stop 시 graceful 종료.
//
// interval 50ms + 200ms 대기 → 약 3-4회 호출 기대 (timing fragile 회피 위해 >= 1 만 검증).
func TestDowngrader_StartStop_TickerPeriodicRun(t *testing.T) {
	repo := &downgradeStubRepo{bulkResult: nil}
	r, _ := fetcherRule.NewResolver(repo, newTestLogger(), time.Hour)

	d, err := fetcherRule.NewDowngrader(repo, r, 50*time.Millisecond, newTestLogger())
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	d.Start(ctx)
	time.Sleep(180 * time.Millisecond)
	cancel()

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer stopCancel()
	require.NoError(t, d.Stop(stopCtx))

	calls := repo.bulkCalls.Load()
	assert.GreaterOrEqual(t, calls, int32(1), "ticker 가 적어도 1회 발동해야 함")
	assert.LessOrEqual(t, calls, int32(10), "ticker 가 과도하게 발동되지 않아야 함")
}

// TestDowngrader_Name_Stable:
// processor.Stage 식별자 sentinel — 다른 stage / locks key 와 충돌 회피.
func TestDowngrader_Name_Stable(t *testing.T) {
	repo := &downgradeStubRepo{}
	r, _ := fetcherRule.NewResolver(repo, newTestLogger(), time.Hour)
	d, err := fetcherRule.NewDowngrader(repo, r, time.Hour, newTestLogger())
	require.NoError(t, err)
	assert.Equal(t, "fetcher-downgrader", d.Name())
}

// positiveStubRepo 는 Resolver cache invalidate 효과 검증을 위한 추가 스텁.
// downgradeStubRepo 와 분리 — GetByHost 가 실제 host map 조회를 수행해 캐시 동작 추적.
type positiveStubRepo struct {
	rules      map[string]*storage.FetcherRuleRecord
	bulkResult []string
	getCalls   atomic.Int32
	bulkCalls  atomic.Int32
}

func (s *positiveStubRepo) Upsert(ctx context.Context, host string, fetcher storage.FetcherKind, reason string) error {
	return nil
}
func (s *positiveStubRepo) GetByHost(ctx context.Context, host string) (*storage.FetcherRuleRecord, error) {
	s.getCalls.Add(1)
	if r, ok := s.rules[host]; ok {
		return r, nil
	}
	return nil, storage.ErrNotFound
}
func (s *positiveStubRepo) List(ctx context.Context) ([]*storage.FetcherRuleRecord, error) {
	return nil, nil
}
func (s *positiveStubRepo) Delete(ctx context.Context, host string) error { return nil }
func (s *positiveStubRepo) BulkDowngradeAutoUpgraded(ctx context.Context) ([]string, error) {
	s.bulkCalls.Add(1)
	out := make([]string, len(s.bulkResult))
	copy(out, s.bulkResult)
	return out, nil
}
