package queue_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/pkg/logger"
	"issuetracker/pkg/queue"
)

// fakeProducer 는 underlying Kafka producer 의 in-memory mock.
type fakeProducer struct {
	mu        sync.Mutex
	published []queue.Message
	failErr   error // Publish/PublishBatch 가 모두 이 에러 반환 — 실패 시나리오 시뮬레이션
}

func (p *fakeProducer) Publish(_ context.Context, msg queue.Message) error {
	if p.failErr != nil {
		return p.failErr
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.published = append(p.published, msg)
	return nil
}

func (p *fakeProducer) PublishBatch(_ context.Context, msgs []queue.Message) error {
	if p.failErr != nil {
		return p.failErr
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.published = append(p.published, msgs...)
	return nil
}

func (p *fakeProducer) Close() error { return nil }

func (p *fakeProducer) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.published)
}

// fakeBuffer 는 in-memory JobBuffer mock.
type fakeBuffer struct {
	mu         sync.Mutex
	enqueued   map[string][][]byte
	enqueueErr error
}

func newFakeBuffer() *fakeBuffer {
	return &fakeBuffer{enqueued: make(map[string][][]byte)}
}

func (b *fakeBuffer) EnqueueJob(_ context.Context, label string, payload []byte, _ int64) error {
	if b.enqueueErr != nil {
		return b.enqueueErr
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.enqueued[label] = append(b.enqueued[label], payload)
	return nil
}

func (b *fakeBuffer) EnqueueBatch(_ context.Context, label string, payloads [][]byte, _ int64) error {
	if b.enqueueErr != nil {
		return b.enqueueErr
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.enqueued[label] = append(b.enqueued[label], payloads...)
	return nil
}

func (b *fakeBuffer) DrainJobs(_ context.Context, label string, n int) ([][]byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	items := b.enqueued[label]
	if len(items) == 0 || n <= 0 {
		return nil, nil
	}
	if n > len(items) {
		n = len(items)
	}
	out := items[:n]
	b.enqueued[label] = items[n:]
	return out, nil
}

func (b *fakeBuffer) JobBufferLen(_ context.Context, label string) (int64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return int64(len(b.enqueued[label])), nil
}

func (b *fakeBuffer) lenFor(label string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.enqueued[label])
}

func newTestLog() *logger.Logger {
	return logger.New(logger.DefaultConfig())
}

// TestBufferingProducer_Publish_NormalRoutesToBuffer 는 normal topic 메시지가 buffer 로 라우팅되는지 검증.
func TestBufferingProducer_Publish_NormalRoutesToBuffer(t *testing.T) {
	under := &fakeProducer{}
	buf := newFakeBuffer()
	p := queue.NewBufferingProducer(under, buf, 1000, newTestLog())

	err := p.Publish(context.Background(), queue.Message{
		Topic: queue.TopicCrawlNormal,
		Key:   []byte("k1"),
		Value: []byte("v1"),
	})
	require.NoError(t, err)
	assert.Equal(t, 0, under.count(), "normal 은 underlying 직접 publish 안 함")
	assert.Equal(t, 1, buf.lenFor("normal"))
	assert.Equal(t, 0, buf.lenFor("low"))
}

// TestBufferingProducer_Publish_LowRoutesToBuffer 는 low topic 메시지가 buffer 로 라우팅되는지 검증.
func TestBufferingProducer_Publish_LowRoutesToBuffer(t *testing.T) {
	under := &fakeProducer{}
	buf := newFakeBuffer()
	p := queue.NewBufferingProducer(under, buf, 1000, newTestLog())

	err := p.Publish(context.Background(), queue.Message{
		Topic: queue.TopicCrawlLow,
		Value: []byte("v"),
	})
	require.NoError(t, err)
	assert.Equal(t, 0, under.count())
	assert.Equal(t, 1, buf.lenFor("low"))
}

// TestBufferingProducer_Publish_HighDirectPublish 는 high priority 가 buffer 우회하여 직접 publish 되는지 검증.
func TestBufferingProducer_Publish_HighDirectPublish(t *testing.T) {
	under := &fakeProducer{}
	buf := newFakeBuffer()
	p := queue.NewBufferingProducer(under, buf, 1000, newTestLog())

	err := p.Publish(context.Background(), queue.Message{
		Topic: queue.TopicCrawlHigh,
		Value: []byte("v"),
	})
	require.NoError(t, err)
	assert.Equal(t, 1, under.count(), "high 는 underlying 직접 publish")
	assert.Equal(t, 0, buf.lenFor("normal"))
	assert.Equal(t, 0, buf.lenFor("low"))
}

// TestBufferingProducer_Publish_NonCrawlTopicDirectPublish 는 crawl 외 토픽은 우회하는지 검증.
func TestBufferingProducer_Publish_NonCrawlTopicDirectPublish(t *testing.T) {
	under := &fakeProducer{}
	buf := newFakeBuffer()
	p := queue.NewBufferingProducer(under, buf, 1000, newTestLog())

	err := p.Publish(context.Background(), queue.Message{
		Topic: "issuetracker.normalized", // crawl 외
		Value: []byte("v"),
	})
	require.NoError(t, err)
	assert.Equal(t, 1, under.count())
}

// TestBufferingProducer_Publish_BufferFailFallback 은 buffer enqueue 실패 시 underlying 으로 fallback 검증.
func TestBufferingProducer_Publish_BufferFailFallback(t *testing.T) {
	under := &fakeProducer{}
	buf := newFakeBuffer()
	buf.enqueueErr = errors.New("redis down")
	p := queue.NewBufferingProducer(under, buf, 1000, newTestLog())

	err := p.Publish(context.Background(), queue.Message{
		Topic: queue.TopicCrawlNormal,
		Value: []byte("v"),
	})
	require.NoError(t, err, "fallback 으로 underlying.Publish 가 성공하면 본 호출도 성공")
	assert.Equal(t, 1, under.count(), "buffer 실패 시 underlying 으로 fallback")
}

// countingFakeBuffer 는 EnqueueBatch 호출 횟수를 추적해 단일 batch 호출 검증용.
type countingFakeBuffer struct {
	*fakeBuffer
	batchCallsPerLabel map[string]int
}

func newCountingFakeBuffer() *countingFakeBuffer {
	return &countingFakeBuffer{
		fakeBuffer:         newFakeBuffer(),
		batchCallsPerLabel: make(map[string]int),
	}
}

func (b *countingFakeBuffer) EnqueueBatch(ctx context.Context, label string, payloads [][]byte, maxLen int64) error {
	b.mu.Lock()
	b.batchCallsPerLabel[label]++
	b.mu.Unlock()
	return b.fakeBuffer.EnqueueBatch(ctx, label, payloads, maxLen)
}

// TestBufferingProducer_PublishBatch_UsesSingleEnqueueBatchPerLabel 는 같은 label 의 N개 메시지가
// 단일 EnqueueBatch 호출로 처리되는지 검증 (gemini PR #511 피드백).
func TestBufferingProducer_PublishBatch_UsesSingleEnqueueBatchPerLabel(t *testing.T) {
	under := &fakeProducer{}
	buf := newCountingFakeBuffer()
	p := queue.NewBufferingProducer(under, buf, 1000, newTestLog())

	msgs := []queue.Message{
		{Topic: queue.TopicCrawlNormal, Value: []byte("n1")},
		{Topic: queue.TopicCrawlNormal, Value: []byte("n2")},
		{Topic: queue.TopicCrawlNormal, Value: []byte("n3")},
		{Topic: queue.TopicCrawlLow, Value: []byte("l1")},
		{Topic: queue.TopicCrawlLow, Value: []byte("l2")},
	}
	require.NoError(t, p.PublishBatch(context.Background(), msgs))

	// 같은 label 의 3개 normal 메시지 → 1회 EnqueueBatch, 2개 low → 1회 EnqueueBatch.
	assert.Equal(t, 1, buf.batchCallsPerLabel["normal"], "label 'normal' 1회 batch 호출")
	assert.Equal(t, 1, buf.batchCallsPerLabel["low"], "label 'low' 1회 batch 호출")
	assert.Equal(t, 3, buf.lenFor("normal"))
	assert.Equal(t, 2, buf.lenFor("low"))
}

// TestBufferingProducer_PublishBatch_MixedPriorities 는 batch 안 high/normal/low 혼합 라우팅 검증.
func TestBufferingProducer_PublishBatch_MixedPriorities(t *testing.T) {
	under := &fakeProducer{}
	buf := newFakeBuffer()
	p := queue.NewBufferingProducer(under, buf, 1000, newTestLog())

	msgs := []queue.Message{
		{Topic: queue.TopicCrawlHigh, Value: []byte("h1")},
		{Topic: queue.TopicCrawlNormal, Value: []byte("n1")},
		{Topic: queue.TopicCrawlLow, Value: []byte("l1")},
		{Topic: queue.TopicCrawlHigh, Value: []byte("h2")},
		{Topic: queue.TopicCrawlNormal, Value: []byte("n2")},
	}
	require.NoError(t, p.PublishBatch(context.Background(), msgs))

	assert.Equal(t, 2, under.count(), "high 2개만 underlying 으로 직접")
	assert.Equal(t, 2, buf.lenFor("normal"))
	assert.Equal(t, 1, buf.lenFor("low"))
}

// TestBufferingProducer_NilBufferUsesNoop 은 nil buffer 가 NoopJobBuffer 로 대체되어 fallback 됨을 검증.
func TestBufferingProducer_NilBufferUsesNoop(t *testing.T) {
	under := &fakeProducer{}
	p := queue.NewBufferingProducer(under, nil, 0, newTestLog()) // nil buffer

	err := p.Publish(context.Background(), queue.Message{
		Topic: queue.TopicCrawlNormal,
		Value: []byte("v"),
	})
	require.NoError(t, err)
	assert.Equal(t, 1, under.count(), "nil buffer → Noop 가 항상 error → fallback 으로 underlying 호출")
}

// TestBufferingProducer_Underlying 은 Underlying() 이 데코 우회용 producer 를 반환하는지 검증.
func TestBufferingProducer_Underlying(t *testing.T) {
	under := &fakeProducer{}
	buf := newFakeBuffer()
	p := queue.NewBufferingProducer(under, buf, 1000, newTestLog())

	got := p.Underlying()
	assert.Same(t, under, got, "Underlying 은 wrapping 전 raw producer 그대로 반환")
}

// TestEncodeDecodeBufferedMessage 는 buffered 직렬화 round-trip 을 검증.
func TestEncodeDecodeBufferedMessage(t *testing.T) {
	under := &fakeProducer{}
	buf := newFakeBuffer()
	p := queue.NewBufferingProducer(under, buf, 1000, newTestLog())

	orig := queue.Message{
		Topic:   queue.TopicCrawlNormal,
		Key:     []byte("key-123"),
		Value:   []byte(`{"hello":"world"}`),
		Headers: map[string]string{"crawler": "test", "priority": "1"},
	}
	require.NoError(t, p.Publish(context.Background(), orig))

	// buffer 에서 payload 를 꺼내 직접 decode.
	payloads, err := buf.DrainJobs(context.Background(), "normal", 10)
	require.NoError(t, err)
	require.Len(t, payloads, 1)

	decoded, bufferedAt, err := queue.DecodeBufferedMessage(payloads[0])
	require.NoError(t, err)
	assert.Equal(t, orig.Topic, decoded.Topic)
	assert.Equal(t, orig.Key, decoded.Key)
	assert.Equal(t, orig.Value, decoded.Value)
	assert.Equal(t, orig.Headers, decoded.Headers)
	assert.False(t, bufferedAt.IsZero(), "BufferedAt 은 enqueue 시점에 채워짐")
}
