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
	"issuetracker/pkg/logger"
	"issuetracker/pkg/queue"
)

// seedFakeProducer — PublishSeed 전용 producer mock. 단건 Publish 만 기록.
type seedFakeProducer struct {
	mu       sync.Mutex
	messages []queue.Message
	failOnce error
}

func (m *seedFakeProducer) Publish(_ context.Context, msg queue.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failOnce != nil {
		err := m.failOnce
		m.failOnce = nil
		return err
	}
	m.messages = append(m.messages, msg)
	return nil
}

func (m *seedFakeProducer) PublishBatch(_ context.Context, _ []queue.Message) error { return nil }
func (m *seedFakeProducer) Close() error                                            { return nil }

func (m *seedFakeProducer) snapshot() []queue.Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]queue.Message, len(m.messages))
	copy(out, m.messages)
	return out
}

// seedFakeResolver — 항상 같은 priority 반환 (시드 발행에선 Resolve 호출 안 함 — entry.Priority 사용).
type seedFakeResolver struct{}

func (seedFakeResolver) Resolve(_ *core.CrawlJob) core.Priority { return core.PriorityNormal }

// seedFakeGuard — CheckAndAcquire / Release 동작을 제어하는 PipelineGuard mock.
type seedFakeGuard struct {
	mu              sync.Mutex
	acquired        map[string]struct{}
	acquireErr      error
	denyURL         string
	releaseErr      error
	releasedURL     string
	releasedCounter int
}

func newSeedFakeGuard() *seedFakeGuard {
	return &seedFakeGuard{acquired: make(map[string]struct{})}
}

func (g *seedFakeGuard) CheckAndAcquire(_ context.Context, url string, _ core.TargetType) (bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.acquireErr != nil {
		err := g.acquireErr
		g.acquireErr = nil
		return false, err
	}
	if g.denyURL != "" && url == g.denyURL {
		return false, nil
	}
	g.acquired[url] = struct{}{}
	return true, nil
}

func (g *seedFakeGuard) Release(_ context.Context, url string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.releasedURL = url
	g.releasedCounter++
	delete(g.acquired, url)
	return g.releaseErr
}

func (g *seedFakeGuard) isAcquired(url string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	_, ok := g.acquired[url]
	return ok
}

func (g *seedFakeGuard) releaseCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.releasedCounter
}

func seedTestJob(url string) *core.CrawlJob {
	return &core.CrawlJob{
		ID:          "seed-test-1",
		CrawlerName: "test",
		Target: core.Target{
			URL:  url,
			Type: core.TargetTypeArticle,
		},
		Priority:    core.PriorityNormal,
		ScheduledAt: time.Now(),
		Timeout:     5 * time.Second,
		MaxRetries:  3,
	}
}

func newSeedPublisher() (*bus.Publisher, *seedFakeProducer) {
	prod := &seedFakeProducer{}
	pub := bus.New(prod, seedFakeResolver{}, logger.New(logger.DefaultConfig()))
	return pub, prod
}

// TestPublishSeed_NilJob 는 exported method 의 nil 방어 검증 (이슈 #387 — gemini 피드백).
func TestPublishSeed_NilJob(t *testing.T) {
	pub, _ := newSeedPublisher()
	err := pub.PublishSeed(context.Background(), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil job")
}

// TestPublishSeed_NoGuard_Success 는 guard 미주입 시 정상 발행을 검증.
func TestPublishSeed_NoGuard_Success(t *testing.T) {
	pub, prod := newSeedPublisher()
	job := seedTestJob("https://example.com/article")

	require.NoError(t, pub.PublishSeed(context.Background(), job))

	msgs := prod.snapshot()
	require.Len(t, msgs, 1)
	assert.Equal(t, []byte(job.ID), msgs[0].Key)
}

// TestPublishSeed_GuardDeny_ReturnsErrPublishSkipped 는 PipelineGuard 가 false 반환 시
// ErrPublishSkipped 가 반환되고 producer.Publish 가 호출되지 않음을 검증 (Copilot PR #395).
func TestPublishSeed_GuardDeny_ReturnsErrPublishSkipped(t *testing.T) {
	pub, prod := newSeedPublisher()
	guard := newSeedFakeGuard()
	guard.denyURL = "https://example.com/dup"
	pub.SetPipelineGuard(guard)

	job := seedTestJob("https://example.com/dup")
	err := pub.PublishSeed(context.Background(), job)

	require.Error(t, err)
	assert.ErrorIs(t, err, bus.ErrPublishSkipped)
	assert.Empty(t, prod.snapshot(), "guard 가 deny 했으므로 publish 호출 X")
	assert.Equal(t, 0, guard.releaseCount(), "deny 케이스는 release 호출 안 함 (acquire 안 됨)")
}

// TestPublishSeed_GuardError_FailOpen 는 PipelineGuard 가 에러 반환 시 fail-open 으로
// publish 가 진행됨을 검증 (Copilot PR #395).
func TestPublishSeed_GuardError_FailOpen(t *testing.T) {
	pub, prod := newSeedPublisher()
	guard := newSeedFakeGuard()
	guard.acquireErr = errors.New("redis down")
	pub.SetPipelineGuard(guard)

	job := seedTestJob("https://example.com/article")
	require.NoError(t, pub.PublishSeed(context.Background(), job),
		"guard 에러는 fail-open — publish 진행")
	assert.Len(t, prod.snapshot(), 1, "guard 에러 시에도 publish 호출")
}

// TestPublishSeed_PublishFailure_ReleasesGuard 는 producer.Publish 실패 시 marker 가
// 즉시 release 되는지 검증 (Copilot PR #395 — 핵심 케이스).
func TestPublishSeed_PublishFailure_ReleasesGuard(t *testing.T) {
	pub, prod := newSeedPublisher()
	prod.failOnce = errors.New("kafka unavailable")

	guard := newSeedFakeGuard()
	pub.SetPipelineGuard(guard)

	job := seedTestJob("https://example.com/article")
	err := pub.PublishSeed(context.Background(), job)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "publish seed job")
	assert.Equal(t, 1, guard.releaseCount(), "publish 실패 시 marker 1회 release")
	assert.Equal(t, job.Target.URL, guard.releasedURL)
	assert.False(t, guard.isAcquired(job.Target.URL), "release 후 acquire 상태 해제")
}
