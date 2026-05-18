package scheduler_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/scheduler"
	"issuetracker/pkg/logger"
	"issuetracker/pkg/queue"
)

// ─────────────────────────────────────────────────────────────────────────────
// Mocks (BufferDrainer)
// ─────────────────────────────────────────────────────────────────────────────

type mockJobBuffer struct {
	mu       sync.Mutex
	store    map[string][][]byte
	enqErr   error
	drainErr error
	lenErr   error
}

func newMockJobBuffer() *mockJobBuffer {
	return &mockJobBuffer{store: make(map[string][][]byte)}
}

func (b *mockJobBuffer) EnqueueJob(_ context.Context, label string, payload []byte, _ int64) error {
	if b.enqErr != nil {
		return b.enqErr
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.store[label] = append(b.store[label], payload)
	return nil
}

func (b *mockJobBuffer) DrainJobs(_ context.Context, label string, n int) ([][]byte, error) {
	if b.drainErr != nil {
		return nil, b.drainErr
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	items := b.store[label]
	if n <= 0 || len(items) == 0 {
		return nil, nil
	}
	if n > len(items) {
		n = len(items)
	}
	out := append([][]byte(nil), items[:n]...)
	b.store[label] = items[n:]
	return out, nil
}

func (b *mockJobBuffer) JobBufferLen(_ context.Context, label string) (int64, error) {
	if b.lenErr != nil {
		return 0, b.lenErr
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return int64(len(b.store[label])), nil
}

func (b *mockJobBuffer) seed(label string, payloads ...[]byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.store[label] = append(b.store[label], payloads...)
}

type mockDrainerProducer struct {
	mu        sync.Mutex
	published []queue.Message
	failErr   error
}

func (p *mockDrainerProducer) Publish(_ context.Context, msg queue.Message) error {
	if p.failErr != nil {
		return p.failErr
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.published = append(p.published, msg)
	return nil
}

func (p *mockDrainerProducer) PublishBatch(_ context.Context, msgs []queue.Message) error {
	if p.failErr != nil {
		return p.failErr
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.published = append(p.published, msgs...)
	return nil
}

func (p *mockDrainerProducer) Close() error { return nil }

func (p *mockDrainerProducer) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.published)
}

type fixedBacklogChecker struct {
	lag atomic.Int64
	err error
}

func (c *fixedBacklogChecker) Backlog(_ context.Context, _ string, _ string) (int64, error) {
	if c.err != nil {
		return 0, c.err
	}
	return c.lag.Load(), nil
}

func newDrainerTestLog() *logger.Logger {
	return logger.New(logger.DefaultConfig())
}

// encodeMsg 는 BufferDrainer 가 decode 가능한 직렬화 payload 를 만들어 줍니다.
// 본 헬퍼는 BufferingProducer 의 encodeBufferedMessage 와 동일 포맷이어야 합니다.
// queue 패키지의 unexported encodeBufferedMessage 직접 호출이 불가능하므로,
// BufferingProducer.Publish 를 통해 mock buffer 에 적재 → seed payload 로 사용합니다.
func encodeMsg(t *testing.T, msg queue.Message) []byte {
	t.Helper()
	relay := &mockJobBuffer{store: make(map[string][][]byte)}
	bp := queue.NewBufferingProducer(&mockDrainerProducer{}, relay, 0, newDrainerTestLog())
	require.NoError(t, bp.Publish(context.Background(), msg))
	label := "normal"
	if msg.Topic == queue.TopicCrawlLow {
		label = "low"
	}
	out, err := relay.DrainJobs(context.Background(), label, 1)
	require.NoError(t, err)
	require.Len(t, out, 1)
	return out[0]
}

// ─────────────────────────────────────────────────────────────────────────────
// Tests
// ─────────────────────────────────────────────────────────────────────────────

// TestBufferDrainer_DrainsUpToAvailable 은 backlog < target 시 buffer 에서 정확한 수만큼 drain 후 publish 검증.
func TestBufferDrainer_DrainsUpToAvailable(t *testing.T) {
	buf := newMockJobBuffer()
	for i := 0; i < 5; i++ {
		payload := encodeMsg(t, queue.Message{Topic: queue.TopicCrawlNormal, Value: []byte("v")})
		buf.seed("normal", payload)
	}

	prod := &mockDrainerProducer{}
	check := &fixedBacklogChecker{}
	check.lag.Store(100) // 현재 lag

	d, err := scheduler.NewBufferDrainer(buf, prod, check, scheduler.BufferDrainerConfig{
		Interval:      time.Hour, // tick 발화 회피 — drainAll 한 번만 검증
		TargetBacklog: 200,       // available = 100
		DrainBatch:    50,        // → min(100, 50, 5) = 5
		GroupID:       queue.GroupCrawlerWorkers,
	}, newDrainerTestLog())
	require.NoError(t, err)

	// drainAll 은 unexported — Start + ctx cancel 로 간접 호출.
	ctx, cancel := context.WithCancel(context.Background())
	d.Start(ctx)

	// drainAll 이 부팅 직후 1회 호출 — 약간 대기 후 cancel.
	time.Sleep(80 * time.Millisecond)
	cancel()
	d.Stop()

	assert.Equal(t, 5, prod.count(), "buffer 5건 모두 drain 후 underlying publish")
}

// TestBufferDrainer_SkipsWhenBacklogReached 은 backlog >= target 시 drain skip 검증.
func TestBufferDrainer_SkipsWhenBacklogReached(t *testing.T) {
	buf := newMockJobBuffer()
	for i := 0; i < 3; i++ {
		payload := encodeMsg(t, queue.Message{Topic: queue.TopicCrawlNormal, Value: []byte("v")})
		buf.seed("normal", payload)
	}

	prod := &mockDrainerProducer{}
	check := &fixedBacklogChecker{}
	check.lag.Store(5000) // 매우 큰 lag

	d, err := scheduler.NewBufferDrainer(buf, prod, check, scheduler.BufferDrainerConfig{
		Interval:      time.Hour,
		TargetBacklog: 3000, // available = -2000 → skip
		DrainBatch:    100,
		GroupID:       queue.GroupCrawlerWorkers,
	}, newDrainerTestLog())
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	d.Start(ctx)
	time.Sleep(80 * time.Millisecond)
	cancel()
	d.Stop()

	assert.Equal(t, 0, prod.count(), "backlog 임계 도달 — drain 0")
	// buffer 에 그대로 남아있어야 함
	n, _ := buf.JobBufferLen(context.Background(), "normal")
	assert.Equal(t, int64(3), n)
}

// TestBufferDrainer_ReEnqueueOnPublishFailure 는 publish 실패 시 drained payload 가 재적재되는지 검증.
func TestBufferDrainer_ReEnqueueOnPublishFailure(t *testing.T) {
	buf := newMockJobBuffer()
	for i := 0; i < 3; i++ {
		payload := encodeMsg(t, queue.Message{Topic: queue.TopicCrawlNormal, Value: []byte("v")})
		buf.seed("normal", payload)
	}

	prod := &mockDrainerProducer{failErr: errors.New("kafka down")}
	check := &fixedBacklogChecker{}
	check.lag.Store(0)

	d, err := scheduler.NewBufferDrainer(buf, prod, check, scheduler.BufferDrainerConfig{
		Interval:      time.Hour,
		TargetBacklog: 1000,
		DrainBatch:    100,
		GroupID:       queue.GroupCrawlerWorkers,
	}, newDrainerTestLog())
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	d.Start(ctx)
	time.Sleep(80 * time.Millisecond)
	cancel()
	d.Stop()

	assert.Equal(t, 0, prod.count(), "publish 실패 → underlying 에 아무것도 안 들어감")
	n, _ := buf.JobBufferLen(context.Background(), "normal")
	assert.Equal(t, int64(3), n, "실패한 drained payload 가 buffer 에 재적재되어 잔존")
}

// TestBufferDrainer_IdleWhenBufferEmpty 는 buffer 비어있을 때 publish 호출 없음 검증.
func TestBufferDrainer_IdleWhenBufferEmpty(t *testing.T) {
	buf := newMockJobBuffer()
	prod := &mockDrainerProducer{}
	check := &fixedBacklogChecker{}
	check.lag.Store(0)

	d, err := scheduler.NewBufferDrainer(buf, prod, check, scheduler.BufferDrainerConfig{
		Interval:      time.Hour,
		TargetBacklog: 1000,
		DrainBatch:    100,
		GroupID:       queue.GroupCrawlerWorkers,
	}, newDrainerTestLog())
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	d.Start(ctx)
	time.Sleep(80 * time.Millisecond)
	cancel()
	d.Stop()

	assert.Equal(t, 0, prod.count())
}

// TestBufferDrainer_NilDepsReturnError 는 필수 의존성 nil 시 생성자가 error 반환을 검증.
func TestBufferDrainer_NilDepsReturnError(t *testing.T) {
	buf := newMockJobBuffer()
	prod := &mockDrainerProducer{}
	check := &fixedBacklogChecker{}
	log := newDrainerTestLog()
	cfg := scheduler.BufferDrainerConfig{Interval: time.Second, TargetBacklog: 1000}

	_, err := scheduler.NewBufferDrainer(nil, prod, check, cfg, log)
	assert.Error(t, err, "nil buffer")
	_, err = scheduler.NewBufferDrainer(buf, nil, check, cfg, log)
	assert.Error(t, err, "nil producer")
	_, err = scheduler.NewBufferDrainer(buf, prod, nil, cfg, log)
	assert.Error(t, err, "nil checker")
	_, err = scheduler.NewBufferDrainer(buf, prod, check, cfg, nil)
	assert.Error(t, err, "nil logger")
}
