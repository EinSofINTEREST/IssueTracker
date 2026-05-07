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

	"issuetracker/internal/processor/fetcher/core"
	"issuetracker/internal/processor/parser/rule"
	"issuetracker/internal/processor/parser/rule/llmgen"
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

	// findByNaturalKeyResult: 사전 lookup 시뮬레이션 (이슈 #274). nil 이면 ErrNotFound.
	findByNaturalKeyResult *storage.ParsingRuleRecord
	// findByNaturalKeyErr: 설정 시 result 무시하고 본 에러 반환 (PR #275 리뷰 — DB 장애 시뮬레이션).
	findByNaturalKeyErr   error
	findByNaturalKeyCalls int

	// insertCalls / insertNextVersionCalls: stale 경로가 InsertNextVersion (version bump) 만 사용하고
	// 일반 경로가 Insert (v=1 신규) 만 사용하는지 명시 검증 (이슈 #282, PR #294 CodeRabbit 피드백).
	insertCalls            int
	insertNextVersionCalls int
}

func (r *recordingRepo) Insert(_ context.Context, rec *storage.ParsingRuleRecord) error {
	if r.insertEr != nil {
		return r.insertEr
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.insertCalls++
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
func (r *recordingRepo) HasAnyRule(_ context.Context, _ string, _ storage.TargetType) (bool, bool, error) {
	return false, false, nil
}
func (r *recordingRepo) InsertNextVersion(ctx context.Context, rec *storage.ParsingRuleRecord) error {
	r.mu.Lock()
	r.insertNextVersionCalls++
	// 자연키 (source, host, path, type) 동일 row 의 MAX(version)+1 시뮬레이션 — postgres 구현과 일치.
	maxVer := 0
	for _, existing := range r.inserted {
		if existing.SourceName == rec.SourceName &&
			existing.HostPattern == rec.HostPattern &&
			existing.PathPattern == rec.PathPattern &&
			existing.TargetType == rec.TargetType {
			if existing.Version > maxVer {
				maxVer = existing.Version
			}
		}
	}
	rec.Version = maxVer + 1
	r.mu.Unlock()
	return r.Insert(ctx, rec)
}
func (r *recordingRepo) FindByNaturalKey(_ context.Context, _, _, _ string, _ storage.TargetType, _ int) (*storage.ParsingRuleRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.findByNaturalKeyCalls++
	if r.findByNaturalKeyErr != nil {
		return nil, r.findByNaturalKeyErr
	}
	if r.findByNaturalKeyResult != nil {
		return r.findByNaturalKeyResult, nil
	}
	return nil, storage.ErrNotFound
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
	resolver, _ := rule.NewResolver(&noopFindRepo{}, rule.WithCacheTTL(time.Minute))
	g, err := llmgen.New(provider, repo, resolver, logger.New(logger.DefaultConfig()))
	require.NoError(t, err)
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
func (noopFindRepo) HasAnyRule(_ context.Context, _ string, _ storage.TargetType) (bool, bool, error) {
	return false, false, nil
}
func (noopFindRepo) InsertNextVersion(_ context.Context, _ *storage.ParsingRuleRecord) error {
	return nil
}
func (noopFindRepo) FindByNaturalKey(_ context.Context, _, _, _ string, _ storage.TargetType, _ int) (*storage.ParsingRuleRecord, error) {
	return nil, storage.ErrNotFound
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

func TestGenerator_Enqueue_PageSuccess_InsertsEnabledRule(t *testing.T) {
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
	}, 0, "", 0)

	waitForInserts(t, repo, 1, 2*time.Second)

	got := repo.inserts()
	require.Len(t, got, 1)
	rec := got[0]
	assert.Equal(t, "example.com", rec.HostPattern)
	assert.Equal(t, storage.TargetTypePage, rec.TargetType)
	assert.True(t, rec.Enabled, "CSS selector 검증 통과 = 품질 게이트 — LLM 자동 생성 룰은 즉시 enabled=true")
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
	}, 0, "", 0)

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
	}, 0, "", 0)

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
	}, 0, "", 0)

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
			}, 0, "", 0)
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
	}, 0, "", 0)
	g.Enqueue(context.Background(), "b.example.com", storage.TargetTypePage, &core.RawContent{
		URL: "https://b.example.com/x", HTML: samplePageHTML,
	}, 0, "", 0)

	waitForInserts(t, repo, 2, 2*time.Second)
	assert.Equal(t, 2, provider.callCount())
}

func TestGenerator_Enqueue_EmptyHTML_NoCall(t *testing.T) {
	provider := &fakeProvider{name: "fake", response: "{}"}
	repo := &recordingRepo{}
	g, _ := newGenerator(t, provider, repo)

	g.Enqueue(context.Background(), "example.com", storage.TargetTypePage, &core.RawContent{
		URL: "https://example.com/x", HTML: "",
	}, 0, "", 0)

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
	}, 0, "", 0)

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
	}, 0, "", 0)

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
	}, 0, "", 0)

	// 호출자 ctx 즉시 cancel — LLM 호출 중에 cancel 됨. WithoutCancel 이라 호출은 계속 진행되어야 함.
	cancel()

	waitForInserts(t, repo, 1, 2*time.Second)
	assert.Equal(t, 1, provider.callCount(), "호출자 ctx cancel 후에도 LLM 호출은 best-effort 진행")
}

// ─────────────────────────────────────────────────────────────────────────────
// Extractor 경로 테스트 (이슈 #256)
// ─────────────────────────────────────────────────────────────────────────────

// fakeExtractor 는 SelectorExtractor 의 테스트용 구현입니다.
type fakeExtractor struct {
	sm  storage.SelectorMap
	err error
	mu  sync.Mutex
	n   int
}

func (e *fakeExtractor) Extract(_ context.Context, _ string, _ storage.TargetType, _ string) (storage.SelectorMap, error) {
	e.mu.Lock()
	e.n++
	e.mu.Unlock()
	return e.sm, e.err
}

func (e *fakeExtractor) callCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.n
}

// fakeExtractorWithModel 은 ModelName() 도 구현하는 SelectorExtractor 입니다.
type fakeExtractorWithModel struct {
	fakeExtractor
	modelName string
}

func (e *fakeExtractorWithModel) ModelName() string { return e.modelName }

// TestGenerator_SetExtractor_UsesExtractorNotProvider 는 SetExtractor 설정 시
// provider 대신 extractor 가 사용되는지 검증합니다 (이슈 #256).
func TestGenerator_SetExtractor_UsesExtractorNotProvider(t *testing.T) {
	provider := &fakeProvider{name: "gemini-flash", response: "{}"}
	repo := &recordingRepo{}
	g, _ := newGenerator(t, provider, repo)

	extractor := &fakeExtractor{
		sm: storage.SelectorMap{
			Title:       &storage.FieldSelector{CSS: "h1.article-title"},
			MainContent: &storage.FieldSelector{CSS: "article p", Multi: true},
		},
	}
	g.SetExtractor(extractor)

	g.Enqueue(context.Background(), "example.com", storage.TargetTypePage, &core.RawContent{
		URL: "https://example.com/article/1", HTML: samplePageHTML,
	}, 0, "", 0)

	waitForInserts(t, repo, 1, 2*time.Second)

	assert.Equal(t, 0, provider.callCount(), "extractor 설정 시 provider 는 호출 안 됨")
	assert.Equal(t, 1, extractor.callCount(), "extractor 가 1회 호출됨")
	recs := repo.inserts()
	require.Len(t, recs, 1)
	assert.Equal(t, "h1.article-title", recs[0].Selectors.Title.CSS)
}

// TestGenerator_SetExtractor_ModelNameFromInterface 는 extractor 가 ModelName() 을 구현하면
// 실제 모델 ID 가 DB description 에 기록되는지 검증합니다 (이슈 #256).
func TestGenerator_SetExtractor_ModelNameFromInterface(t *testing.T) {
	provider := &fakeProvider{name: "gemini-flash", response: "{}"}
	repo := &recordingRepo{}
	g, _ := newGenerator(t, provider, repo)

	extractor := &fakeExtractorWithModel{
		fakeExtractor: fakeExtractor{
			sm: storage.SelectorMap{
				Title:       &storage.FieldSelector{CSS: "h1.article-title"},
				MainContent: &storage.FieldSelector{CSS: "article p"},
			},
		},
		modelName: "claude-sonnet-4-6",
	}
	g.SetExtractor(extractor)

	g.Enqueue(context.Background(), "example.com", storage.TargetTypePage, &core.RawContent{
		URL: "https://example.com/article/1", HTML: samplePageHTML,
	}, 0, "", 0)

	waitForInserts(t, repo, 1, 2*time.Second)

	recs := repo.inserts()
	require.Len(t, recs, 1)
	assert.Contains(t, recs[0].Description, "claude-sonnet-4-6",
		"ModelName() 구현체의 모델 ID 가 description 에 포함되어야 함")
}

// TestGenerator_SetExtractor_FallbackModelName 는 extractor 가 ModelName() 을 구현하지 않으면
// fallback "claude-code" 가 description 에 기록되는지 검증합니다 (이슈 #256).
func TestGenerator_SetExtractor_FallbackModelName(t *testing.T) {
	provider := &fakeProvider{name: "gemini-flash", response: "{}"}
	repo := &recordingRepo{}
	g, _ := newGenerator(t, provider, repo)

	extractor := &fakeExtractor{
		sm: storage.SelectorMap{
			Title:       &storage.FieldSelector{CSS: "h1.article-title"},
			MainContent: &storage.FieldSelector{CSS: "article p"},
		},
	}
	g.SetExtractor(extractor)

	g.Enqueue(context.Background(), "example.com", storage.TargetTypePage, &core.RawContent{
		URL: "https://example.com/article/1", HTML: samplePageHTML,
	}, 0, "", 0)

	waitForInserts(t, repo, 1, 2*time.Second)

	recs := repo.inserts()
	require.Len(t, recs, 1)
	assert.Contains(t, recs[0].Description, "claude-code",
		"ModelName() 미구현 시 fallback 'claude-code' 가 description 에 포함되어야 함")
}

// TestGenerator_SetExtractor_Error_NoInsert 는 extractor 오류 시 INSERT 가 발생하지 않는지 검증합니다.
func TestGenerator_SetExtractor_Error_NoInsert(t *testing.T) {
	provider := &fakeProvider{name: "gemini-flash", response: "{}"}
	repo := &recordingRepo{}
	g, _ := newGenerator(t, provider, repo)

	extractor := &fakeExtractor{err: errors.New("docker exec failed")}
	g.SetExtractor(extractor)

	g.Enqueue(context.Background(), "example.com", storage.TargetTypePage, &core.RawContent{
		URL: "https://example.com/article/1", HTML: samplePageHTML,
	}, 0, "", 0)

	time.Sleep(300 * time.Millisecond)
	assert.Empty(t, repo.inserts(), "extractor 오류 시 INSERT 없어야 함")
}

// TestGenerator_SetExtractor_Nil_FallsBackToProvider 는 SetExtractor(nil) 호출 시
// 기존 provider 경로로 fallback 하는지 검증합니다.
func TestGenerator_SetExtractor_Nil_FallsBackToProvider(t *testing.T) {
	provider := &fakeProvider{
		name: "fake",
		response: `{
			"title": {"css": "h1.article-title"},
			"main_content": {"css": "article p"}
		}`,
	}
	repo := &recordingRepo{}
	g, _ := newGenerator(t, provider, repo)

	g.SetExtractor(nil) // nil → provider fallback

	g.Enqueue(context.Background(), "example.com", storage.TargetTypePage, &core.RawContent{
		URL: "https://example.com/article/1", HTML: samplePageHTML,
	}, 0, "", 0)

	waitForInserts(t, repo, 1, 2*time.Second)
	assert.Equal(t, 1, provider.callCount(), "nil extractor 시 provider 가 호출되어야 함")
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

// ─────────────────────────────────────────────────────────────────────────────
// 이슈 #274 — ErrDuplicate 흡수 + 사전 lookup
// ─────────────────────────────────────────────────────────────────────────────

// TestGenerator_DuplicateInsert_AbsorbedAsSuccess 는 Insert 가 ErrDuplicate 를 반환할 때
// generator 가 정상 경로로 흡수하여 selectorValidationError 분기 없이 종료되는지 검증합니다 (이슈 #274 — A).
func TestGenerator_DuplicateInsert_AbsorbedAsSuccess(t *testing.T) {
	provider := &fakeProvider{
		name: "fake",
		response: `{
			"title": {"css": "h1.article-title"},
			"main_content": {"css": "article p", "multi": true}
		}`,
	}
	// Insert 가 ErrDuplicate 를 반환하도록 구성. inserted 슬라이스는 비어있어야 함 (race 없음).
	repo := &recordingRepo{insertEr: storage.ErrDuplicate}
	g, _ := newGenerator(t, provider, repo)

	g.Enqueue(context.Background(), "example.com", storage.TargetTypePage, &core.RawContent{
		URL: "https://example.com/article/1", HTML: samplePageHTML,
	}, 0, "", 0)

	// Stop 으로 in-flight goroutine 종료 대기 — provider 호출은 발생했어야 함.
	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	g.Stop(stopCtx)

	assert.Equal(t, 1, provider.callCount(), "LLM 호출은 발생해야 함 (사전 lookup miss)")
	assert.Empty(t, repo.inserts(), "Insert ErrDuplicate 시 inserted 누적 없음 (recordingRepo 의 insertEr 분기)")
	// 핵심 회귀 방어: validateFailureHandler 등록 없이 ErrDuplicate 가 selectorValidationError 로
	// wrap 되지 않고 정상 종료됨 — 패닉 / 무한 루프 / requeue 폭주 없음.
}

// TestGenerator_PreCheckHit_SkipsLLMCall 은 사전 lookup 적중 시 LLM 호출이 발생하지 않는지 검증합니다 (이슈 #274 — B).
func TestGenerator_PreCheckHit_SkipsLLMCall(t *testing.T) {
	provider := &fakeProvider{name: "fake", response: `{"title":{"css":"h1"}}`}
	repo := &recordingRepo{
		findByNaturalKeyResult: &storage.ParsingRuleRecord{
			ID:          42,
			SourceName:  llmgen.LLMAutoSourceName,
			HostPattern: "example.com",
			TargetType:  storage.TargetTypePage,
			Version:     1,
			Enabled:     true,
		},
	}
	g, _ := newGenerator(t, provider, repo)

	g.Enqueue(context.Background(), "example.com", storage.TargetTypePage, &core.RawContent{
		URL: "https://example.com/article/1", HTML: samplePageHTML,
	}, 0, "", 0)

	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	g.Stop(stopCtx)

	assert.Equal(t, 0, provider.callCount(), "사전 lookup 적중 시 LLM 호출 미발생")
	assert.Empty(t, repo.inserts(), "사전 lookup 적중 시 Insert 호출 미발생")
	repo.mu.Lock()
	assert.Equal(t, 1, repo.findByNaturalKeyCalls, "사전 lookup 1회 호출")
	repo.mu.Unlock()
}

// TestGenerator_PreCheckMiss_ProceedsToLLM 는 사전 lookup miss 시 기존 경로 (LLM 호출 + Insert) 가
// 정상 동작하는지 검증합니다 (이슈 #274 — B).
func TestGenerator_PreCheckMiss_ProceedsToLLM(t *testing.T) {
	provider := &fakeProvider{
		name: "fake",
		response: `{
			"title": {"css": "h1.article-title"},
			"main_content": {"css": "article p", "multi": true}
		}`,
	}
	// findByNaturalKeyResult 미설정 → ErrNotFound 반환 → LLM 경로 진행.
	repo := &recordingRepo{}
	g, _ := newGenerator(t, provider, repo)

	g.Enqueue(context.Background(), "example.com", storage.TargetTypePage, &core.RawContent{
		URL: "https://example.com/article/1", HTML: samplePageHTML,
	}, 0, "", 0)

	waitForInserts(t, repo, 1, 2*time.Second)

	assert.Equal(t, 1, provider.callCount(), "사전 lookup miss 시 LLM 호출 발생")
	require.Len(t, repo.inserts(), 1)
	repo.mu.Lock()
	assert.Equal(t, 1, repo.findByNaturalKeyCalls, "사전 lookup 1회 호출")
	repo.mu.Unlock()
}

// TestGenerator_PreCheckHit_DisabledRule_SkipsLLM 은 사전 lookup 적중이지만 enabled=false 일 때
// LLM 호출 없이 warn 로그만 남기고 종료하는지 검증합니다 (PR #275 리뷰).
func TestGenerator_PreCheckHit_DisabledRule_SkipsLLM(t *testing.T) {
	provider := &fakeProvider{name: "fake", response: `{"title":{"css":"h1"}}`}
	repo := &recordingRepo{
		findByNaturalKeyResult: &storage.ParsingRuleRecord{
			ID:          99,
			SourceName:  llmgen.LLMAutoSourceName,
			HostPattern: "example.com",
			TargetType:  storage.TargetTypePage,
			Version:     1,
			Enabled:     false, // 운영자 disabled — 자동 재활성 회피
		},
	}
	g, _ := newGenerator(t, provider, repo)

	g.Enqueue(context.Background(), "example.com", storage.TargetTypePage, &core.RawContent{
		URL: "https://example.com/article/1", HTML: samplePageHTML,
	}, 0, "", 0)

	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	g.Stop(stopCtx)

	assert.Equal(t, 0, provider.callCount(), "disabled 룰 적중 시 LLM 호출 미발생")
	assert.Empty(t, repo.inserts(), "disabled 룰 적중 시 Insert 미발생")
}

// TestGenerator_PreCheckLookupError_FallthroughToLLM 은 사전 lookup 이 ErrNotFound 외 에러를
// 반환할 때 LLM 경로로 fallthrough 하는지 검증합니다 (PR #275 리뷰 — DB 장애 best-effort 정책).
func TestGenerator_PreCheckLookupError_FallthroughToLLM(t *testing.T) {
	provider := &fakeProvider{
		name: "fake",
		response: `{
			"title": {"css": "h1.article-title"},
			"main_content": {"css": "article p", "multi": true}
		}`,
	}
	// 일시 DB 장애 시뮬레이션 — ErrNotFound 외 에러
	repo := &recordingRepo{findByNaturalKeyErr: errors.New("db connection refused")}
	g, _ := newGenerator(t, provider, repo)

	g.Enqueue(context.Background(), "example.com", storage.TargetTypePage, &core.RawContent{
		URL: "https://example.com/article/1", HTML: samplePageHTML,
	}, 0, "", 0)

	waitForInserts(t, repo, 1, 2*time.Second)

	assert.Equal(t, 1, provider.callCount(), "lookup 에러 시 LLM 경로 fallthrough — best-effort 정책")
	require.Len(t, repo.inserts(), 1, "lookup 에러 후에도 Insert 진행")
}

// ─────────────────────────────────────────────────────────────────────────────
// EnqueueStale (이슈 #282)
// ─────────────────────────────────────────────────────────────────────────────

// EnqueueStale 가 InsertNextVersion 경로로 갈지 검증.
func TestGenerator_EnqueueStale_InsertNextVersion(t *testing.T) {
	provider := &fakeProvider{
		name: "fake",
		response: `{
			"title": {"css": "h1.article-title"},
			"main_content": {"css": "article p", "multi": true}
		}`,
	}
	repo := &recordingRepo{}
	g, _ := newGenerator(t, provider, repo)

	g.EnqueueStale(context.Background(), "stale.example.com", storage.TargetTypePage, &core.RawContent{
		URL: "https://stale.example.com/article/1", HTML: samplePageHTML,
	}, 0, "", 0)

	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	g.Stop(stopCtx)

	repo.mu.Lock()
	defer repo.mu.Unlock()
	// PR #294 CodeRabbit 피드백: InsertNextVersion 호출 자체를 명시 검증 — 일반 Insert 로 우회되는
	// regression 차단. 두 카운터는 상호 배타적으로 1/0 이어야 함 (stale 경로는 항상 InsertNextVersion).
	assert.Equal(t, 1, provider.callCount(), "EnqueueStale 도 LLM 호출은 발생")
	assert.Equal(t, 1, repo.insertNextVersionCalls, "EnqueueStale must use InsertNextVersion (not plain Insert)")
	assert.Len(t, repo.inserted, 1, "InsertNextVersion 경유로 inserted 누적")
}

// EnqueueStale 의 pre-check 가 enabled=true 룰 적중해도 skip 안 함을 검증 (vs Enqueue 의 skip 동작).
func TestGenerator_EnqueueStale_BypassesEnabledPreCheck(t *testing.T) {
	provider := &fakeProvider{
		name: "fake",
		response: `{
			"title": {"css": "h1.article-title"},
			"main_content": {"css": "article p", "multi": true}
		}`,
	}
	// 사전 lookup 결과 — enabled=true 룰 잔존 (정상적으로 fresh Enqueue 면 skip 대상).
	// 동일 자연키 row 를 inserted 에 미리 시딩하여 InsertNextVersion 의 MAX(version)+1 동작이
	// v=2 를 산출하도록 — postgres 의 실제 자연키 충돌 회피 동작과 일치.
	existingV1 := &storage.ParsingRuleRecord{
		ID:          1,
		SourceName:  llmgen.LLMAutoSourceName,
		HostPattern: "stale.example.com",
		PathPattern: "",
		TargetType:  storage.TargetTypePage,
		Version:     1,
		Enabled:     true,
	}
	repo := &recordingRepo{
		inserted:               []*storage.ParsingRuleRecord{existingV1},
		findByNaturalKeyResult: existingV1,
	}
	g, _ := newGenerator(t, provider, repo)

	g.EnqueueStale(context.Background(), "stale.example.com", storage.TargetTypePage, &core.RawContent{
		URL: "https://stale.example.com/article/1", HTML: samplePageHTML,
	}, 0, "", 0)

	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	g.Stop(stopCtx)

	assert.Equal(t, 1, provider.callCount(), "EnqueueStale 는 enabled 룰 적중해도 LLM 호출 진행 (v2 학습)")
	repo.mu.Lock()
	defer repo.mu.Unlock()
	// PR #294 CodeRabbit 피드백: InsertNextVersion 사용 여부를 직접 검증 — 일반 Insert 로 v=1 row 생성
	// 시 자연키 충돌 (이미 enabled v=1 row 존재) 로 stale 경로가 망가짐을 사전 차단.
	assert.Equal(t, 1, repo.insertNextVersionCalls, "stale path must call InsertNextVersion")
	assert.Equal(t, 0, repo.insertCalls-repo.insertNextVersionCalls, "no plain Insert (only InsertNextVersion → Insert)")
	require.Len(t, repo.inserted, 2, "기존 v=1 + 신규 v=2")
	assert.Equal(t, 1, repo.inserted[0].Version, "기존 v=1 보존")
	assert.Equal(t, 2, repo.inserted[1].Version, "InsertNextVersion bumps to v=2 (existing enabled v=1 detected)")
}
