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
	"issuetracker/pkg/logger"
)

// stubFetcherRuleRepo 는 FetcherRuleRepository 의 in-memory 스텁입니다.
//
// repo 동작 자체는 본 sub 의 검증 대상이 아니라 (Postgres 통합 테스트 영역) Resolver 의
// cache / fallback / Invalidate 동작을 격리 검증하기 위한 stub.
type stubFetcherRuleRepo struct {
	rules map[string]*storage.FetcherRuleRecord
	err   error
	calls atomic.Int32
}

func (s *stubFetcherRuleRepo) Upsert(ctx context.Context, host string, fetcher storage.FetcherKind, reason string) error {
	if s.rules == nil {
		s.rules = make(map[string]*storage.FetcherRuleRecord)
	}
	s.rules[host] = &storage.FetcherRuleRecord{HostPattern: host, Fetcher: fetcher, Reason: reason}
	return nil
}
func (s *stubFetcherRuleRepo) GetByHost(ctx context.Context, host string) (*storage.FetcherRuleRecord, error) {
	s.calls.Add(1)
	if s.err != nil {
		return nil, s.err
	}
	if r, ok := s.rules[host]; ok {
		return r, nil
	}
	return nil, storage.ErrNotFound
}
func (s *stubFetcherRuleRepo) List(ctx context.Context) ([]*storage.FetcherRuleRecord, error) {
	out := make([]*storage.FetcherRuleRecord, 0, len(s.rules))
	for _, r := range s.rules {
		out = append(out, r)
	}
	return out, nil
}
func (s *stubFetcherRuleRepo) Delete(ctx context.Context, host string) error {
	delete(s.rules, host)
	return nil
}

func newTestLogger() *logger.Logger {
	return logger.New(logger.DefaultConfig())
}

// TestNewResolver_NilRepo_ReturnsError:
// 이슈 #208 panic-on-nil → error 정책 — wiring 시 nil repo 는 즉시 error.
func TestNewResolver_NilRepo_ReturnsError(t *testing.T) {
	_, err := fetcherRule.NewResolver(nil, newTestLogger(), 0)
	assert.Error(t, err)
}

// TestResolver_HostNotFound_ReturnsFalse:
// fetcher_rules 부재 host 는 ResolveResult{Found: false} — 호출자가 default chain 사용 분기.
func TestResolver_HostNotFound_ReturnsFalse(t *testing.T) {
	repo := &stubFetcherRuleRepo{}
	r, err := fetcherRule.NewResolver(repo, newTestLogger(), 0)
	require.NoError(t, err)

	res, err := r.Resolve(context.Background(), "unknown.example.com")
	require.NoError(t, err)
	assert.False(t, res.Found)
}

// TestResolver_HostMatched_ReturnsFetcher:
// 매칭 host 는 등록된 fetcher 그대로 반환.
func TestResolver_HostMatched_ReturnsFetcher(t *testing.T) {
	repo := &stubFetcherRuleRepo{
		rules: map[string]*storage.FetcherRuleRecord{
			"edition.cnn.com": {HostPattern: "edition.cnn.com", Fetcher: storage.FetcherChromedp},
		},
	}
	r, err := fetcherRule.NewResolver(repo, newTestLogger(), 0)
	require.NoError(t, err)

	res, err := r.Resolve(context.Background(), "edition.cnn.com")
	require.NoError(t, err)
	assert.True(t, res.Found)
	assert.Equal(t, storage.FetcherChromedp, res.Fetcher)
}

// TestResolver_CachesPositiveResult:
// 같은 host 의 반복 조회는 cache hit — repo.GetByHost 가 1회만 호출.
func TestResolver_CachesPositiveResult(t *testing.T) {
	repo := &stubFetcherRuleRepo{
		rules: map[string]*storage.FetcherRuleRecord{
			"edition.cnn.com": {HostPattern: "edition.cnn.com", Fetcher: storage.FetcherChromedp},
		},
	}
	r, err := fetcherRule.NewResolver(repo, newTestLogger(), 1*time.Hour)
	require.NoError(t, err)

	for i := 0; i < 5; i++ {
		_, _ = r.Resolve(context.Background(), "edition.cnn.com")
	}
	assert.Equal(t, int32(1), repo.calls.Load())
}

// TestResolver_CachesNegativeResult:
// not-found 도 cache — 동일 host 의 반복 조회가 매번 DB 까지 가지 않도록.
func TestResolver_CachesNegativeResult(t *testing.T) {
	repo := &stubFetcherRuleRepo{}
	r, err := fetcherRule.NewResolver(repo, newTestLogger(), 1*time.Hour)
	require.NoError(t, err)

	for i := 0; i < 5; i++ {
		_, _ = r.Resolve(context.Background(), "unknown.example.com")
	}
	assert.Equal(t, int32(1), repo.calls.Load())
}

// TestResolver_RepoError_PropagatesNoCache:
// repo 가 ErrNotFound 가 아닌 에러를 반환하면 caller 에 전파 + cache 미저장 (다음 호출에 재시도).
func TestResolver_RepoError_PropagatesNoCache(t *testing.T) {
	repo := &stubFetcherRuleRepo{err: errors.New("connection lost")}
	r, err := fetcherRule.NewResolver(repo, newTestLogger(), 1*time.Hour)
	require.NoError(t, err)

	_, err = r.Resolve(context.Background(), "edition.cnn.com")
	assert.Error(t, err)

	// 동일 host 의 다음 호출도 repo 까지 도달해야 함.
	_, err = r.Resolve(context.Background(), "edition.cnn.com")
	assert.Error(t, err)
	assert.Equal(t, int32(2), repo.calls.Load())
}

// TestResolver_TTLExpiry_RefetchesFromRepo:
// TTL 만료 후 재조회는 repo 로 다시 hit.
func TestResolver_TTLExpiry_RefetchesFromRepo(t *testing.T) {
	repo := &stubFetcherRuleRepo{
		rules: map[string]*storage.FetcherRuleRecord{
			"edition.cnn.com": {HostPattern: "edition.cnn.com", Fetcher: storage.FetcherChromedp},
		},
	}
	r, err := fetcherRule.NewResolver(repo, newTestLogger(), 5*time.Millisecond)
	require.NoError(t, err)

	_, _ = r.Resolve(context.Background(), "edition.cnn.com")
	assert.Equal(t, int32(1), repo.calls.Load())

	time.Sleep(20 * time.Millisecond)

	_, _ = r.Resolve(context.Background(), "edition.cnn.com")
	assert.Equal(t, int32(2), repo.calls.Load())
}

// TestResolver_Invalidate_ForcesRefetch:
// Invalidate 호출 후 같은 host 조회는 cache miss → repo hit.
func TestResolver_Invalidate_ForcesRefetch(t *testing.T) {
	repo := &stubFetcherRuleRepo{
		rules: map[string]*storage.FetcherRuleRecord{
			"edition.cnn.com": {HostPattern: "edition.cnn.com", Fetcher: storage.FetcherChromedp},
		},
	}
	r, err := fetcherRule.NewResolver(repo, newTestLogger(), 1*time.Hour)
	require.NoError(t, err)

	_, _ = r.Resolve(context.Background(), "edition.cnn.com")
	assert.Equal(t, int32(1), repo.calls.Load())

	r.Invalidate("edition.cnn.com")

	_, _ = r.Resolve(context.Background(), "edition.cnn.com")
	assert.Equal(t, int32(2), repo.calls.Load())
}

// TestResolver_EmptyHost_ReturnsFalseWithoutRepoCall:
// 빈 host 는 핫패스에서 즉시 default 분기 — repo 호출 없음.
func TestResolver_EmptyHost_ReturnsFalseWithoutRepoCall(t *testing.T) {
	repo := &stubFetcherRuleRepo{}
	r, err := fetcherRule.NewResolver(repo, newTestLogger(), 0)
	require.NoError(t, err)

	res, err := r.Resolve(context.Background(), "")
	require.NoError(t, err)
	assert.False(t, res.Found)
	assert.Equal(t, int32(0), repo.calls.Load())
}
