package worker_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/processor/fetcher/core"
	"issuetracker/internal/worker"
	"issuetracker/pkg/logger"
	"issuetracker/pkg/queue"
)

// upgradeFakeProducer — PublishUpgrade 전용 producer mock. PublishBatch 만 기록.
type upgradeFakeProducer struct {
	mu     sync.Mutex
	calls  int
	batch  []queue.Message
	failOn error
}

func (m *upgradeFakeProducer) Publish(_ context.Context, _ queue.Message) error { return nil }
func (m *upgradeFakeProducer) PublishBatch(_ context.Context, msgs []queue.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	if m.failOn != nil {
		err := m.failOn
		m.failOn = nil
		return err
	}
	m.batch = append([]queue.Message{}, msgs...)
	return nil
}
func (m *upgradeFakeProducer) Close() error { return nil }

func (m *upgradeFakeProducer) snapshot() (calls int, batch []queue.Message) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls, append([]queue.Message{}, m.batch...)
}

type upgradeFakeResolver struct{}

func (upgradeFakeResolver) Resolve(_ *core.CrawlJob) core.Priority { return core.PriorityNormal }

func newUpgradePublisher() (*worker.Publisher, *upgradeFakeProducer) {
	prod := &upgradeFakeProducer{}
	pub := worker.New(prod, upgradeFakeResolver{}, logger.New(logger.DefaultConfig()))
	return pub, prod
}

// TestPublishUpgrade_EmptyMsgs_Noop 는 빈 슬라이스 시 PublishBatch 호출 없음 + nil 반환.
func TestPublishUpgrade_EmptyMsgs_Noop(t *testing.T) {
	pub, prod := newUpgradePublisher()
	err := pub.PublishUpgrade(context.Background(), "host.com", nil)
	require.NoError(t, err)
	calls, _ := prod.snapshot()
	assert.Equal(t, 0, calls, "빈 msgs 는 PublishBatch 호출 안 함")
}

// TestPublishUpgrade_NonEmpty_CallsPublishBatch 는 메시지 전달 시 PublishBatch 1회 호출 검증.
func TestPublishUpgrade_NonEmpty_CallsPublishBatch(t *testing.T) {
	pub, prod := newUpgradePublisher()

	msgs := []queue.Message{
		{Topic: queue.TopicCrawlNormal, Key: []byte("id-1"), Value: []byte(`{}`)},
		{Topic: queue.TopicCrawlNormal, Key: []byte("id-2"), Value: []byte(`{}`)},
	}
	require.NoError(t, pub.PublishUpgrade(context.Background(), "host.com", msgs))

	calls, batch := prod.snapshot()
	assert.Equal(t, 1, calls, "PublishBatch 1회 호출")
	assert.Len(t, batch, 2, "메시지 2건 전달")
}

// TestPublishUpgrade_BatchFailure_WrapsError 는 PublishBatch 실패 시 host / count 포함된 에러 반환.
func TestPublishUpgrade_BatchFailure_WrapsError(t *testing.T) {
	pub, prod := newUpgradePublisher()
	prod.failOn = errors.New("kafka unavailable")

	msgs := []queue.Message{{Topic: queue.TopicCrawlNormal, Key: []byte("id-1"), Value: []byte(`{}`)}}
	err := pub.PublishUpgrade(context.Background(), "host.com", msgs)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "host.com")
	assert.Contains(t, err.Error(), "count=1")
}
