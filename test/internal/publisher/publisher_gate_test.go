package publisher_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/crawler/core"
	"issuetracker/internal/publisher"
	"issuetracker/pkg/logger"
	"issuetracker/pkg/queue"
	"issuetracker/pkg/urlguard"
)

// gateMockProducer 는 PublishBatch 호출 인자를 기록합니다.
type gateMockProducer struct {
	mu     sync.Mutex
	calls  [][]queue.Message
	retErr error
}

func (m *gateMockProducer) Publish(_ context.Context, _ queue.Message) error {
	return nil
}

func (m *gateMockProducer) PublishBatch(_ context.Context, msgs []queue.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cpy := make([]queue.Message, len(msgs))
	copy(cpy, msgs)
	m.calls = append(m.calls, cpy)
	return m.retErr
}

func (m *gateMockProducer) Close() error { return nil }

func (m *gateMockProducer) batchSize() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.calls) == 0 {
		return 0
	}
	return len(m.calls[0])
}

// noopResolver 는 Priority Resolver 더블입니다 (항상 Normal 반환).
type noopResolver struct{}

func (noopResolver) Resolve(_ *core.CrawlJob) core.Priority { return core.PriorityNormal }

func gateLog() *logger.Logger { return logger.New(logger.DefaultConfig()) }

// TestPublisher_Gate_FiltersBlockedURLs:
// urls 슬라이스에서 차단 대상이 사전 필터링되고 통과분만 batch publish.
func TestPublisher_Gate_FiltersBlockedURLs(t *testing.T) {
	prod := &gateMockProducer{}
	pub := publisher.New(prod, noopResolver{}, gateLog())
	pub.SetGate(urlguard.NewGate(urlguard.Default(), gateLog()))

	urls := []string{
		"https://edition.cnn.com/article/1",      // pass
		"https://rss.cnn.com/rss/cnn_health.rss", // block
		"https://news.naver.com/main/read.naver", // pass
		"mailto:foo@example.com",                 // block
	}
	err := pub.Publish(context.Background(), "cnn", urls, core.TargetTypeArticle, 5*time.Second)
	require.NoError(t, err)

	assert.Equal(t, 2, prod.batchSize(), "통과 2건만 batch 에 포함")
}

// TestPublisher_Gate_AllBlocked_NoPublish:
// 모두 차단되면 PublishBatch 호출 없이 nil 반환.
func TestPublisher_Gate_AllBlocked_NoPublish(t *testing.T) {
	prod := &gateMockProducer{}
	pub := publisher.New(prod, noopResolver{}, gateLog())
	pub.SetGate(urlguard.NewGate(urlguard.Default(), gateLog()))

	err := pub.Publish(context.Background(), "cnn", []string{
		"https://rss.cnn.com/rss/cnn_health.rss",
		"mailto:x@y.z",
	}, core.TargetTypeArticle, 5*time.Second)
	require.NoError(t, err)

	prod.mu.Lock()
	defer prod.mu.Unlock()
	assert.Empty(t, prod.calls, "전부 차단 시 PublishBatch 미호출")
}

// TestPublisher_NoGate_LegacyBehavior:
// SetGate 미호출 시 모든 URL publish (기존 동작 유지).
func TestPublisher_NoGate_LegacyBehavior(t *testing.T) {
	prod := &gateMockProducer{}
	pub := publisher.New(prod, noopResolver{}, gateLog())
	// SetGate 호출 없음

	urls := []string{
		"https://rss.cnn.com/rss/cnn_health.rss",
		"https://edition.cnn.com/article/1",
	}
	require.NoError(t, pub.Publish(context.Background(), "cnn", urls, core.TargetTypeArticle, 5*time.Second))

	assert.Equal(t, 2, prod.batchSize(), "가드 미설정 — 모든 URL publish")
}

// TestPublisher_Gate_AllowAllGuard_NoFiltering:
// AllowAllGuard 명시 사용 시 차단 없이 전체 publish.
func TestPublisher_Gate_AllowAllGuard_NoFiltering(t *testing.T) {
	prod := &gateMockProducer{}
	pub := publisher.New(prod, noopResolver{}, gateLog())
	pub.SetGate(urlguard.NewGate(urlguard.AllowAllGuard{}, gateLog()))

	urls := []string{
		"https://rss.cnn.com/rss/cnn_health.rss",
		"mailto:foo@example.com",
	}
	require.NoError(t, pub.Publish(context.Background(), "cnn", urls, core.TargetTypeArticle, 5*time.Second))
	assert.Equal(t, 2, prod.batchSize())
}
