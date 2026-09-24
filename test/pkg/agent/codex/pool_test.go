// 본 파일은 Pool 의 구성 / 분배 / 기동 실패 처리를 검증합니다 (이슈 #535).
package codex_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/storage/model"
	"issuetracker/pkg/agent"
	"issuetracker/pkg/agent/codex"
)

// Pool 이 외부 패키지 시점에서도 agent.Agent 계약을 만족하는지 컴파일 타임에 고정합니다.
var _ agent.Agent = (*codex.Pool)(nil)

func TestPool_NewPool_NilLogger(t *testing.T) {
	_, err := codex.NewPool([]*codex.Worker{newWorker(t, makeAuthDir(t), &mockRunner{})}, nil)
	require.Error(t, err)
}

func TestPool_NewPool_EmptyWorkers(t *testing.T) {
	_, err := codex.NewPool(nil, testLogger())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "at least 1 worker")
}

func TestPool_NewPool_NilWorker(t *testing.T) {
	_, err := codex.NewPool([]*codex.Worker{nil}, testLogger())
	require.Error(t, err)
}

// TestPool_StartStop_AllContainers 는 Start / Stop 이 모든 worker 에 전파되는지 검증합니다.
func TestPool_StartStop_AllContainers(t *testing.T) {
	r1, r2, r3 := &mockRunner{}, &mockRunner{}, &mockRunner{}
	p := newPool(t, r1, r2, r3)

	require.NoError(t, p.Start(t.Context()))
	for i, r := range []*mockRunner{r1, r2, r3} {
		start, _, _ := r.counts()
		assert.Equal(t, 1, start, "worker[%d] 기동", i)
	}

	require.NoError(t, p.Stop(t.Context()))
	for i, r := range []*mockRunner{r1, r2, r3} {
		_, _, stop := r.counts()
		assert.Equal(t, 1, stop, "worker[%d] 종료", i)
	}
	assert.Equal(t, 3, p.WorkerCount())
}

// TestPool_Start_PartialFailure_CleansUpStarted 는 일부 worker 기동 실패 시 이미 뜬
// 컨테이너가 정리되는지 검증합니다.
//
// 정리하지 않으면 운영자에게는 "기동 실패" 로 보이는데 컨테이너는 살아남아 누수됩니다.
// Pool 은 전부 가동 / 전부 종료 두 상태만 노출해야 합니다.
func TestPool_Start_PartialFailure_CleansUpStarted(t *testing.T) {
	ok1 := &mockRunner{}
	bad := &mockRunner{startErr: errors.New("docker unreachable")}
	ok2 := &mockRunner{}
	p := newPool(t, ok1, bad, ok2)

	err := p.Start(t.Context())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "docker unreachable")

	for i, r := range []*mockRunner{ok1, ok2} {
		_, _, stop := r.counts()
		assert.Equal(t, 1, stop, "성공한 worker[%d] 는 정리돼야 한다", i)
	}
	_, _, badStop := bad.counts()
	assert.Zero(t, badStop, "기동 실패한 worker 는 정리 대상이 아니다")
}

// TestPool_Extract_RoundRobin 은 호출이 worker 들에 균등 분배되는지 검증합니다.
func TestPool_Extract_RoundRobin(t *testing.T) {
	runners := []*mockRunner{
		{execStdout: okOutput("h1.a")},
		{execStdout: okOutput("h1.b")},
		{execStdout: okOutput("h1.c")},
	}
	p := newPool(t, runners...)
	require.NoError(t, p.Start(t.Context()))
	t.Cleanup(func() { _ = p.Stop(context.Background()) })

	const calls = 9
	for i := 0; i < calls; i++ {
		_, err := p.Extract(t.Context(), "example.com", model.TargetTypeArticle, "<html></html>")
		require.NoError(t, err)
	}

	for i, r := range runners {
		_, exec, _ := r.counts()
		assert.Equal(t, calls/len(runners), exec, "worker[%d] 분배량", i)
	}
}

// TestPool_ExtractEnriched_RoundRobin 은 ExtractEnriched 도 같은 분배를 쓰는지 검증합니다.
func TestPool_ExtractEnriched_RoundRobin(t *testing.T) {
	runners := []*mockRunner{
		{execStdout: okOutput("h1.a")},
		{execStdout: okOutput("h1.b")},
	}
	p := newPool(t, runners...)
	require.NoError(t, p.Start(t.Context()))
	t.Cleanup(func() { _ = p.Stop(context.Background()) })

	for i := 0; i < 4; i++ {
		res, err := p.ExtractEnriched(t.Context(), "example.com", model.TargetTypeArticle, "<html></html>")
		require.NoError(t, err)
		require.NotNil(t, res)
	}

	for i, r := range runners {
		_, exec, _ := r.counts()
		assert.Equal(t, 2, exec, "worker[%d] 분배량", i)
	}
}

// TestPool_RunSession_RoundRobin 은 enrich 경로 (RunSession) 도 분배되는지 검증합니다.
func TestPool_RunSession_RoundRobin(t *testing.T) {
	runners := []*mockRunner{{execStdout: "a"}, {execStdout: "b"}}
	p := newPool(t, runners...)
	require.NoError(t, p.Start(t.Context()))
	t.Cleanup(func() { _ = p.Stop(context.Background()) })

	for i := 0; i < 4; i++ {
		_, err := p.RunSession(t.Context(), "enrich-extract", nil, "prompt")
		require.NoError(t, err)
	}

	for i, r := range runners {
		_, exec, _ := r.counts()
		assert.Equal(t, 2, exec, "worker[%d] 분배량", i)
	}
}

func TestPool_ModelName(t *testing.T) {
	p := newPool(t, &mockRunner{})
	assert.Equal(t, "gpt-5-codex", p.ModelName())
}

// ── NewPoolFromConfig / env 해석 ──────────────────────────────────────────────

// TestPool_NewPoolFromEnv_DefaultCount 는 미설정 시 기본 worker 수가 쓰이는지 검증합니다.
func TestPool_NewPoolFromEnv_DefaultCount(t *testing.T) {
	t.Setenv("CODEX_WORKER_COUNT", "")
	t.Setenv("CODEX_AUTH_DIR", makeAuthDir(t))

	p, err := codex.NewPoolFromEnv(codexLoader, testLogger())
	require.NoError(t, err)
	assert.Equal(t, 2, p.WorkerCount())
}

// TestPool_NewPoolFromEnv_EnvOverride 는 명시 값이 반영되는지 검증합니다.
func TestPool_NewPoolFromEnv_EnvOverride(t *testing.T) {
	t.Setenv("CODEX_WORKER_COUNT", "4")
	t.Setenv("CODEX_AUTH_DIR", makeAuthDir(t))

	p, err := codex.NewPoolFromEnv(codexLoader, testLogger())
	require.NoError(t, err)
	assert.Equal(t, 4, p.WorkerCount())
}

// TestPool_NewPoolFromEnv_InvalidValueFallsBack 는 잘못된 값이 기본값으로 떨어지는지
// 검증합니다 — 오타 하나로 기동이 막히는 것보다 기본값 + WARN 이 낫습니다.
func TestPool_NewPoolFromEnv_InvalidValueFallsBack(t *testing.T) {
	t.Setenv("CODEX_AUTH_DIR", makeAuthDir(t))
	for _, raw := range []string{"abc", "0", "-1", "3.5"} {
		t.Run(raw, func(t *testing.T) {
			t.Setenv("CODEX_WORKER_COUNT", raw)
			p, err := codex.NewPoolFromEnv(codexLoader, testLogger())
			require.NoError(t, err)
			assert.Equal(t, 2, p.WorkerCount())
		})
	}
}

// TestPool_NewPoolFromEnv_ExceedsMax_ClampsToCap 는 상한 clamp 를 검증합니다 —
// 컨테이너 누수 / quota 폭발 방지선입니다.
func TestPool_NewPoolFromEnv_ExceedsMax_ClampsToCap(t *testing.T) {
	t.Setenv("CODEX_WORKER_COUNT", "999")
	t.Setenv("CODEX_AUTH_DIR", makeAuthDir(t))

	p, err := codex.NewPoolFromEnv(codexLoader, testLogger())
	require.NoError(t, err)
	assert.Equal(t, 16, p.WorkerCount())
}

// TestPool_NewPoolFromConfig_StagePrefixWins 는 stage prefix 환경변수가 base 보다
// 우선하는지 검증합니다 (이슈 #530 의 stage 별 풀 구성).
func TestPool_NewPoolFromConfig_StagePrefixWins(t *testing.T) {
	t.Setenv("CODEX_AUTH_DIR", makeAuthDir(t))
	t.Setenv("CODEX_WORKER_COUNT", "3")
	t.Setenv("PARSER_CODEX_WORKER_COUNT", "5")

	p, err := codex.NewPoolFromConfig(codex.PoolConfig{Name: "parser"}, codexLoader, testLogger())
	require.NoError(t, err)
	assert.Equal(t, 5, p.WorkerCount())
}

// TestPool_NewPoolFromConfig_FallsBackToBase 는 stage prefix 미설정 시 base 로
// 떨어지는지 검증합니다.
func TestPool_NewPoolFromConfig_FallsBackToBase(t *testing.T) {
	t.Setenv("CODEX_AUTH_DIR", makeAuthDir(t))
	t.Setenv("CODEX_WORKER_COUNT", "3")

	p, err := codex.NewPoolFromConfig(codex.PoolConfig{Name: "enrich"}, codexLoader, testLogger())
	require.NoError(t, err)
	assert.Equal(t, 3, p.WorkerCount())
}

// TestPool_NewPoolFromConfig_StageImageOverride 는 stage prefix 가 worker 수 외의
// 항목 (이미지) 에도 적용되는지 검증합니다.
func TestPool_NewPoolFromConfig_StageImageOverride(t *testing.T) {
	t.Setenv("CODEX_AUTH_DIR", makeAuthDir(t))
	t.Setenv("CODEX_MODEL", "base-model")
	t.Setenv("ENRICH_CODEX_MODEL", "stage-model")

	p, err := codex.NewPoolFromConfig(codex.PoolConfig{Name: "enrich"}, codexLoader, testLogger())
	require.NoError(t, err)
	assert.Equal(t, "stage-model", p.ModelName())
}
