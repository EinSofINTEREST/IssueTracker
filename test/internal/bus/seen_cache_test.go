// seenCache 동작 검증 (이슈 #507).
//
// 이 캐시는 Redis 왕복을 생략하려고 판단을 기억하는데, 잘못 기억하면 **발행되어야 할 URL 이
// 영영 막힙니다.** 아래 테스트가 그 경계를 고정합니다.
//
// seenCache 는 비공개 타입이라 Publisher 의 공개 동작(PublishChained)으로 간접 검증합니다.
package bus_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/bus"
	"issuetracker/internal/processor/fetcher/core"
	"issuetracker/pkg/logger"
	"issuetracker/pkg/queue"
)

// countingMarker 는 Acquire 호출 횟수를 세는 IngestionMarker 입니다.
// acquired 가 false 면 "이미 파이프라인에 있음" 을 뜻합니다.
type countingMarker struct {
	mu       sync.Mutex
	calls    map[string]int
	acquired bool
}

func newCountingMarker(acquired bool) *countingMarker {
	return &countingMarker{calls: map[string]int{}, acquired: acquired}
}

func (m *countingMarker) Acquire(_ context.Context, url string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls[url]++
	return m.acquired, nil
}

func (m *countingMarker) Calls(url string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls[url]
}

// countingGuard 는 CheckAndAcquire 호출 횟수를 세는 PipelineGuard 입니다.
//
// Category 경로를 검증하려면 guard 가 필요합니다 — IngestionMarker 만 주입하면
// Category 는 marker 경로 자체를 우회하기 때문입니다 (chain.go 의 dispatch 참조).
type countingGuard struct {
	mu       sync.Mutex
	calls    map[string]int
	acquired bool
}

func newCountingGuard(acquired bool) *countingGuard {
	return &countingGuard{calls: map[string]int{}, acquired: acquired}
}

func (g *countingGuard) CheckAndAcquire(_ context.Context, url string, _ core.TargetType) (bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls[url]++
	return g.acquired, nil
}

func (g *countingGuard) Release(_ context.Context, _ string) error { return nil }

func (g *countingGuard) Calls(url string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.calls[url]
}

// recordingProducer 는 발행된 메시지를 모읍니다.
type recordingProducer struct {
	mu   sync.Mutex
	msgs []queue.Message
}

func (p *recordingProducer) Publish(_ context.Context, msg queue.Message) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.msgs = append(p.msgs, msg)
	return nil
}

func (p *recordingProducer) PublishBatch(_ context.Context, msgs []queue.Message) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.msgs = append(p.msgs, msgs...)
	return nil
}

func (p *recordingProducer) Close() error { return nil }

func (p *recordingProducer) Count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.msgs)
}

type staticPriority struct{}

func (staticPriority) Resolve(_ *core.CrawlJob) core.Priority { return core.PriorityNormal }

func newGuardPublisher(t *testing.T, guard *countingGuard, cacheTTL time.Duration) (*bus.Publisher, *recordingProducer) {
	t.Helper()
	prod := &recordingProducer{}
	p := bus.New(prod, staticPriority{}, logger.New(logger.Config{Level: "error"}))
	p.SetPipelineGuard(guard)
	p.SetSeenCache(100, cacheTTL)
	return p, prod
}

func newTestPublisher(t *testing.T, marker *countingMarker, cacheTTL time.Duration) (*bus.Publisher, *recordingProducer) {
	t.Helper()
	prod := &recordingProducer{}
	p := bus.New(prod, staticPriority{}, logger.New(logger.Config{Level: "error"}))
	p.SetIngestionMarker(marker)
	p.SetSeenCache(100, cacheTTL)
	return p, prod
}

// TestSeenCache_SkipsRedisOnRepeatedURL 은 동일 URL 반복 publish 시 Redis 호출이
// 1회로 줄어드는지 확인합니다 (이슈 #507 의 완료 조건).
func TestSeenCache_SkipsRedisOnRepeatedURL(t *testing.T) {
	marker := newCountingMarker(false) // 항상 "이미 파이프라인에 있음"
	p, prod := newTestPublisher(t, marker, 10*time.Minute)

	const url = "https://example.com/article/1"
	for i := 0; i < 5; i++ {
		require.NoError(t, p.PublishChained(context.Background(), "test",
			[]string{url}, core.TargetTypeArticle, time.Minute))
	}

	assert.Equal(t, 1, marker.Calls(url), "두 번째부터는 캐시가 Redis 왕복을 생략해야 한다")
	assert.Equal(t, 0, prod.Count(), "이미 파이프라인에 있으므로 발행은 0건")
}

// TestSeenCache_Disabled_AlwaysHitsRedis 는 TTL 0 이면 기존 동작이 유지되는지 확인합니다.
func TestSeenCache_Disabled_AlwaysHitsRedis(t *testing.T) {
	marker := newCountingMarker(false)
	p, _ := newTestPublisher(t, marker, 0) // 비활성

	const url = "https://example.com/article/2"
	for i := 0; i < 5; i++ {
		require.NoError(t, p.PublishChained(context.Background(), "test",
			[]string{url}, core.TargetTypeArticle, time.Minute))
	}

	assert.Equal(t, 5, marker.Calls(url), "캐시 비활성 시 매번 Redis 왕복")
}

// TestSeenCache_DoesNotCacheAcquiredURLs 는 **획득 성공을 캐시하지 않는지** 확인합니다.
//
// 성공을 기억하면 marker 가 만료된 뒤에도 "이미 발행함" 으로 오판해 재수집이 영영 막힌다.
func TestSeenCache_DoesNotCacheAcquiredURLs(t *testing.T) {
	marker := newCountingMarker(true) // 항상 획득 성공
	p, prod := newTestPublisher(t, marker, 10*time.Minute)

	const url = "https://example.com/article/3"
	for i := 0; i < 3; i++ {
		require.NoError(t, p.PublishChained(context.Background(), "test",
			[]string{url}, core.TargetTypeArticle, time.Minute))
	}

	assert.Equal(t, 3, marker.Calls(url), "획득 성공은 캐시하지 않으므로 매번 확인")
	assert.Equal(t, 3, prod.Count(), "매번 발행")
}

// TestSeenCache_CategoryNotCached 는 Category 가 캐시를 타지 않는지 확인합니다.
//
// Category marker TTL 은 60s 로 단명하고 정상 흐름은 cycle 종료 시 명시적 release 다.
// 캐시(기본 10m)가 marker 보다 오래 살면 10~30분 fetch 주기가 깨진다.
//
// guard 를 주입한다 — IngestionMarker 만으로는 Category 가 dedup 경로 자체를 우회해
// 캐시 적용 여부를 검증할 수 없다.
func TestSeenCache_CategoryNotCached(t *testing.T) {
	guard := newCountingGuard(false) // 항상 "이미 파이프라인에 있음"
	p, _ := newGuardPublisher(t, guard, 10*time.Minute)

	const url = "https://example.com/category/news"
	for i := 0; i < 4; i++ {
		require.NoError(t, p.PublishChained(context.Background(), "test",
			[]string{url}, core.TargetTypeCategory, time.Minute))
	}

	assert.Equal(t, 4, guard.Calls(url),
		"Category 는 캐시하지 않고 매번 guard 를 확인해야 한다")
}

// TestSeenCache_ArticleCachedOnGuardPath 는 같은 guard 경로에서 Article 은 캐시되는지
// 확인합니다 — Category 만 제외된다는 것이 요점입니다.
func TestSeenCache_ArticleCachedOnGuardPath(t *testing.T) {
	guard := newCountingGuard(false)
	p, _ := newGuardPublisher(t, guard, 10*time.Minute)

	const url = "https://example.com/article/9"
	for i := 0; i < 4; i++ {
		require.NoError(t, p.PublishChained(context.Background(), "test",
			[]string{url}, core.TargetTypeArticle, time.Minute))
	}

	assert.Equal(t, 1, guard.Calls(url),
		"Article 은 guard 경로에서도 캐시되어 왕복이 1회로 줄어야 한다")
}
