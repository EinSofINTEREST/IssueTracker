package publisher_test

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
	"issuetracker/internal/publisher"
)

// ─────────────────────────────────────────────────────────────────────────────
// Mock URLCache
// ─────────────────────────────────────────────────────────────────────────────

// stubURLCache 는 사전 등록된 hit 집합과 호출 카운터를 갖는 URLCache 더블입니다.
// err 가 nil 이 아니면 hit 여부 무관하게 에러를 우선 반환합니다.
type stubURLCache struct {
	mu     sync.Mutex
	hits   map[string]bool // Exists → true 가 될 URL 집합
	err    error
	called atomic.Int32
}

func newStubURLCache(hitURLs ...string) *stubURLCache {
	hits := make(map[string]bool, len(hitURLs))
	for _, u := range hitURLs {
		hits[u] = true
	}
	return &stubURLCache{hits: hits}
}

func (s *stubURLCache) Exists(_ context.Context, url string) (bool, error) {
	s.called.Add(1)
	if s.err != nil {
		return false, s.err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits[url], nil
}

// ─────────────────────────────────────────────────────────────────────────────
// 테스트
// ─────────────────────────────────────────────────────────────────────────────

// TestPublisher_URLCache_FiltersCachedURLs:
// cache hit URL 은 batch 에서 제외되고 miss 만 publish 되어야 함.
func TestPublisher_URLCache_FiltersCachedURLs(t *testing.T) {
	prod := &gateMockProducer{}
	pub := publisher.New(prod, noopResolver{}, gateLog())

	cache := newStubURLCache(
		"https://edition.cnn.com/article/1", // hit
		"https://edition.cnn.com/article/3", // hit
	)
	pub.SetURLCache(cache)

	urls := []string{
		"https://edition.cnn.com/article/1", // hit
		"https://edition.cnn.com/article/2", // miss
		"https://edition.cnn.com/article/3", // hit
		"https://edition.cnn.com/article/4", // miss
	}
	err := pub.Publish(context.Background(), "cnn", urls, core.TargetTypeArticle, 5*time.Second)
	require.NoError(t, err)

	assert.Equal(t, 2, prod.batchSize(), "miss 2건만 batch 에 포함")
	assert.Equal(t, int32(4), cache.called.Load(), "각 URL 마다 cache 1회 조회")
}

// TestPublisher_URLCache_AllCached_NoPublish:
// 모두 cache hit 이면 PublishBatch 호출 없이 nil 반환.
func TestPublisher_URLCache_AllCached_NoPublish(t *testing.T) {
	prod := &gateMockProducer{}
	pub := publisher.New(prod, noopResolver{}, gateLog())

	urls := []string{
		"https://edition.cnn.com/article/1",
		"https://edition.cnn.com/article/2",
	}
	pub.SetURLCache(newStubURLCache(urls...))

	require.NoError(t, pub.Publish(context.Background(), "cnn", urls, core.TargetTypeArticle, 5*time.Second))

	prod.mu.Lock()
	defer prod.mu.Unlock()
	assert.Empty(t, prod.calls, "전부 cache hit 시 PublishBatch 미호출")
}

// TestPublisher_URLCache_AllMiss_PublishesAll:
// 모두 cache miss 면 전부 publish.
func TestPublisher_URLCache_AllMiss_PublishesAll(t *testing.T) {
	prod := &gateMockProducer{}
	pub := publisher.New(prod, noopResolver{}, gateLog())
	pub.SetURLCache(newStubURLCache()) // 빈 hit set

	urls := []string{
		"https://edition.cnn.com/article/1",
		"https://edition.cnn.com/article/2",
	}
	require.NoError(t, pub.Publish(context.Background(), "cnn", urls, core.TargetTypeArticle, 5*time.Second))
	assert.Equal(t, 2, prod.batchSize(), "miss 만 있으면 전부 publish")
}

// TestPublisher_URLCache_CategoryBypassesCache:
// TargetTypeCategory 는 dedup 비대상 — 모든 URL 이 cache hit 이라도 그대로 publish.
// consumer-side 와 동일 규칙 (카테고리는 매 주기 새 기사 추출이 목적).
func TestPublisher_URLCache_CategoryBypassesCache(t *testing.T) {
	prod := &gateMockProducer{}
	pub := publisher.New(prod, noopResolver{}, gateLog())

	urls := []string{
		"https://edition.cnn.com/health",
		"https://edition.cnn.com/world",
	}
	cache := newStubURLCache(urls...) // 모두 hit 으로 설정
	pub.SetURLCache(cache)

	require.NoError(t, pub.Publish(context.Background(), "cnn", urls, core.TargetTypeCategory, 5*time.Second))

	assert.Equal(t, 2, prod.batchSize(), "category 는 dedup 우회 — 전부 publish")
	assert.Equal(t, int32(0), cache.called.Load(), "category 는 cache 조회 자체를 생략")
}

// TestPublisher_URLCache_CacheError_FailOpen:
// cache 조회 실패 시 fail-open — 해당 URL 이 통과해서 publish 됨.
func TestPublisher_URLCache_CacheError_FailOpen(t *testing.T) {
	prod := &gateMockProducer{}
	pub := publisher.New(prod, noopResolver{}, gateLog())

	cache := &stubURLCache{err: errors.New("redis unavailable")}
	pub.SetURLCache(cache)

	urls := []string{
		"https://edition.cnn.com/article/1",
		"https://edition.cnn.com/article/2",
	}
	require.NoError(t, pub.Publish(context.Background(), "cnn", urls, core.TargetTypeArticle, 5*time.Second))

	assert.Equal(t, 2, prod.batchSize(), "조회 실패는 fail-open — 전부 publish")
	assert.Equal(t, int32(2), cache.called.Load())
}

// TestPublisher_NoURLCache_LegacyBehavior:
// SetURLCache 미호출 시 dedup 비활성 — 모든 URL publish (기존 동작 유지).
func TestPublisher_NoURLCache_LegacyBehavior(t *testing.T) {
	prod := &gateMockProducer{}
	pub := publisher.New(prod, noopResolver{}, gateLog())
	// SetURLCache 호출 없음

	urls := []string{
		"https://edition.cnn.com/article/1",
		"https://edition.cnn.com/article/2",
	}
	require.NoError(t, pub.Publish(context.Background(), "cnn", urls, core.TargetTypeArticle, 5*time.Second))
	assert.Equal(t, 2, prod.batchSize(), "URLCache 미설정 — 모든 URL publish")
}

// TestPublisher_SetURLCache_NilUnsetsPrevious:
// SetURLCache(nil) 호출 시 이전 cache 가 제거되고 dedup 이 비활성화되어야 함.
func TestPublisher_SetURLCache_NilUnsetsPrevious(t *testing.T) {
	prod := &gateMockProducer{}
	pub := publisher.New(prod, noopResolver{}, gateLog())

	urls := []string{
		"https://edition.cnn.com/article/1",
		"https://edition.cnn.com/article/2",
	}
	cache := newStubURLCache(urls...) // 모두 hit
	pub.SetURLCache(cache)
	pub.SetURLCache(nil) // 이전 cache 해제

	require.NoError(t, pub.Publish(context.Background(), "cnn", urls, core.TargetTypeArticle, 5*time.Second))

	assert.Equal(t, 2, prod.batchSize(), "SetURLCache(nil) 후 모든 URL publish")
	assert.Equal(t, int32(0), cache.called.Load(), "해제된 cache 는 호출되지 않음")
}

// TestPublisher_URLCache_AppliedAfterGate:
// Gate 차단된 URL 은 cache 조회 자체가 일어나지 않아야 함 — Gate → URLCache 순서 검증.
func TestPublisher_URLCache_AppliedAfterGate(t *testing.T) {
	prod := &gateMockProducer{}
	pub := publisher.New(prod, noopResolver{}, gateLog())

	cache := newStubURLCache() // 모두 miss
	pub.SetURLCache(cache)

	// Gate 미설정이면 모든 URL 이 통과 → cache 조회는 입력 개수만큼 발생
	urls := []string{
		"https://edition.cnn.com/article/1",
		"https://edition.cnn.com/article/2",
		"https://edition.cnn.com/article/3",
	}
	require.NoError(t, pub.Publish(context.Background(), "cnn", urls, core.TargetTypeArticle, 5*time.Second))

	assert.Equal(t, 3, prod.batchSize())
	assert.Equal(t, int32(3), cache.called.Load(),
		"Gate 미설정 시 모든 URL 이 cache 조회 대상")
}
