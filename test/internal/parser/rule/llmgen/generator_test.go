package llmgen_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/crawler/core"
	"issuetracker/internal/crawler/parser/rule"
	"issuetracker/internal/crawler/parser/rule/llmgen"
	"issuetracker/internal/storage"
	"issuetracker/pkg/llm"
	"issuetracker/pkg/logger"
)

// ─────────────────────────────────────────────────────────────────────────────
// Fakes
// ─────────────────────────────────────────────────────────────────────────────

// fakeProvider 는 미리 설정된 응답을 반환하는 llm.Provider 입니다.
type fakeProvider struct {
	name     string
	response string
	err      error

	mu    sync.Mutex
	calls int
}

func (p *fakeProvider) Name() string { return p.name }
func (p *fakeProvider) Generate(_ context.Context, _ llm.Request) (*llm.Response, error) {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	if p.err != nil {
		return nil, p.err
	}
	return &llm.Response{Content: p.response, Model: p.name}, nil
}
func (p *fakeProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// recordingRepo 는 Insert 호출을 기록하는 ParsingRuleRepository 구현입니다.
type recordingRepo struct {
	mu       sync.Mutex
	inserted []*storage.ParsingRuleRecord
	insertEr error
}

func (r *recordingRepo) Insert(_ context.Context, rec *storage.ParsingRuleRecord) error {
	if r.insertEr != nil {
		return r.insertEr
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	rec.ID = int64(len(r.inserted) + 1)
	clone := *rec
	r.inserted = append(r.inserted, &clone)
	return nil
}
func (r *recordingRepo) Update(_ context.Context, _ *storage.ParsingRuleRecord) error { return nil }
func (r *recordingRepo) GetByID(_ context.Context, _ int64) (*storage.ParsingRuleRecord, error) {
	return nil, storage.ErrNotFound
}
func (r *recordingRepo) FindActive(_ context.Context, _ string, _ storage.TargetType) (*storage.ParsingRuleRecord, error) {
	return nil, storage.ErrNotFound
}
func (r *recordingRepo) FindActiveCandidates(_ context.Context, _ string, _ storage.TargetType) ([]*storage.ParsingRuleRecord, error) {
	return nil, nil
}
func (r *recordingRepo) List(_ context.Context, _ storage.ParsingRuleFilter) ([]*storage.ParsingRuleRecord, error) {
	return nil, nil
}
func (r *recordingRepo) Delete(_ context.Context, _ int64) error { return nil }
func (r *recordingRepo) UpdatePathPattern(_ context.Context, _ int64, _, _ string) error {
	return nil
}
func (r *recordingRepo) inserts() []*storage.ParsingRuleRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*storage.ParsingRuleRecord, len(r.inserted))
	copy(out, r.inserted)
	return out
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

const samplePageHTML = `<!DOCTYPE html><html><body>
<h1 class="article-title">Sample Headline</h1>
<article><p>Body paragraph one.</p><p>Body paragraph two.</p></article>
<span class="author">Jane Doe</span>
</body></html>`

const sampleListHTML = `<!DOCTYPE html><html><body>
<ul class="news-list">
  <li class="news-item"><a href="/article/1">First</a></li>
  <li class="news-item"><a href="/article/2">Second</a></li>
</ul>
</body></html>`

func newGenerator(t *testing.T, provider llm.Provider, repo storage.ParsingRuleRepository) (*llmgen.Generator, *rule.Resolver) {
	t.Helper()
	resolver := rule.NewResolver(&noopFindRepo{}, rule.WithCacheTTL(time.Minute))
	g := llmgen.New(provider, repo, resolver, logger.New(logger.DefaultConfig()))
	return g, resolver
}

// noopFindRepo: Resolver 가 요구하는 ParsingRuleRepository 인터페이스를 만족하는 stub.
type noopFindRepo struct{}

func (noopFindRepo) Insert(_ context.Context, _ *storage.ParsingRuleRecord) error { return nil }
func (noopFindRepo) Update(_ context.Context, _ *storage.ParsingRuleRecord) error { return nil }
func (noopFindRepo) GetByID(_ context.Context, _ int64) (*storage.ParsingRuleRecord, error) {
	return nil, storage.ErrNotFound
}
func (noopFindRepo) FindActive(_ context.Context, _ string, _ storage.TargetType) (*storage.ParsingRuleRecord, error) {
	return nil, storage.ErrNotFound
}
func (noopFindRepo) FindActiveCandidates(_ context.Context, _ string, _ storage.TargetType) ([]*storage.ParsingRuleRecord, error) {
	return nil, nil
}
func (noopFindRepo) List(_ context.Context, _ storage.ParsingRuleFilter) ([]*storage.ParsingRuleRecord, error) {
	return nil, nil
}
func (noopFindRepo) Delete(_ context.Context, _ int64) error { return nil }
func (noopFindRepo) UpdatePathPattern(_ context.Context, _ int64, _, _ string) error {
	return nil
}

// waitForInserts 는 polling 으로 비동기 Insert 가 발생할 때까지 대기합니다.
// timeout 안에 want 만큼 발생하지 않으면 t.Fatal.
func waitForInserts(t *testing.T, repo *recordingRepo, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if len(repo.inserts()) >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected %d inserts within %s, got %d", want, timeout, len(repo.inserts()))
}

// ─────────────────────────────────────────────────────────────────────────────
// Tests
// ─────────────────────────────────────────────────────────────────────────────

func TestGenerator_Enqueue_PageSuccess_InsertsDisabledRule(t *testing.T) {
	provider := &fakeProvider{
		name: "fake",
		response: `{
			"title": {"css": "h1.article-title"},
			"main_content": {"css": "article p", "multi": true},
			"author": {"css": "span.author"}
		}`,
	}
	repo := &recordingRepo{}
	g, _ := newGenerator(t, provider, repo)

	g.Enqueue(context.Background(), "example.com", storage.TargetTypePage, &core.RawContent{
		URL: "https://example.com/article/1", HTML: samplePageHTML,
	})

	waitForInserts(t, repo, 1, 2*time.Second)

	got := repo.inserts()
	require.Len(t, got, 1)
	rec := got[0]
	assert.Equal(t, "example.com", rec.HostPattern)
	assert.Equal(t, storage.TargetTypePage, rec.TargetType)
	assert.False(t, rec.Enabled, "운영자 review 게이트 — 자동 생성은 enabled=false")
	assert.Equal(t, "llm-auto", rec.SourceName)
	require.NotNil(t, rec.Selectors.Title)
	assert.Equal(t, "h1.article-title", rec.Selectors.Title.CSS)
	require.NotNil(t, rec.Selectors.MainContent)
	assert.Equal(t, "article p", rec.Selectors.MainContent.CSS)
}

func TestGenerator_Enqueue_ListSuccess(t *testing.T) {
	provider := &fakeProvider{
		name: "fake",
		response: `{
			"item_container": {"css": "li.news-item"},
			"item_link": {"css": "a", "attribute": "href"}
		}`,
	}
	repo := &recordingRepo{}
	g, _ := newGenerator(t, provider, repo)

	g.Enqueue(context.Background(), "example.com", storage.TargetTypeList, &core.RawContent{
		URL: "https://example.com/news", HTML: sampleListHTML,
	})

	waitForInserts(t, repo, 1, 2*time.Second)
	rec := repo.inserts()[0]
	assert.Equal(t, storage.TargetTypeList, rec.TargetType)
	require.NotNil(t, rec.Selectors.ItemContainer)
	assert.Equal(t, "li.news-item", rec.Selectors.ItemContainer.CSS)
}

func TestGenerator_Enqueue_ValidationFailure_NoInsert(t *testing.T) {
	// Title selector 가 매칭 0건 → validation reject → INSERT 안 됨
	provider := &fakeProvider{
		name: "fake",
		response: `{
			"title": {"css": "h1.does-not-exist"},
			"main_content": {"css": "article p"}
		}`,
	}
	repo := &recordingRepo{}
	g, _ := newGenerator(t, provider, repo)

	g.Enqueue(context.Background(), "example.com", storage.TargetTypePage, &core.RawContent{
		URL: "https://example.com/article/1", HTML: samplePageHTML,
	})

	// validation reject 시 INSERT 가 발생하지 않는지 확인 — 충분한 wait 후에도 0건이어야 함
	time.Sleep(300 * time.Millisecond)
	assert.Empty(t, repo.inserts(), "validation 실패 시 INSERT 없어야 함")
}

func TestGenerator_Enqueue_LLMError_NoInsert(t *testing.T) {
	provider := &fakeProvider{name: "fake", err: errors.New("network down")}
	repo := &recordingRepo{}
	g, _ := newGenerator(t, provider, repo)

	g.Enqueue(context.Background(), "example.com", storage.TargetTypePage, &core.RawContent{
		URL: "https://example.com/x", HTML: samplePageHTML,
	})

	// async goroutine 이 LLM 호출 후 실패 — INSERT 없어야 함
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) && provider.callCount() == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	assert.Equal(t, 1, provider.callCount(), "LLM 1회 호출됐어야 함")
	assert.Empty(t, repo.inserts())
}

func TestGenerator_Enqueue_InflightDedup_SingleLLMCall(t *testing.T) {
	// 동일 host 에 대해 동시 100회 Enqueue → LLM 호출은 1회만 (in-flight dedup)
	const enqueueCount = 100

	// LLM 응답에 약간의 지연 — 첫 호출이 끝나기 전에 나머지 99회가 dedup 되어야 함
	provider := &slowProvider{
		fakeProvider: fakeProvider{
			name: "fake",
			response: `{
				"title": {"css": "h1.article-title"},
				"main_content": {"css": "article p"}
			}`,
		},
		delay: 100 * time.Millisecond,
	}
	repo := &recordingRepo{}
	g, _ := newGenerator(t, provider, repo)

	var wg sync.WaitGroup
	for i := 0; i < enqueueCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			g.Enqueue(context.Background(), "example.com", storage.TargetTypePage, &core.RawContent{
				URL: "https://example.com/x", HTML: samplePageHTML,
			})
		}()
	}
	wg.Wait()

	waitForInserts(t, repo, 1, 2*time.Second)
	assert.Equal(t, 1, provider.callCount(), "in-flight dedup — 동일 host 동시 enqueue 는 LLM 1회만 호출")
	assert.Len(t, repo.inserts(), 1)
}

func TestGenerator_Enqueue_DifferentHosts_BothCalled(t *testing.T) {
	// 다른 host 두 개는 dedup 영향 없이 둘 다 호출
	provider := &fakeProvider{
		name: "fake",
		response: `{
			"title": {"css": "h1.article-title"},
			"main_content": {"css": "article p"}
		}`,
	}
	repo := &recordingRepo{}
	g, _ := newGenerator(t, provider, repo)

	g.Enqueue(context.Background(), "a.example.com", storage.TargetTypePage, &core.RawContent{
		URL: "https://a.example.com/x", HTML: samplePageHTML,
	})
	g.Enqueue(context.Background(), "b.example.com", storage.TargetTypePage, &core.RawContent{
		URL: "https://b.example.com/x", HTML: samplePageHTML,
	})

	waitForInserts(t, repo, 2, 2*time.Second)
	assert.Equal(t, 2, provider.callCount())
}

func TestGenerator_Enqueue_EmptyHTML_NoCall(t *testing.T) {
	provider := &fakeProvider{name: "fake", response: "{}"}
	repo := &recordingRepo{}
	g, _ := newGenerator(t, provider, repo)

	g.Enqueue(context.Background(), "example.com", storage.TargetTypePage, &core.RawContent{
		URL: "https://example.com/x", HTML: "",
	})

	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, 0, provider.callCount(), "빈 HTML 은 LLM 호출 skip")
	assert.Empty(t, repo.inserts())
}

// Stop 은 진행 중인 background goroutine 의 완료를 대기해야 함 (graceful shutdown, PR #168 gemini 피드백).
func TestGenerator_Stop_WaitsForInflightGoroutines(t *testing.T) {
	provider := &slowProvider{
		fakeProvider: fakeProvider{
			name: "fake",
			response: `{
				"title": {"css": "h1.article-title"},
				"main_content": {"css": "article p"}
			}`,
		},
		delay: 200 * time.Millisecond,
	}
	repo := &recordingRepo{}
	g, _ := newGenerator(t, provider, repo)

	g.Enqueue(context.Background(), "example.com", storage.TargetTypePage, &core.RawContent{
		URL: "https://example.com/x", HTML: samplePageHTML,
	})

	// 충분한 timeout 으로 Stop — provider delay (200ms) 가 끝날 때까지 대기.
	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	g.Stop(stopCtx)

	// Stop 반환 후에는 INSERT 가 이미 완료되어 있어야 함 (in-flight 대기 보장).
	assert.Len(t, repo.inserts(), 1, "Stop 은 in-flight goroutine 완료를 대기해야 함")
}

// Stop 후 Enqueue 는 noop — 새 작업 차단.
func TestGenerator_EnqueueAfterStop_NoOp(t *testing.T) {
	provider := &fakeProvider{name: "fake", response: "{}"}
	repo := &recordingRepo{}
	g, _ := newGenerator(t, provider, repo)

	g.Stop(context.Background())

	g.Enqueue(context.Background(), "example.com", storage.TargetTypePage, &core.RawContent{
		URL: "https://example.com/x", HTML: samplePageHTML,
	})

	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, 0, provider.callCount(), "Stop 이후 Enqueue 는 LLM 호출하지 않아야 함")
}

// Stop 은 idempotent — 여러 번 호출되어도 안전.
func TestGenerator_Stop_Idempotent(t *testing.T) {
	provider := &fakeProvider{name: "fake", response: "{}"}
	repo := &recordingRepo{}
	g, _ := newGenerator(t, provider, repo)

	assert.NotPanics(t, func() {
		g.Stop(context.Background())
		g.Stop(context.Background())
		g.Stop(context.Background())
	}, "Stop 은 여러 번 호출되어도 안전")
}

// 호출자 ctx cancel 되어도 LLM 호출은 진행 (best-effort) — context.WithoutCancel 동작 검증.
func TestGenerator_Enqueue_OutlivesCallerCtxCancel(t *testing.T) {
	provider := &slowProvider{
		fakeProvider: fakeProvider{
			name: "fake",
			response: `{
				"title": {"css": "h1.article-title"},
				"main_content": {"css": "article p"}
			}`,
		},
		delay: 100 * time.Millisecond,
	}
	repo := &recordingRepo{}
	g, _ := newGenerator(t, provider, repo)

	callerCtx, cancel := context.WithCancel(context.Background())
	g.Enqueue(callerCtx, "example.com", storage.TargetTypePage, &core.RawContent{
		URL: "https://example.com/x", HTML: samplePageHTML,
	})

	// 호출자 ctx 즉시 cancel — LLM 호출 중에 cancel 됨. WithoutCancel 이라 호출은 계속 진행되어야 함.
	cancel()

	waitForInserts(t, repo, 1, 2*time.Second)
	assert.Equal(t, 1, provider.callCount(), "호출자 ctx cancel 후에도 LLM 호출은 best-effort 진행")
}

// ─────────────────────────────────────────────────────────────────────────────
// helpers
// ─────────────────────────────────────────────────────────────────────────────

// slowProvider 는 응답에 지연을 추가합니다 — in-flight dedup race window 보장.
type slowProvider struct {
	fakeProvider
	delay   time.Duration
	pending atomic.Int64
}

func (s *slowProvider) Generate(ctx context.Context, req llm.Request) (*llm.Response, error) {
	s.pending.Add(1)
	defer s.pending.Add(-1)
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(s.delay):
	}
	return s.fakeProvider.Generate(ctx, req)
}
