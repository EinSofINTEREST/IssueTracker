package bus_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/bus"
	"issuetracker/internal/processor/fetcher/core"
	"issuetracker/pkg/links"
	"issuetracker/pkg/queue"
)

// fakeIngestionLock — 메모리 SETNX 시뮬레이션 (publisher 테스트 전용 — worker 패키지의
// 동일 인터페이스를 구조적 타이핑으로 만족).
type fakeIngestionLock struct {
	mu       sync.Mutex
	keys     map[string]struct{}
	failOnce error
}

func newFakeLock() *fakeIngestionLock {
	return &fakeIngestionLock{keys: make(map[string]struct{})}
}

func (f *fakeIngestionLock) Acquire(_ context.Context, url string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failOnce != nil {
		err := f.failOnce
		f.failOnce = nil
		return false, err
	}
	if _, ok := f.keys[url]; ok {
		return false, nil
	}
	f.keys[url] = struct{}{}
	return true, nil
}

// PublishBatch payload helpers reused from gate test pattern.
type lockMockProducer struct {
	mu       sync.Mutex
	messages []queue.Message
}

func (m *lockMockProducer) Publish(_ context.Context, _ queue.Message) error { return nil }
func (m *lockMockProducer) PublishBatch(_ context.Context, msgs []queue.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.messages = append(m.messages, msgs...)
	return nil
}
func (m *lockMockProducer) Close() error { return nil }
func (m *lockMockProducer) urls() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.messages))
	for _, msg := range m.messages {
		job, err := core.UnmarshalCrawlJob(msg.Value)
		if err != nil {
			continue
		}
		out = append(out, job.Target.URL)
	}
	return out
}

// ─────────────────────────────────────────────────────────────────────────────

// 첫 publish 는 모든 URL 통과, 두 번째 동일 URL 은 lock 차단.
func TestPublisher_IngestionLock_BlocksDuplicateOnSecondPublish(t *testing.T) {
	prod := &lockMockProducer{}
	pub := bus.New(prod, noopResolver{}, gateLog())
	// SetIngestionLock 은 1회만 호출 — 같은 lock 인스턴스가 두 Publish 호출 모두에 적용됨.
	pub.SetIngestionLock(newFakeLock())

	urls := []string{"https://example.com/a", "https://example.com/b"}
	require.NoError(t, pub.PublishChained(context.Background(), "test", urls, core.TargetTypeArticle, time.Second))
	require.NoError(t, pub.PublishChained(context.Background(), "test", urls, core.TargetTypeArticle, time.Second))

	// 첫 호출에서만 2개 publish, 두 번째 호출은 lock 차단으로 0개
	assert.ElementsMatch(t, urls, prod.urls(), "두 번째 publish 는 lock 으로 모두 차단")
}

// TargetTypeCategory 는 Ingestion Lock 적용 안 됨 (매 주기 새 기사 추출 필요).
func TestPublisher_IngestionLock_SkippedForCategory(t *testing.T) {
	prod := &lockMockProducer{}
	pub := bus.New(prod, noopResolver{}, gateLog())
	pub.SetIngestionLock(newFakeLock())

	urls := []string{"https://example.com/category/news"}
	// 두 번 publish 해도 카테고리는 둘 다 통과
	require.NoError(t, pub.PublishChained(context.Background(), "test", urls, core.TargetTypeCategory, time.Second))
	require.NoError(t, pub.PublishChained(context.Background(), "test", urls, core.TargetTypeCategory, time.Second))

	assert.Len(t, prod.urls(), 2, "Category 는 lock 미적용")
}

// Lock 조회 실패 시 fail-open — 해당 URL 통과 + 다른 URL 도 통과.
func TestPublisher_IngestionLock_FailOpenOnError(t *testing.T) {
	prod := &lockMockProducer{}
	pub := bus.New(prod, noopResolver{}, gateLog())

	lock := newFakeLock()
	lock.failOnce = errors.New("redis timeout")
	pub.SetIngestionLock(lock)

	urls := []string{"https://example.com/x", "https://example.com/y"}
	require.NoError(t, pub.PublishChained(context.Background(), "test", urls, core.TargetTypeArticle, time.Second))

	// 첫 URL 은 lock 에러 → fail-open 으로 통과, 두 번째는 정상 acquire 로 통과
	assert.Len(t, prod.urls(), 2, "lock 에러 시 fail-open — 모든 URL 통과")
}

// SetIngestionLock(nil) 후 publish 는 dedup 미적용 — 동일 URL 두 번 모두 통과.
func TestPublisher_IngestionLock_NilDisablesDedup(t *testing.T) {
	prod := &lockMockProducer{}
	pub := bus.New(prod, noopResolver{}, gateLog())
	pub.SetIngestionLock(newFakeLock())
	pub.SetIngestionLock(nil) // 비활성화

	urls := []string{"https://example.com/x"}
	require.NoError(t, pub.PublishChained(context.Background(), "test", urls, core.TargetTypeArticle, time.Second))
	require.NoError(t, pub.PublishChained(context.Background(), "test", urls, core.TargetTypeArticle, time.Second))

	assert.Len(t, prod.urls(), 2, "lock nil 이면 dedup 비활성")
}

// Normalizer 는 publish 직전 정규화 — fragment / 쿼리 stripping 후 lock 키 일관 동작.
func TestPublisher_Normalizer_AppliedBeforeLock(t *testing.T) {
	prod := &lockMockProducer{}
	pub := bus.New(prod, noopResolver{}, gateLog())
	pub.SetNormalizer(links.NewNormalizer())
	pub.SetIngestionLock(newFakeLock())

	// 두 URL 은 정규화 후 동일 (fragment 제거 + utm_* 쿼리 제거)
	urls := []string{
		"https://example.com/x#section1",
		"https://example.com/x?utm_source=email",
	}
	require.NoError(t, pub.PublishChained(context.Background(), "test", urls, core.TargetTypeArticle, time.Second))

	// 정규화 후 둘 다 "https://example.com/x" — Ingestion Lock 이 두 번째를 차단
	assert.Len(t, prod.urls(), 1, "정규화 후 동일 URL — lock 이 두 번째 차단")
	assert.Equal(t, "https://example.com/x", prod.urls()[0])
}

// 정규화 결과 모두 빈 문자열이면 PublishBatch 호출 없이 early return (PR #179 CodeRabbit 피드백).
func TestPublisher_Normalizer_EarlyReturnOnAllEmpty(t *testing.T) {
	prod := &lockMockProducer{}
	pub := bus.New(prod, noopResolver{}, gateLog())
	pub.SetNormalizer(links.NewNormalizer())

	// 빈 문자열 / 상대 URL — 정규화 후 모두 빈 슬라이스 또는 host 없음
	urls := []string{"", ""}
	require.NoError(t, pub.PublishChained(context.Background(), "test", urls, core.TargetTypeArticle, time.Second))

	assert.Empty(t, prod.urls(), "정규화 후 빈 슬라이스면 PublishBatch 호출 0회 (early return)")
}

// Normalizer 미설정 시 URL 원본 그대로 publish.
func TestPublisher_Normalizer_DisabledByDefault(t *testing.T) {
	prod := &lockMockProducer{}
	pub := bus.New(prod, noopResolver{}, gateLog())
	// Normalizer 미설정

	urls := []string{"https://EXAMPLE.com/X#frag"}
	require.NoError(t, pub.PublishChained(context.Background(), "test", urls, core.TargetTypeArticle, time.Second))

	assert.Equal(t, urls, prod.urls(), "Normalizer 미설정 시 원본 그대로")
}
