package claudegen_test

// pool_test.go — 이슈 #352 ClaudeWorkerPool 단위 테스트.
//
// 검증 항목:
//  1. Start: 모든 worker 컨테이너 기동 + 부분 실패 시 cleanup
//  2. Stop:  모든 worker 컨테이너 정리 + 일부 실패 → first error 반환
//  3. Extract round-robin: N concurrent 호출이 N worker 에 분배됨
//  4. WorkerCount / ModelName 메타 메소드
//  5. 잘못된 입력 (nil worker / 빈 slice 등)

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/processor/parser/rule/claudegen"
	"issuetracker/internal/storage"
	"issuetracker/pkg/logger"
)

// poolMockRunner 는 ExecSession 호출 시 호출된 containerID 를 기록 + 호출 횟수를 추적합니다.
// pool 의 round-robin 분배를 검증하기 위해 worker 간 구분이 가능한 containerID 가 필요.
type poolMockRunner struct {
	id         string
	startErr   error
	execStdout string
	execStderr string
	execErr    error
	execDelay  time.Duration // ExecSession 내부 인위적 지연 — concurrent 분배 검증용
	execCalls  atomic.Int64
	stopCalls  atomic.Int64
	startCalls atomic.Int64
}

func (m *poolMockRunner) StartContainer(_ context.Context, _, _, _, _ string) (string, error) {
	m.startCalls.Add(1)
	if m.startErr != nil {
		return "", m.startErr
	}
	return m.id, nil
}

func (m *poolMockRunner) ExecSession(_ context.Context, _ string, _ []string) (string, string, error) {
	m.execCalls.Add(1)
	if m.execDelay > 0 {
		time.Sleep(m.execDelay)
	}
	return m.execStdout, m.execStderr, m.execErr
}

func (m *poolMockRunner) StopContainer(_ context.Context, _ string) error {
	m.stopCalls.Add(1)
	return nil
}

// newTestPoolWorker 는 ID 가 다른 runner 와 함께 ClaudeWorker 를 생성하는 헬퍼.
func newTestPoolWorker(t *testing.T, runner *poolMockRunner) *claudegen.ClaudeWorker {
	t.Helper()
	log := logger.New(logger.DefaultConfig())
	authDir := makeAuthDir(t)
	w, err := claudegen.NewWithRunner(
		"ghcr.io/anthropics/claude-code:latest",
		"claude-sonnet-4-6",
		authDir,
		"/root/.claude",
		10*time.Second,
		runner,
		claudegenLoader,
		log,
	)
	require.NoError(t, err)
	return w
}

// successStdout — 정상 selector 추출 JSON (worker_test.go 와 동일 형식).
const successStdout = `{"title":{"css":"h1.article-title"},"main_content":{"css":"div.article-body","multi":true},"published_at":{"css":"time","attribute":"datetime"}}`

// TestPool_NewPool_NilLogger 는 nil logger 입력 시 error 반환.
func TestPool_NewPool_NilLogger(t *testing.T) {
	_, err := claudegen.NewPool([]*claudegen.ClaudeWorker{nil}, nil)
	require.Error(t, err)
}

// TestPool_NewPool_EmptyWorkers 는 빈 slice 입력 시 error 반환.
func TestPool_NewPool_EmptyWorkers(t *testing.T) {
	log := logger.New(logger.DefaultConfig())
	_, err := claudegen.NewPool([]*claudegen.ClaudeWorker{}, log)
	require.Error(t, err)
}

// TestPool_NewPool_NilWorker 는 nil 포함 slice 입력 시 error 반환.
func TestPool_NewPool_NilWorker(t *testing.T) {
	log := logger.New(logger.DefaultConfig())
	r := &poolMockRunner{id: "c1", execStdout: successStdout}
	w := newTestPoolWorker(t, r)
	_, err := claudegen.NewPool([]*claudegen.ClaudeWorker{w, nil}, log)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "workers[1] is nil")
}

// TestPool_StartStop_AllContainers 는 N worker pool 의 Start/Stop 이 N 컨테이너 모두 다루는지 검증.
func TestPool_StartStop_AllContainers(t *testing.T) {
	log := logger.New(logger.DefaultConfig())
	r1 := &poolMockRunner{id: "c1", execStdout: successStdout}
	r2 := &poolMockRunner{id: "c2", execStdout: successStdout}
	r3 := &poolMockRunner{id: "c3", execStdout: successStdout}
	workers := []*claudegen.ClaudeWorker{
		newTestPoolWorker(t, r1),
		newTestPoolWorker(t, r2),
		newTestPoolWorker(t, r3),
	}
	pool, err := claudegen.NewPool(workers, log)
	require.NoError(t, err)

	require.NoError(t, pool.Start(context.Background()))
	assert.Equal(t, int64(1), r1.startCalls.Load())
	assert.Equal(t, int64(1), r2.startCalls.Load())
	assert.Equal(t, int64(1), r3.startCalls.Load())
	assert.Equal(t, 3, pool.WorkerCount())

	require.NoError(t, pool.Stop(context.Background()))
	assert.Equal(t, int64(1), r1.stopCalls.Load())
	assert.Equal(t, int64(1), r2.stopCalls.Load())
	assert.Equal(t, int64(1), r3.stopCalls.Load())
}

// TestPool_Start_PartialFailure_CleansUpStarted 는 일부 worker 실패 시 이미 성공한
// worker 가 cleanup 되는지 검증.
func TestPool_Start_PartialFailure_CleansUpStarted(t *testing.T) {
	log := logger.New(logger.DefaultConfig())
	r1 := &poolMockRunner{id: "c1"}
	r2 := &poolMockRunner{id: "c2", startErr: errors.New("docker daemon down")}
	r3 := &poolMockRunner{id: "c3"}
	workers := []*claudegen.ClaudeWorker{
		newTestPoolWorker(t, r1),
		newTestPoolWorker(t, r2),
		newTestPoolWorker(t, r3),
	}
	pool, err := claudegen.NewPool(workers, log)
	require.NoError(t, err)

	err = pool.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "docker daemon down")

	// 성공한 r1, r3 는 Stop 이 호출되어야 함 (cleanup), 실패한 r2 는 호출 안 됨.
	assert.Equal(t, int64(1), r1.stopCalls.Load(), "started worker must be stopped on partial failure")
	assert.Equal(t, int64(0), r2.stopCalls.Load(), "failed worker has no container to stop")
	assert.Equal(t, int64(1), r3.stopCalls.Load(), "started worker must be stopped on partial failure")
}

// TestPool_Extract_RoundRobin 은 concurrent Extract 호출이 N worker 에 균등 분배되는지 검증.
func TestPool_Extract_RoundRobin(t *testing.T) {
	log := logger.New(logger.DefaultConfig())
	const poolSize = 3
	const callCount = 9 // 3 worker × 3 call
	runners := make([]*poolMockRunner, poolSize)
	workers := make([]*claudegen.ClaudeWorker, poolSize)
	for i := 0; i < poolSize; i++ {
		runners[i] = &poolMockRunner{
			id:         "c" + string(rune('1'+i)),
			execStdout: successStdout,
			execDelay:  10 * time.Millisecond, // 모든 worker 가 동시에 busy 가 되도록
		}
		workers[i] = newTestPoolWorker(t, runners[i])
	}
	pool, err := claudegen.NewPool(workers, log)
	require.NoError(t, err)
	require.NoError(t, pool.Start(context.Background()))
	t.Cleanup(func() { _ = pool.Stop(context.Background()) })

	// concurrent Extract 호출
	var wg sync.WaitGroup
	for i := 0; i < callCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = pool.Extract(context.Background(), "example.com", storage.TargetTypePage, "<html></html>")
		}()
	}
	wg.Wait()

	// 각 worker 가 정확히 callCount/poolSize = 3 번 호출 (round-robin 균등 분배).
	for i, r := range runners {
		assert.Equal(t, int64(callCount/poolSize), r.execCalls.Load(),
			"worker[%d] should be called %d times under round-robin", i, callCount/poolSize)
	}
}

// TestPool_Extract_SequentialDistributesRoundRobin 은 순차 호출도 round-robin 패턴을 따르는지 검증.
func TestPool_Extract_SequentialDistributesRoundRobin(t *testing.T) {
	log := logger.New(logger.DefaultConfig())
	r1 := &poolMockRunner{id: "c1", execStdout: successStdout}
	r2 := &poolMockRunner{id: "c2", execStdout: successStdout}
	workers := []*claudegen.ClaudeWorker{
		newTestPoolWorker(t, r1),
		newTestPoolWorker(t, r2),
	}
	pool, err := claudegen.NewPool(workers, log)
	require.NoError(t, err)
	require.NoError(t, pool.Start(context.Background()))
	t.Cleanup(func() { _ = pool.Stop(context.Background()) })

	for i := 0; i < 4; i++ {
		_, _ = pool.Extract(context.Background(), "example.com", storage.TargetTypePage, "<html></html>")
	}

	// 정확히 2회씩 분배.
	assert.Equal(t, int64(2), r1.execCalls.Load())
	assert.Equal(t, int64(2), r2.execCalls.Load())
}

// TestPool_ModelName 은 pool 의 ModelName 이 첫 worker 의 model 을 반환하는지 검증.
func TestPool_ModelName(t *testing.T) {
	log := logger.New(logger.DefaultConfig())
	r := &poolMockRunner{id: "c1"}
	workers := []*claudegen.ClaudeWorker{newTestPoolWorker(t, r)}
	pool, err := claudegen.NewPool(workers, log)
	require.NoError(t, err)
	assert.Equal(t, "claude-sonnet-4-6", pool.ModelName())
}

// TestPool_NewPoolFromEnv_DefaultCount 는 환경변수 미설정 시 기본 worker 수 (2) 가 적용되는지 검증.
// 본 테스트는 실제 docker 호출은 하지 않으며 (validateAuthDir 만 통과), Start 는 호출하지 않음.
func TestPool_NewPoolFromEnv_DefaultCount(t *testing.T) {
	log := logger.New(logger.DefaultConfig())
	// 환경변수 unset 보장
	t.Setenv("CLAUDE_CODE_WORKER_COUNT", "")
	t.Setenv("CLAUDE_CODE_AUTH_DIR", makeAuthDir(t))

	pool, err := claudegen.NewPoolFromEnv(claudegenLoader, log)
	require.NoError(t, err)
	assert.Equal(t, 2, pool.WorkerCount(), "default worker count must be 2")
}

// TestPool_NewPoolFromEnv_EnvOverride 는 환경변수로 worker 수 조정이 반영되는지 검증.
func TestPool_NewPoolFromEnv_EnvOverride(t *testing.T) {
	log := logger.New(logger.DefaultConfig())
	t.Setenv("CLAUDE_CODE_WORKER_COUNT", "4")
	t.Setenv("CLAUDE_CODE_AUTH_DIR", makeAuthDir(t))

	pool, err := claudegen.NewPoolFromEnv(claudegenLoader, log)
	require.NoError(t, err)
	assert.Equal(t, 4, pool.WorkerCount())
}

// TestPool_NewPoolFromEnv_InvalidValueFallsBack 은 잘못된 값 (parse 실패 / 음수 / 0) 시
// default 로 fallback 되는지 검증.
func TestPool_NewPoolFromEnv_InvalidValueFallsBack(t *testing.T) {
	log := logger.New(logger.DefaultConfig())
	t.Setenv("CLAUDE_CODE_AUTH_DIR", makeAuthDir(t))

	for _, raw := range []string{"abc", "-1", "0"} {
		t.Run("raw="+raw, func(t *testing.T) {
			t.Setenv("CLAUDE_CODE_WORKER_COUNT", raw)
			pool, err := claudegen.NewPoolFromEnv(claudegenLoader, log)
			require.NoError(t, err)
			assert.Equal(t, 2, pool.WorkerCount(), "invalid input must fallback to default 2")
		})
	}
}

// TestPool_NewPoolFromEnv_ExceedsMax_ClampsToCap 은 매우 큰 값 입력 시 maxWorkerCount 로
// clamp 되는지 검증 (docker 누수 / API quota 폭발 방지).
func TestPool_NewPoolFromEnv_ExceedsMax_ClampsToCap(t *testing.T) {
	log := logger.New(logger.DefaultConfig())
	t.Setenv("CLAUDE_CODE_WORKER_COUNT", "999")
	t.Setenv("CLAUDE_CODE_AUTH_DIR", makeAuthDir(t))

	pool, err := claudegen.NewPoolFromEnv(claudegenLoader, log)
	require.NoError(t, err)
	assert.LessOrEqual(t, pool.WorkerCount(), 16, "must be clamped to maxWorkerCount")
}
