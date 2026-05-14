package storage_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/storage"
	"issuetracker/internal/storage/model"
	"issuetracker/internal/storage/repository"
)

// inMemorySampleRepo 는 SampleURLRepository 의 in-memory 구현 — 인터페이스 정책 검증용.
//
// postgres 구현은 testcontainers 필요라 본 PR scope 외 (단계 4-2 에서 통합 테스트).
// 본 mock 은 Insert / Count / List / Purge 의 sequence 동작 + cap 정책 + UNIQUE 동작을 검증.
type inMemorySampleRepo struct {
	mu      sync.Mutex
	rows    []*model.SampleURL
	nextID  int64
	failErr error // Insert/Count 가 실패할 때 반환할 에러 (테스트 setup 용)
}

func newInMemorySampleRepo() *inMemorySampleRepo {
	return &inMemorySampleRepo{nextID: 1}
}

func (r *inMemorySampleRepo) Insert(_ context.Context, ruleID int64, url string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failErr != nil {
		return r.failErr
	}
	// cap check (postgres 구현 정책과 동일)
	count := 0
	for _, s := range r.rows {
		if s.RuleID == ruleID {
			count++
		}
	}
	if count >= model.SampleCapPerRule {
		return nil // skip
	}
	// UNIQUE check (rule_id, url)
	for _, s := range r.rows {
		if s.RuleID == ruleID && s.URL == url {
			return storage.ErrDuplicate
		}
	}
	r.rows = append(r.rows, &model.SampleURL{
		ID:         r.nextID,
		RuleID:     ruleID,
		URL:        url,
		ObservedAt: time.Now(),
	})
	r.nextID++
	return nil
}

func (r *inMemorySampleRepo) Count(_ context.Context, ruleID int64) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	count := 0
	for _, s := range r.rows {
		if s.RuleID == ruleID {
			count++
		}
	}
	return count, nil
}

func (r *inMemorySampleRepo) List(_ context.Context, ruleID int64, limit int) ([]*model.SampleURL, error) {
	if limit <= 0 {
		limit = 50
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*model.SampleURL, 0)
	for _, s := range r.rows {
		if s.RuleID == ruleID {
			out = append(out, s)
			if len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

func (r *inMemorySampleRepo) Purge(_ context.Context, ruleID int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	kept := make([]*model.SampleURL, 0, len(r.rows))
	for _, s := range r.rows {
		if s.RuleID != ruleID {
			kept = append(kept, s)
		}
	}
	r.rows = kept
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// 인터페이스 정책 검증
// ─────────────────────────────────────────────────────────────────────────────

func TestSampleURLRepository_InsertAndCount(t *testing.T) {
	repo := newInMemorySampleRepo()
	ctx := context.Background()

	require.NoError(t, repo.Insert(ctx, 1, "/article/1"))
	require.NoError(t, repo.Insert(ctx, 1, "/article/2"))
	require.NoError(t, repo.Insert(ctx, 2, "/news/1"))

	c1, err := repo.Count(ctx, 1)
	require.NoError(t, err)
	assert.Equal(t, 2, c1)

	c2, err := repo.Count(ctx, 2)
	require.NoError(t, err)
	assert.Equal(t, 1, c2)
}

// 같은 (rule_id, url) 중복 INSERT 는 ErrDuplicate.
func TestSampleURLRepository_DuplicateInsertReturnsErrDuplicate(t *testing.T) {
	repo := newInMemorySampleRepo()
	ctx := context.Background()

	require.NoError(t, repo.Insert(ctx, 1, "/article/1"))
	err := repo.Insert(ctx, 1, "/article/1")
	require.Error(t, err)
	assert.True(t, errors.Is(err, storage.ErrDuplicate))
}

// 다른 rule_id 이면 같은 URL 도 OK.
func TestSampleURLRepository_DifferentRuleAllowsSameURL(t *testing.T) {
	repo := newInMemorySampleRepo()
	ctx := context.Background()

	require.NoError(t, repo.Insert(ctx, 1, "/article/1"))
	require.NoError(t, repo.Insert(ctx, 2, "/article/1"))

	c1, _ := repo.Count(ctx, 1)
	c2, _ := repo.Count(ctx, 2)
	assert.Equal(t, 1, c1)
	assert.Equal(t, 1, c2)
}

// SampleCapPerRule 도달 시 INSERT skip + nil — 운영 cap 정책.
func TestSampleURLRepository_CapEnforced(t *testing.T) {
	repo := newInMemorySampleRepo()
	ctx := context.Background()

	for i := 0; i < model.SampleCapPerRule; i++ {
		url := "/article/" + intToStr(i)
		require.NoError(t, repo.Insert(ctx, 1, url))
	}
	count, _ := repo.Count(ctx, 1)
	assert.Equal(t, model.SampleCapPerRule, count)

	// cap 도달 후 INSERT — skip + nil
	err := repo.Insert(ctx, 1, "/article/over-cap")
	require.NoError(t, err, "cap 도달 시 skip + nil")

	countAfter, _ := repo.Count(ctx, 1)
	assert.Equal(t, model.SampleCapPerRule, countAfter, "cap 초과 INSERT 는 누적 안 됨")
}

func TestSampleURLRepository_ListReturnsRuleSamples(t *testing.T) {
	repo := newInMemorySampleRepo()
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		require.NoError(t, repo.Insert(ctx, 1, "/article/"+intToStr(i)))
	}
	require.NoError(t, repo.Insert(ctx, 2, "/news/1"))

	list, err := repo.List(ctx, 1, 50)
	require.NoError(t, err)
	assert.Len(t, list, 3, "rule_id=1 의 sample 3개")

	list2, _ := repo.List(ctx, 2, 50)
	assert.Len(t, list2, 1, "rule_id=2 의 sample 1개")
}

func TestSampleURLRepository_PurgeIdempotent(t *testing.T) {
	repo := newInMemorySampleRepo()
	ctx := context.Background()

	require.NoError(t, repo.Insert(ctx, 1, "/article/1"))
	require.NoError(t, repo.Insert(ctx, 1, "/article/2"))

	require.NoError(t, repo.Purge(ctx, 1))
	count, _ := repo.Count(ctx, 1)
	assert.Equal(t, 0, count)

	// 다시 Purge — 미존재여도 nil (idempotent)
	require.NoError(t, repo.Purge(ctx, 1))
}

// 컴파일 시점에 inMemorySampleRepo 가 SampleURLRepository 인터페이스 만족 검증.
var _ repository.SampleURLRepository = (*inMemorySampleRepo)(nil)

// intToStr — fmt 의존 회피용 단순 helper.
func intToStr(n int) string {
	if n == 0 {
		return "0"
	}
	digits := []byte{}
	if n < 0 {
		digits = append(digits, '-')
		n = -n
	}
	var rev []byte
	for n > 0 {
		rev = append(rev, byte('0'+n%10))
		n /= 10
	}
	for i := len(rev) - 1; i >= 0; i-- {
		digits = append(digits, rev[i])
	}
	return string(digits)
}
