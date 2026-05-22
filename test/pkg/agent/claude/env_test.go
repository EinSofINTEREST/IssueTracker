// stageEnv 헬퍼 + resolveWorkerCount stage prefix 검증 (이슈 #530).
//
// pkg/agent/claude 의 unexported 함수 접근을 위해 _test.go 가 같은 패키지에 위치하지 않고
// test/pkg/agent/claude/ 별도 디렉토리에 있는 본 repo 컨벤션 — 따라서 export 메서드만 검증.
// 핵심 동작은 NewPoolFromConfig 의 env prefix 분기로 검증한다.
package claude_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/pkg/agent/claude"
	"issuetracker/pkg/llm/prompt"
	"issuetracker/pkg/logger"
)

// stubLoader 는 prompt.Loader 의 최소 mock — pool 생성에만 필요, Extract 호출 시점에는 미사용.
type stubLoader struct{}

func (stubLoader) Load(_ string) (string, error)                        { return "test prompt", nil }
func (stubLoader) Render(_ string, _ map[string]string) (string, error) { return "test", nil }

func newTestLogger() *logger.Logger {
	return logger.New(logger.Config{Level: "error"})
}

// TestNewPoolFromConfig_StagePrefix_TakesPrecedence: stage prefix env 가 base env 보다 우선.
//
// 검증: PARSER_CLAUDE_CODE_WORKER_COUNT=3 + CLAUDE_CODE_WORKER_COUNT=5 → pool worker count = 3.
func TestNewPoolFromConfig_StagePrefix_TakesPrecedence(t *testing.T) {
	t.Setenv("CLAUDE_CODE_AUTH_DIR", t.TempDir()) // 인증 디렉토리 fail-fast 회피
	t.Setenv("CLAUDE_CODE_WORKER_COUNT", "5")
	t.Setenv("PARSER_CLAUDE_CODE_WORKER_COUNT", "3")

	pool, err := claude.NewPoolFromConfig(claude.PoolConfig{Name: "parser"}, stubLoader{}, newTestLogger())
	require.NoError(t, err)
	require.NotNil(t, pool)
	assert.Equal(t, 3, pool.WorkerCount(), "PARSER_ prefix env 가 우선 적용되어야 함")
	assert.Equal(t, "parser", pool.Name())
}

// TestNewPoolFromConfig_FallsBackToBase: stage prefix 미설정 시 base env fallback.
func TestNewPoolFromConfig_FallsBackToBase(t *testing.T) {
	t.Setenv("CLAUDE_CODE_AUTH_DIR", t.TempDir())
	t.Setenv("CLAUDE_CODE_WORKER_COUNT", "4")
	// PARSER_CLAUDE_CODE_WORKER_COUNT 명시적으로 미설정

	pool, err := claude.NewPoolFromConfig(claude.PoolConfig{Name: "parser"}, stubLoader{}, newTestLogger())
	require.NoError(t, err)
	assert.Equal(t, 4, pool.WorkerCount(), "stage prefix 미설정 시 base env fallback")
}

// TestNewPoolFromConfig_BothMissing_UsesDefault: 양쪽 모두 미설정 시 default.
func TestNewPoolFromConfig_BothMissing_UsesDefault(t *testing.T) {
	t.Setenv("CLAUDE_CODE_AUTH_DIR", t.TempDir())
	t.Setenv("CLAUDE_CODE_WORKER_COUNT", "")
	t.Setenv("PARSER_CLAUDE_CODE_WORKER_COUNT", "")

	pool, err := claude.NewPoolFromConfig(claude.PoolConfig{Name: "parser"}, stubLoader{}, newTestLogger())
	require.NoError(t, err)
	assert.Equal(t, 2, pool.WorkerCount(), "양쪽 모두 미설정 시 defaultWorkerCount (2)")
}

// TestNewPoolFromConfig_EmptyName_BackwardCompat: Name="" 시 base env 만 lookup (단일 풀 호환).
func TestNewPoolFromConfig_EmptyName_BackwardCompat(t *testing.T) {
	t.Setenv("CLAUDE_CODE_AUTH_DIR", t.TempDir())
	t.Setenv("CLAUDE_CODE_WORKER_COUNT", "6")
	t.Setenv("PARSER_CLAUDE_CODE_WORKER_COUNT", "3") // Name="" 이라 무시되어야 함

	pool, err := claude.NewPoolFromConfig(claude.PoolConfig{Name: ""}, stubLoader{}, newTestLogger())
	require.NoError(t, err)
	assert.Equal(t, 6, pool.WorkerCount(), "Name='' 면 stage prefix 무시, base 만 사용")
	assert.Equal(t, "", pool.Name())
}

// TestNewPoolFromConfig_TwoStages_Independent: 두 stage 가 독립 prefix 사용.
func TestNewPoolFromConfig_TwoStages_Independent(t *testing.T) {
	t.Setenv("CLAUDE_CODE_AUTH_DIR", t.TempDir())
	t.Setenv("CLAUDE_CODE_WORKER_COUNT", "2") // base default
	t.Setenv("PARSER_CLAUDE_CODE_WORKER_COUNT", "3")
	t.Setenv("ENRICH_CLAUDE_CODE_WORKER_COUNT", "5")

	parserPool, err := claude.NewPoolFromConfig(claude.PoolConfig{Name: "parser"}, stubLoader{}, newTestLogger())
	require.NoError(t, err)
	enrichPool, err := claude.NewPoolFromConfig(claude.PoolConfig{Name: "enrich"}, stubLoader{}, newTestLogger())
	require.NoError(t, err)

	assert.Equal(t, 3, parserPool.WorkerCount())
	assert.Equal(t, 5, enrichPool.WorkerCount())
	assert.NotEqual(t, parserPool.Name(), enrichPool.Name())
}

// TestNewPoolFromConfig_ClampUpperBound: maxWorkerCount (16) 초과 시 clamp.
func TestNewPoolFromConfig_ClampUpperBound(t *testing.T) {
	t.Setenv("CLAUDE_CODE_AUTH_DIR", t.TempDir())
	t.Setenv("PARSER_CLAUDE_CODE_WORKER_COUNT", "100")

	pool, err := claude.NewPoolFromConfig(claude.PoolConfig{Name: "parser"}, stubLoader{}, newTestLogger())
	require.NoError(t, err)
	assert.Equal(t, 16, pool.WorkerCount(), "maxWorkerCount (16) 으로 clamp")
}

// TestNewPoolFromConfig_InvalidValue_UsesDefault: parse 실패 / 0 / 음수 → default.
func TestNewPoolFromConfig_InvalidValue_UsesDefault(t *testing.T) {
	cases := []struct {
		name  string
		value string
	}{
		{"non-numeric", "abc"},
		{"zero", "0"},
		{"negative", "-1"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("CLAUDE_CODE_AUTH_DIR", t.TempDir())
			t.Setenv("PARSER_CLAUDE_CODE_WORKER_COUNT", tt.value)
			t.Setenv("CLAUDE_CODE_WORKER_COUNT", "") // base 미설정 — fallback 도 없음

			pool, err := claude.NewPoolFromConfig(claude.PoolConfig{Name: "parser"}, stubLoader{}, newTestLogger())
			require.NoError(t, err)
			assert.Equal(t, 2, pool.WorkerCount(), "%q → default", tt.value)
		})
	}
}

// 컴파일 타임 — stubLoader 가 prompt.Loader 만족하는지 확인.
var _ prompt.Loader = stubLoader{}
