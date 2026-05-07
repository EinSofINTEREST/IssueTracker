package llmgen_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/processor/fetcher/core"
	"issuetracker/internal/processor/parser/rule/llmgen"
	"issuetracker/internal/storage"
)

// ─────────────────────────────────────────────────────────────────────────────
// pendingQueue 통합 테스트 — memPendingQueue (in-process fake)
// ─────────────────────────────────────────────────────────────────────────────

// memPendingQueue 는 테스트용 in-process storage.PendingQueue 구현입니다.
// payload 는 raw bytes 로 보관 — Generator 가 marshal/unmarshal 책임을 가짐.
type memPendingQueue struct {
	mu    sync.Mutex
	items map[string][][]byte
}

func newMemPendingQueue() *memPendingQueue {
	return &memPendingQueue{items: make(map[string][][]byte)}
}

func (m *memPendingQueue) Push(_ context.Context, host string, targetType storage.TargetType, payload []byte) error {
	key := host + ":" + string(targetType)
	m.mu.Lock()
	// payload 를 복사하여 호출자 측 buffer 변경에 영향 받지 않도록.
	cp := make([]byte, len(payload))
	copy(cp, payload)
	m.items[key] = append(m.items[key], cp)
	m.mu.Unlock()
	return nil
}

func (m *memPendingQueue) Flush(_ context.Context, host string, targetType storage.TargetType) ([][]byte, error) {
	key := host + ":" + string(targetType)
	m.mu.Lock()
	defer m.mu.Unlock()
	out := m.items[key]
	delete(m.items, key)
	return out, nil
}

func (m *memPendingQueue) count(host string, targetType storage.TargetType) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.items[host+":"+string(targetType)])
}

// Ensure memPendingQueue implements storage.PendingQueue
var _ storage.PendingQueue = (*memPendingQueue)(nil)

// ─────────────────────────────────────────────────────────────────────────────
// Tests
// ─────────────────────────────────────────────────────────────────────────────

// TestGenerator_PendingQueue_InFlight_Queued 는 in-flight 중 동일 도메인 URL 이
// pendingQueue 에 적재되는지 검증합니다 (이슈 #262).
func TestGenerator_PendingQueue_InFlight_Queued(t *testing.T) {
	provider := &slowProvider{
		fakeProvider: fakeProvider{
			name: "fake",
			response: `{
				"title": {"css": "h1.article-title"},
				"main_content": {"css": "article p"}
			}`,
		},
		delay: 150 * time.Millisecond,
	}
	repo := &recordingRepo{}
	g, _ := newGenerator(t, provider, repo)

	pq := newMemPendingQueue()
	var requeued []llmgen.PendingItem
	var requeueMu sync.Mutex
	g.SetPendingQueue(pq, func(_ context.Context, items []llmgen.PendingItem) []llmgen.PendingItem {
		requeueMu.Lock()
		requeued = append(requeued, items...)
		requeueMu.Unlock()
		return nil
	})

	// URL 1: 슬롯 획득 → LLM 시작
	g.Enqueue(context.Background(), "example.com", storage.TargetTypePage, &core.RawContent{
		URL: "https://example.com/article/1", HTML: samplePageHTML,
	}, 0, "", 0)

	// URL 2: in-flight 중 → pendingQueue 적재
	time.Sleep(20 * time.Millisecond) // URL 1 goroutine 확실히 시작 후
	g.Enqueue(context.Background(), "example.com", storage.TargetTypePage, &core.RawContent{
		URL: "https://example.com/article/2", HTML: samplePageHTML,
	}, 0, "", 0)

	assert.Equal(t, 1, pq.count("example.com", storage.TargetTypePage), "URL 2는 pendingQueue에 적재되어야 함")

	// LLM 완료 대기 → pending flush 후 requeueFn 호출
	waitForInserts(t, repo, 1, 2*time.Second)
	time.Sleep(50 * time.Millisecond) // flush + requeue 비동기 완료 대기

	requeueMu.Lock()
	requeuedCount := len(requeued)
	requeueMu.Unlock()

	assert.Equal(t, 1, requeuedCount, "룰 생성 완료 후 pending URL 1개가 requeueFn으로 전달되어야 함")
	assert.Equal(t, 0, pq.count("example.com", storage.TargetTypePage), "flush 후 pendingQueue는 비어야 함")
}

// TestGenerator_PendingQueue_LLMFail_NotFlushed 는 LLM 실패 시 pendingQueue 가
// flush 되지 않는지 검증합니다 (이슈 #262 — 룰 없이 재파싱 의미 없음).
func TestGenerator_PendingQueue_LLMFail_NotFlushed(t *testing.T) {
	provider := &slowProvider{
		fakeProvider: fakeProvider{name: "fake", err: assert.AnError},
		delay:        50 * time.Millisecond,
	}
	repo := &recordingRepo{}
	g, _ := newGenerator(t, provider, repo)

	pq := newMemPendingQueue()
	var requeueCalled bool
	g.SetPendingQueue(pq, func(_ context.Context, _ []llmgen.PendingItem) []llmgen.PendingItem {
		requeueCalled = true
		return nil
	})

	g.Enqueue(context.Background(), "example.com", storage.TargetTypePage, &core.RawContent{
		URL: "https://example.com/article/1", HTML: samplePageHTML,
	}, 0, "", 0)

	time.Sleep(20 * time.Millisecond)
	g.Enqueue(context.Background(), "example.com", storage.TargetTypePage, &core.RawContent{
		URL: "https://example.com/article/2", HTML: samplePageHTML,
	}, 0, "", 0)

	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	g.Stop(stopCtx)

	assert.False(t, requeueCalled, "LLM 실패 시 requeueFn 호출 없어야 함")
	assert.Equal(t, 1, pq.count("example.com", storage.TargetTypePage), "LLM 실패 시 pending URL 보존되어야 함")
}

// TestGenerator_NoPendingQueue_SkipsBehaviorUnchanged 는 pendingQueue 미설정 시
// 기존 skip 동작을 유지하는지 검증합니다.
func TestGenerator_NoPendingQueue_SkipsBehaviorUnchanged(t *testing.T) {
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
	g, _ := newGenerator(t, provider, repo) // pendingQueue 미설정

	g.Enqueue(context.Background(), "example.com", storage.TargetTypePage, &core.RawContent{
		URL: "https://example.com/article/1", HTML: samplePageHTML,
	}, 0, "", 0)

	time.Sleep(20 * time.Millisecond)
	g.Enqueue(context.Background(), "example.com", storage.TargetTypePage, &core.RawContent{
		URL: "https://example.com/article/2", HTML: samplePageHTML,
	}, 0, "", 0)

	waitForInserts(t, repo, 1, 2*time.Second)
	assert.Equal(t, 1, provider.callCount(), "pendingQueue 없으면 LLM은 1회만 호출")
}

// TestGenerator_PendingQueue_DifferentHosts_Independent 는 서로 다른 도메인의
// pendingQueue 가 독립적으로 동작하는지 검증합니다.
func TestGenerator_PendingQueue_DifferentHosts_Independent(t *testing.T) {
	provider := &fakeProvider{
		name: "fake",
		response: `{
			"title": {"css": "h1.article-title"},
			"main_content": {"css": "article p"}
		}`,
	}
	repo := &recordingRepo{}
	g, _ := newGenerator(t, provider, repo)

	pq := newMemPendingQueue()
	g.SetPendingQueue(pq, func(_ context.Context, _ []llmgen.PendingItem) []llmgen.PendingItem { return nil })

	g.Enqueue(context.Background(), "a.com", storage.TargetTypePage, &core.RawContent{
		URL: "https://a.com/x", HTML: samplePageHTML,
	}, 0, "", 0)
	g.Enqueue(context.Background(), "b.com", storage.TargetTypePage, &core.RawContent{
		URL: "https://b.com/x", HTML: samplePageHTML,
	}, 0, "", 0)

	waitForInserts(t, repo, 2, 2*time.Second)
	assert.Equal(t, 2, provider.callCount(), "다른 도메인은 각자 LLM 호출")
	assert.Equal(t, 0, pq.count("a.com", storage.TargetTypePage), "a.com pending 없어야 함")
	assert.Equal(t, 0, pq.count("b.com", storage.TargetTypePage), "b.com pending 없어야 함")
}

// TestPendingItem_RequeueFunc_ReceivesCorrectData 는 requeueFn 이 올바른 PendingItem 을
// 전달받는지 검증합니다.
func TestPendingItem_RequeueFunc_ReceivesCorrectData(t *testing.T) {
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

	pq := newMemPendingQueue()
	var received []llmgen.PendingItem
	var mu sync.Mutex
	g.SetPendingQueue(pq, func(_ context.Context, items []llmgen.PendingItem) []llmgen.PendingItem {
		mu.Lock()
		received = append(received, items...)
		mu.Unlock()
		return nil
	})

	g.Enqueue(context.Background(), "example.com", storage.TargetTypePage, &core.RawContent{
		URL: "https://example.com/article/1", HTML: samplePageHTML,
	}, 0, "test-crawler", 0)

	time.Sleep(20 * time.Millisecond)
	g.Enqueue(context.Background(), "example.com", storage.TargetTypePage, &core.RawContent{
		URL: "https://example.com/article/2", HTML: samplePageHTML,
	}, 1, "test-crawler", 0)

	waitForInserts(t, repo, 1, 2*time.Second)
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	items := make([]llmgen.PendingItem, len(received))
	copy(items, received)
	mu.Unlock()

	require.Len(t, items, 1)
	assert.Equal(t, "https://example.com/article/2", items[0].RawRef.URL)
	assert.Equal(t, "test-crawler", items[0].CrawlerName)
	assert.Equal(t, 1, items[0].LLMRetryCount)
	assert.Equal(t, storage.TargetTypePage, items[0].TargetType)
}

// TestGenerator_PendingQueue_TargetTypeList_Queued 는 TargetTypeList (카테고리 페이지) 에 대한
// pendingQueue 가 TargetTypePage 와 독립적으로 동작하는지 검증합니다 (이슈 #262 리뷰).
func TestGenerator_PendingQueue_TargetTypeList_Queued(t *testing.T) {
	provider := &slowProvider{
		fakeProvider: fakeProvider{
			name: "fake",
			response: `{
				"item_container": {"css": "li.news-item"},
				"item_link": {"css": "a", "attribute": "href"}
			}`,
		},
		delay: 150 * time.Millisecond,
	}
	repo := &recordingRepo{}
	g, _ := newGenerator(t, provider, repo)

	pq := newMemPendingQueue()
	var requeued []llmgen.PendingItem
	var mu sync.Mutex
	g.SetPendingQueue(pq, func(_ context.Context, items []llmgen.PendingItem) []llmgen.PendingItem {
		mu.Lock()
		requeued = append(requeued, items...)
		mu.Unlock()
		return nil
	})

	// URL 1: TargetTypeList 슬롯 획득 → LLM 시작
	g.Enqueue(context.Background(), "example.com", storage.TargetTypeList, &core.RawContent{
		URL: "https://example.com/news", HTML: sampleListHTML,
	}, 0, "", 0)

	// URL 2: 동일 host + TargetTypeList, in-flight 중 → pending 적재
	time.Sleep(20 * time.Millisecond)
	g.Enqueue(context.Background(), "example.com", storage.TargetTypeList, &core.RawContent{
		URL: "https://example.com/news/2", HTML: sampleListHTML,
	}, 0, "", 0)

	assert.Equal(t, 1, pq.count("example.com", storage.TargetTypeList), "URL 2는 TargetTypeList pending 에 적재되어야 함")
	// TargetTypePage pending 은 영향 없어야 함
	assert.Equal(t, 0, pq.count("example.com", storage.TargetTypePage), "TargetTypePage pending 은 독립")

	waitForInserts(t, repo, 1, 2*time.Second)
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	requeuedCount := len(requeued)
	mu.Unlock()

	assert.Equal(t, 1, requeuedCount, "룰 생성 완료 후 TargetTypeList pending URL이 requeueFn 으로 전달되어야 함")
	assert.Equal(t, 0, pq.count("example.com", storage.TargetTypeList), "flush 후 pending 비어야 함")
	if requeuedCount > 0 {
		mu.Lock()
		assert.Equal(t, storage.TargetTypeList, requeued[0].TargetType, "재투입 항목의 TargetType 은 TargetTypeList")
		mu.Unlock()
	}
}
