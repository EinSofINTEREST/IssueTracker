package llmgen_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"issuetracker/internal/processor/fetcher/core"
	"issuetracker/internal/processor/parser/rule/llmgen"
	"issuetracker/internal/storage"
)

// ─────────────────────────────────────────────────────────────────────────────
// InflightLocker 인터페이스 계약 검증
// ─────────────────────────────────────────────────────────────────────────────

// TestGenerator_SetLocker_Nil_FallsBackToMem 는 SetLocker(nil) 시 in-process 로 fallback 하는지 검증합니다.
func TestGenerator_SetLocker_Nil_FallsBackToMem(t *testing.T) {
	provider := &fakeProvider{
		name: "fake",
		response: `{
			"title": {"css": "h1.article-title"},
			"main_content": {"css": "article p"}
		}`,
	}
	repo := &recordingRepo{}
	g, _ := newGenerator(t, provider, repo)

	g.SetLocker(nil)

	g.Enqueue(context.Background(), "example.com", storage.TargetTypePage, &core.RawContent{
		URL: "https://example.com/article/1", HTML: samplePageHTML,
	}, 0, "")

	waitForInserts(t, repo, 1, 2*time.Second)
	assert.Equal(t, 1, provider.callCount(), "nil SetLocker 시 기존 동작 유지")
}

// TestGenerator_SetLocker_AlwaysLocked_NoLLMCall 는 locker 가 항상 false 를 반환하면
// LLM 이 호출되지 않는지 검증합니다 (다른 인스턴스가 처리 중인 분산 환경 시뮬레이션).
func TestGenerator_SetLocker_AlwaysLocked_NoLLMCall(t *testing.T) {
	provider := &fakeProvider{name: "fake", response: "{}"}
	repo := &recordingRepo{}
	g, _ := newGenerator(t, provider, repo)

	g.SetLocker(&alwaysLockedLocker{})

	g.Enqueue(context.Background(), "example.com", storage.TargetTypePage, &core.RawContent{
		URL: "https://example.com/x", HTML: samplePageHTML,
	}, 0, "")

	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, 0, provider.callCount(), "locker 가 false 반환 시 LLM 호출 없어야 함")
}

// TestGenerator_SetLocker_CustomLocker_IsInvoked 는 SetLocker 로 주입한 locker 의
// TryAcquire 가 실제로 호출되는지 검증합니다.
func TestGenerator_SetLocker_CustomLocker_IsInvoked(t *testing.T) {
	provider := &fakeProvider{
		name: "fake",
		response: `{
			"title": {"css": "h1.article-title"},
			"main_content": {"css": "article p"}
		}`,
	}
	repo := &recordingRepo{}
	g, _ := newGenerator(t, provider, repo)

	locker := &countingLocker{}
	g.SetLocker(locker)

	g.Enqueue(context.Background(), "example.com", storage.TargetTypePage, &core.RawContent{
		URL: "https://example.com/x", HTML: samplePageHTML,
	}, 0, "")

	waitForInserts(t, repo, 1, 2*time.Second)
	assert.Equal(t, 1, locker.acquireCalls(), "TryAcquire 가 1회 호출되어야 함")
	assert.Equal(t, 1, locker.releaseCalls(), "Release 가 1회 호출되어야 함")
}

// ─────────────────────────────────────────────────────────────────────────────
// test fakes
// ─────────────────────────────────────────────────────────────────────────────

// alwaysLockedLocker 는 항상 acquired=false 를 반환합니다.
type alwaysLockedLocker struct{}

func (a *alwaysLockedLocker) TryAcquire(_ context.Context, _ string, _ storage.TargetType) (bool, error) {
	return false, nil
}
func (a *alwaysLockedLocker) Release(_ context.Context, _ string, _ storage.TargetType) error {
	return nil
}

// countingLocker 는 TryAcquire/Release 호출 횟수를 기록하며 실제 dedup 도 수행합니다.
type countingLocker struct {
	mu      sync.Mutex
	acquire int
	release int
	held    map[string]struct{}
}

func (c *countingLocker) TryAcquire(_ context.Context, host string, targetType storage.TargetType) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.acquire++
	if c.held == nil {
		c.held = make(map[string]struct{})
	}
	key := host + ":" + string(targetType)
	if _, ok := c.held[key]; ok {
		return false, nil
	}
	c.held[key] = struct{}{}
	return true, nil
}

func (c *countingLocker) Release(_ context.Context, host string, targetType storage.TargetType) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.release++
	delete(c.held, host+":"+string(targetType))
	return nil
}

func (c *countingLocker) acquireCalls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.acquire
}

func (c *countingLocker) releaseCalls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.release
}

// Ensure test fakes implement llmgen.InflightLocker
var _ llmgen.InflightLocker = (*alwaysLockedLocker)(nil)
var _ llmgen.InflightLocker = (*countingLocker)(nil)
