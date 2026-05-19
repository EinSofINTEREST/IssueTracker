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

	"issuetracker/internal/processor/fetcher/core"
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

func (b *mockJobBuffer) EnqueueBatch(_ context.Context, label string, payloads [][]byte, _ int64) error {
	if b.enqErr != nil {
		return b.enqErr
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.store[label] = append(b.store[label], payloads...)
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

// TestBufferDrainer_DrainsLowPriority 는 low label 도 정상 drain 되는지 검증 (Copilot PR #511 피드백).
// normal 만 검증하면 drainTargets 의 low 매핑 누락/오류가 회귀로 잡히지 않음.
func TestBufferDrainer_DrainsLowPriority(t *testing.T) {
	buf := newMockJobBuffer()
	for i := 0; i < 3; i++ {
		payload := encodeMsg(t, queue.Message{Topic: queue.TopicCrawlLow, Value: []byte("v")})
		buf.seed("low", payload)
	}

	prod := &mockDrainerProducer{}
	check := &fixedBacklogChecker{}
	check.lag.Store(50)

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

	assert.Equal(t, 3, prod.count(), "low priority buffer 3건 모두 drain")
	for _, m := range prod.published {
		assert.Equal(t, queue.TopicCrawlLow, m.Topic, "low buffer payload 는 low topic 으로 publish")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Leader election + retry path tests (이슈 #512)
// ─────────────────────────────────────────────────────────────────────────────

// mockLeaderLocker 는 TryAcquire / Release 결과를 호출자가 제어할 수 있는 mock.
type mockLeaderLocker struct {
	mu           sync.Mutex
	tryResult    bool  // 기본 true
	tryErr       error // nil 이면 success
	acquireCount int
	releaseCount int
}

func newMockLeaderLocker(acquire bool) *mockLeaderLocker {
	return &mockLeaderLocker{tryResult: acquire}
}

func (l *mockLeaderLocker) TryAcquire(_ context.Context) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.acquireCount++
	return l.tryResult, l.tryErr
}

func (l *mockLeaderLocker) Release(_ context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.releaseCount++
	return nil
}

// mockRetryScheduler 는 Enqueue 호출을 기록.
type mockRetryScheduler struct {
	mu         sync.Mutex
	enqueued   []*core.CrawlJob
	enqueueErr error
}

func (m *mockRetryScheduler) Enqueue(_ context.Context, job *core.CrawlJob, _ error) error {
	if m.enqueueErr != nil {
		return m.enqueueErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.enqueued = append(m.enqueued, job)
	return nil
}

func (m *mockRetryScheduler) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.enqueued)
}

// encodeJobMsg 는 CrawlJob 을 marshal 한 msg.Value 를 갖는 buffered payload 를 만듭니다.
// retry path 검증용 — drainer 가 msg.Value 를 UnmarshalCrawlJob 으로 복원해야 함.
func encodeJobMsg(t *testing.T, topic string, job *core.CrawlJob) []byte {
	t.Helper()
	data, err := job.Marshal()
	require.NoError(t, err)
	return encodeMsg(t, queue.Message{Topic: topic, Key: []byte(job.ID), Value: data})
}

// TestBufferDrainer_NonLeader_SkipsCycle 는 leader 미획득 시 drain skip 검증.
func TestBufferDrainer_NonLeader_SkipsCycle(t *testing.T) {
	buf := newMockJobBuffer()
	job := &core.CrawlJob{ID: "j1", CrawlerName: "test", Target: core.Target{URL: "https://example.com/a"}, Priority: core.PriorityNormal}
	buf.seed("normal", encodeJobMsg(t, queue.TopicCrawlNormal, job))

	prod := &mockDrainerProducer{}
	check := &fixedBacklogChecker{}
	leader := newMockLeaderLocker(false) // leader 획득 실패

	d, err := scheduler.NewBufferDrainer(buf, prod, check, scheduler.BufferDrainerConfig{
		Interval:      time.Hour,
		TargetBacklog: 1000,
		DrainBatch:    100,
		GroupID:       queue.GroupCrawlerWorkers,
		Leader:        leader,
	}, newDrainerTestLog())
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	d.Start(ctx)
	time.Sleep(80 * time.Millisecond)
	cancel()
	d.Stop()

	assert.Equal(t, 0, prod.count(), "non-leader: publish 0건")
	assert.GreaterOrEqual(t, leader.acquireCount, 1, "TryAcquire 호출됨")
	assert.Equal(t, 0, leader.releaseCount, "non-leader 는 Release 안 함")
	n, _ := buf.JobBufferLen(context.Background(), "normal")
	assert.Equal(t, int64(1), n, "buffer 항목 보존")
}

// TestBufferDrainer_Leader_DrainsAndReleases 는 leader 획득 시 drain 후 release 검증.
func TestBufferDrainer_Leader_DrainsAndReleases(t *testing.T) {
	buf := newMockJobBuffer()
	job := &core.CrawlJob{ID: "j1", CrawlerName: "test", Target: core.Target{URL: "https://example.com/a"}, Priority: core.PriorityNormal}
	buf.seed("normal", encodeJobMsg(t, queue.TopicCrawlNormal, job))

	prod := &mockDrainerProducer{}
	check := &fixedBacklogChecker{}
	leader := newMockLeaderLocker(true) // leader 획득 성공

	d, err := scheduler.NewBufferDrainer(buf, prod, check, scheduler.BufferDrainerConfig{
		Interval:      time.Hour,
		TargetBacklog: 1000,
		DrainBatch:    100,
		GroupID:       queue.GroupCrawlerWorkers,
		Leader:        leader,
	}, newDrainerTestLog())
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	d.Start(ctx)
	time.Sleep(80 * time.Millisecond)
	cancel()
	d.Stop()

	assert.Equal(t, 1, prod.count(), "leader: drain 후 publish")
	assert.GreaterOrEqual(t, leader.acquireCount, 1)
	assert.GreaterOrEqual(t, leader.releaseCount, 1, "leader cycle 후 Release 호출")
}

// TestBufferDrainer_LeaderAcquireError_FailsClosed 는 election Redis 인프라 에러 시 fail-closed 검증.
func TestBufferDrainer_LeaderAcquireError_FailsClosed(t *testing.T) {
	buf := newMockJobBuffer()
	job := &core.CrawlJob{ID: "j1", CrawlerName: "test", Target: core.Target{URL: "https://example.com/a"}, Priority: core.PriorityNormal}
	buf.seed("normal", encodeJobMsg(t, queue.TopicCrawlNormal, job))

	prod := &mockDrainerProducer{}
	check := &fixedBacklogChecker{}
	leader := newMockLeaderLocker(false)
	leader.tryErr = errors.New("redis down")

	d, err := scheduler.NewBufferDrainer(buf, prod, check, scheduler.BufferDrainerConfig{
		Interval:      time.Hour,
		TargetBacklog: 1000,
		DrainBatch:    100,
		GroupID:       queue.GroupCrawlerWorkers,
		Leader:        leader,
	}, newDrainerTestLog())
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	d.Start(ctx)
	time.Sleep(80 * time.Millisecond)
	cancel()
	d.Stop()

	assert.Equal(t, 0, prod.count(), "election Redis 에러 → fail-closed (drain skip)")
}

// TestBufferDrainer_PublishFailDispatchesToRetryScheduler 는 publish 실패 시 RetryScheduler 경유 검증.
func TestBufferDrainer_PublishFailDispatchesToRetryScheduler(t *testing.T) {
	buf := newMockJobBuffer()
	for i := 0; i < 3; i++ {
		job := &core.CrawlJob{
			ID:          "j" + string(rune('a'+i)),
			CrawlerName: "test",
			Target:      core.Target{URL: "https://example.com/" + string(rune('a'+i))},
			Priority:    core.PriorityNormal,
		}
		buf.seed("normal", encodeJobMsg(t, queue.TopicCrawlNormal, job))
	}

	prod := &mockDrainerProducer{failErr: errors.New("kafka down")}
	check := &fixedBacklogChecker{}
	rs := &mockRetryScheduler{}

	d, err := scheduler.NewBufferDrainer(buf, prod, check, scheduler.BufferDrainerConfig{
		Interval:       time.Hour,
		TargetBacklog:  1000,
		DrainBatch:     100,
		GroupID:        queue.GroupCrawlerWorkers,
		RetryScheduler: rs, // 이슈 #512 — Redis 재적재 대신 Kafka 재시도 경로
	}, newDrainerTestLog())
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	d.Start(ctx)
	time.Sleep(80 * time.Millisecond)
	cancel()
	d.Stop()

	assert.Equal(t, 0, prod.count(), "publish 실패 (kafka down)")
	assert.Equal(t, 3, rs.count(), "실패한 3건 모두 RetryScheduler 로 dispatch")
	// buffer 는 비어있어야 함 — Redis 재적재 안 함
	n, _ := buf.JobBufferLen(context.Background(), "normal")
	assert.Equal(t, int64(0), n, "RetryScheduler 사용 시 Redis 재적재 X")
}

// TestBufferDrainer_RetryDispatchFailure_ReenqueueToBuffer 는 RetryScheduler.Enqueue 실패 시
// 실패한 항목이 Redis buffer 로 재적재되는지 검증 (Copilot PR #516 피드백 — 데이터 손실 방어).
func TestBufferDrainer_RetryDispatchFailure_ReenqueueToBuffer(t *testing.T) {
	buf := newMockJobBuffer()
	job := &core.CrawlJob{ID: "j1", CrawlerName: "test", Target: core.Target{URL: "https://example.com/a"}, Priority: core.PriorityNormal}
	buf.seed("normal", encodeJobMsg(t, queue.TopicCrawlNormal, job))

	prod := &mockDrainerProducer{failErr: errors.New("kafka down")}
	check := &fixedBacklogChecker{}
	rs := &mockRetryScheduler{enqueueErr: errors.New("retry scheduler also down")}

	d, err := scheduler.NewBufferDrainer(buf, prod, check, scheduler.BufferDrainerConfig{
		Interval:       time.Hour,
		TargetBacklog:  1000,
		DrainBatch:     100,
		GroupID:        queue.GroupCrawlerWorkers,
		RetryScheduler: rs,
	}, newDrainerTestLog())
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	d.Start(ctx)
	time.Sleep(80 * time.Millisecond)
	cancel()
	d.Stop()

	assert.Equal(t, 0, prod.count())
	assert.Equal(t, 0, rs.count(), "Enqueue 도 실패 → success 0")
	n, _ := buf.JobBufferLen(context.Background(), "normal")
	assert.Equal(t, int64(1), n, "RetryScheduler.Enqueue 실패한 항목 → buffer 재적재 (데이터 손실 방어)")
}

// TestBufferDrainer_PublishFailNoRetryScheduler_FallsBackToRedis 는 RetryScheduler nil 시 기존 Redis 재적재 fallback 검증.
func TestBufferDrainer_PublishFailNoRetryScheduler_FallsBackToRedis(t *testing.T) {
	buf := newMockJobBuffer()
	job := &core.CrawlJob{ID: "j1", CrawlerName: "test", Target: core.Target{URL: "https://example.com/a"}, Priority: core.PriorityNormal}
	buf.seed("normal", encodeJobMsg(t, queue.TopicCrawlNormal, job))

	prod := &mockDrainerProducer{failErr: errors.New("kafka down")}
	check := &fixedBacklogChecker{}

	d, err := scheduler.NewBufferDrainer(buf, prod, check, scheduler.BufferDrainerConfig{
		Interval:      time.Hour,
		TargetBacklog: 1000,
		DrainBatch:    100,
		GroupID:       queue.GroupCrawlerWorkers,
		// RetryScheduler: nil — fallback 모드
	}, newDrainerTestLog())
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	d.Start(ctx)
	time.Sleep(80 * time.Millisecond)
	cancel()
	d.Stop()

	n, _ := buf.JobBufferLen(context.Background(), "normal")
	assert.Equal(t, int64(1), n, "RetryScheduler 부재 시 Redis 재적재 fallback (기존 PR #511 동작 보존)")
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
