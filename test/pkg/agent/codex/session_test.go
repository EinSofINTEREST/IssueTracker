// 본 파일은 RunSession (이슈 #447 의 generic session primitive) 흐름을 검증합니다 (이슈 #535).
package codex_test

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentdb "issuetracker/pkg/agent/dependency/db"
)

// TestRunSession_WritesFilesAndReturnsStdout 은 세션 디렉토리에 파일이 기록되고
// stdout 이 가공 없이 반환되는지 검증합니다.
//
// RunSession 은 ExtractEnriched 와 달리 JSON 파싱 / blacklist 분기를 하지 않습니다 —
// 호출자(enrich)가 자체 schema 로 파싱하는 것이 설계 의도입니다.
func TestRunSession_WritesFilesAndReturnsStdout(t *testing.T) {
	var seen []string
	var sessionDir string

	runner := &mockRunner{execStdout: "raw output, not JSON"}
	runner.onExec = func(_ string, _ []string) {
		// exec 시점에는 세션 디렉토리가 아직 살아 있다 (RunSession 이 defer 로 지우기 전).
		_, workDir, _, _ := runner.started()
		entries, err := os.ReadDir(workDir)
		if err != nil || len(entries) != 1 {
			return
		}
		sessionDir = filepath.Join(workDir, entries[0].Name())
		files, err := os.ReadDir(sessionDir)
		if err != nil {
			return
		}
		for _, f := range files {
			seen = append(seen, f.Name())
		}
	}

	w := newStartedWorker(t, runner)

	out, err := w.RunSession(t.Context(), "enrich-extract", map[string][]byte{
		"article.txt":  []byte("body"),
		"context.json": []byte(`{"k":"v"}`),
	}, "summarize {{SESSION_PATH}}")
	require.NoError(t, err)
	assert.Equal(t, "raw output, not JSON", out, "stdout 은 가공 없이 그대로 반환된다")

	sort.Strings(seen)
	assert.Equal(t, []string{"article.txt", "context.json"}, seen)

	// 종료 후 세션 디렉토리는 정리된다.
	require.NotEmpty(t, sessionDir)
	_, statErr := os.Stat(sessionDir)
	assert.True(t, os.IsNotExist(statErr), "세션 디렉토리는 호출 종료 시 삭제된다")
}

// TestRunSession_EmptyFiles_Allowed 는 파일 없이도 세션이 성립하는지 검증합니다 —
// prompt 만으로 완결되는 호출 (예: 컨텍스트 요약) 을 막지 않아야 합니다.
func TestRunSession_EmptyFiles_Allowed(t *testing.T) {
	runner := &mockRunner{execStdout: "ok"}
	w := newStartedWorker(t, runner)

	out, err := w.RunSession(t.Context(), "enrich-score", nil, "just a prompt")
	require.NoError(t, err)
	assert.Equal(t, "ok", out)
}

// TestRunSession_ExecArgs 는 프롬프트가 마지막 위치 인자로 전달되는지 검증합니다.
func TestRunSession_ExecArgs(t *testing.T) {
	runner := &mockRunner{execStdout: "ok"}
	w := newStartedWorker(t, runner)

	_, err := w.RunSession(t.Context(), "enrich-verify", nil, "PROMPT-BODY")
	require.NoError(t, err)

	args := runner.execArgs()
	require.GreaterOrEqual(t, len(args), 6)
	assert.Equal(t, []string{"codex", "exec", "--skip-git-repo-check", "--model", "gpt-5-codex"}, args[:5],
		"--skip-git-repo-check 가 빠지면 codex 가 프롬프트 실행 전에 거부한다 (이슈 #591)")
	assert.Equal(t, "PROMPT-BODY", args[len(args)-1])
}

// TestRunSession_RejectsPathTraversalNames 는 파일명에 경로 요소가 섞이면 거부되는지
// 검증합니다 — 세션 디렉토리 밖으로 쓰기를 막는 방어선입니다.
func TestRunSession_RejectsPathTraversalNames(t *testing.T) {
	for _, name := range []string{"..", ".", "", "../escape.txt", "sub/dir.txt", `win\path.txt`} {
		t.Run(name, func(t *testing.T) {
			runner := &mockRunner{execStdout: "ok"}
			w := newStartedWorker(t, runner)

			_, err := w.RunSession(t.Context(), "enrich-extract",
				map[string][]byte{name: []byte("x")}, "prompt")
			require.Error(t, err)
			assert.Contains(t, err.Error(), "invalid session file name")

			_, exec, _ := runner.counts()
			assert.Zero(t, exec, "파일명 검증 실패 시 exec 까지 가지 않는다")
		})
	}
}

// TestRunSession_MCPConfig_PassedAsOverrides 는 MCP 설정이 `-c` override 로 전달되는지
// 검증합니다 (이슈 #585).
//
// 과거에는 이 경로가 명시적 에러였다 — codex 의 exec 파서에 --mcp-config 가 없어
// claude 의 플래그를 그대로 쓰면 인자 파싱 단계에서 세션이 통째로 실패했기 때문이다.
// 지원 경로를 실측으로 확인한 뒤 전달로 바꿨다.
func TestRunSession_MCPConfig_PassedAsOverrides(t *testing.T) {
	runner := &mockRunner{execStdout: "ok"}
	w := newStartedWorker(t, runner)
	w.WithMCPConfig(&agentdb.MCPConfig{
		MCPServers: map[string]agentdb.MCPServerConfig{
			"issuetracker_ro": {
				Command: "npx",
				Args:    []string{"-y", "@modelcontextprotocol/server-postgres"},
				Env:     map[string]string{"PG_DSN": "postgres://ro:pw@db:5432/x"},
			},
		},
	})

	_, err := w.RunSession(t.Context(), "enrich-extract", nil, "PROMPT")
	require.NoError(t, err)

	args := runner.execArgs()
	joined := strings.Join(args, "\n")
	assert.Contains(t, joined, `mcp_servers.issuetracker_ro.command="npx"`)
	assert.Contains(t, joined, `mcp_servers.issuetracker_ro.args=["-y","@modelcontextprotocol/server-postgres"]`)
	assert.Contains(t, joined, `mcp_servers.issuetracker_ro.env.PG_DSN="postgres://ro:pw@db:5432/x"`)

	// 프롬프트는 여전히 마지막 위치 인자여야 한다 — override 가 뒤에 붙으면 codex 가
	// 프롬프트를 인자로 읽지 못한다.
	assert.Equal(t, "PROMPT", args[len(args)-1])
}

// TestRunSession_MCPConfig_Deterministic 은 같은 설정이 항상 같은 인자를 만드는지
// 검증합니다 — map 순회 순서가 새면 진단이 어려워지고 테스트도 불안정해진다.
func TestRunSession_MCPConfig_Deterministic(t *testing.T) {
	cfg := &agentdb.MCPConfig{
		MCPServers: map[string]agentdb.MCPServerConfig{
			"b_server": {Command: "cmd-b", Env: map[string]string{"Z": "1", "A": "2", "M": "3"}},
			"a_server": {Command: "cmd-a"},
		},
	}

	var first []string
	for i := 0; i < 5; i++ {
		runner := &mockRunner{execStdout: "ok"}
		w := newStartedWorker(t, runner)
		w.WithMCPConfig(cfg)
		_, err := w.RunSession(t.Context(), "enrich-extract", nil, "P")
		require.NoError(t, err)

		got := runner.execArgs()
		if first == nil {
			first = got
			continue
		}
		assert.Equal(t, first, got, "같은 설정이 매번 같은 인자를 만들어야 한다")
	}

	joined := strings.Join(first, "\n")
	assert.Less(t, strings.Index(joined, "a_server"), strings.Index(joined, "b_server"),
		"서버 이름이 정렬돼야 한다")
	assert.Less(t, strings.Index(joined, "env.A"), strings.Index(joined, "env.M"),
		"env 키가 정렬돼야 한다")
}

// TestRunSession_MCPConfig_Invalid 는 잘못된 설정이 **세션 시작 전에** 거부되는지
// 검증합니다. 그대로 넘기면 codex 가 "invalid transport" 같은 간접적인 메시지를 내
// 원인 추적이 어렵다.
func TestRunSession_MCPConfig_Invalid(t *testing.T) {
	tests := []struct {
		name string
		cfg  *agentdb.MCPConfig
		want string
	}{
		{
			name: "command 누락",
			cfg: &agentdb.MCPConfig{MCPServers: map[string]agentdb.MCPServerConfig{
				"ro": {Args: []string{"-y"}},
			}},
			want: "no command",
		},
		{
			name: "서버 이름에 점",
			cfg: &agentdb.MCPConfig{MCPServers: map[string]agentdb.MCPServerConfig{
				"a.b": {Command: "npx"},
			}},
			want: "unsupported character",
		},
		{
			name: "env 키에 공백",
			cfg: &agentdb.MCPConfig{MCPServers: map[string]agentdb.MCPServerConfig{
				"ro": {Command: "npx", Env: map[string]string{"A B": "1"}},
			}},
			want: "unsupported character",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &mockRunner{execStdout: "ok"}
			w := newStartedWorker(t, runner)
			w.WithMCPConfig(tt.cfg)

			_, err := w.RunSession(t.Context(), "enrich-extract", nil, "P")
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)

			_, exec, _ := runner.counts()
			assert.Zero(t, exec, "잘못된 설정은 exec 전에 끊는다")
		})
	}
}

// TestRunSession_MCPConfig_QuotesValues 는 따옴표/역슬래시가 섞인 값이 TOML 문자열로
// 안전하게 인용되는지 검증합니다.
//
// 인용이 깨지면 codex 는 "TOML 파싱 실패 시 raw 문자열로 취급" 경로로 떨어져 **에러 없이**
// 값이 격하된다. 조용한 격하는 나중에 원인을 찾기 어렵다.
func TestRunSession_MCPConfig_QuotesValues(t *testing.T) {
	runner := &mockRunner{execStdout: "ok"}
	w := newStartedWorker(t, runner)
	w.WithMCPConfig(&agentdb.MCPConfig{
		MCPServers: map[string]agentdb.MCPServerConfig{
			"ro": {Command: `pa"th\x`, Env: map[string]string{"K": "line1\nline2"}},
		},
	})

	_, err := w.RunSession(t.Context(), "enrich-extract", nil, "P")
	require.NoError(t, err)

	joined := strings.Join(runner.execArgs(), "\n")
	assert.Contains(t, joined, `mcp_servers.ro.command="pa\"th\\x"`)
	assert.Contains(t, joined, `mcp_servers.ro.env.K="line1\nline2"`)
}

// TestRunSession_BeforeStart_Fails 는 기동 전 호출이 거부되는지 검증합니다.
func TestRunSession_BeforeStart_Fails(t *testing.T) {
	w := newWorker(t, makeAuthDir(t), &mockRunner{})

	_, err := w.RunSession(t.Context(), "enrich-extract", nil, "prompt")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not started")
}

// TestRunSession_Timeout 은 세션 타임아웃이 exec 를 끊는지 검증합니다.
func TestRunSession_Timeout(t *testing.T) {
	runner := &mockRunner{execBlocks: true}
	w := shortTimeoutWorker(t, runner)
	require.NoError(t, w.Start(t.Context()))
	t.Cleanup(func() { _ = w.Stop(context.Background()) })

	start := time.Now()
	_, err := w.RunSession(t.Context(), "enrich-extract", nil, "prompt")
	require.Error(t, err)
	assert.Less(t, time.Since(start), 2*time.Second)
}
