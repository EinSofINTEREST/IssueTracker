// 본 파일은 RunSession (이슈 #447 의 generic session primitive) 흐름을 검증합니다 (이슈 #535).
package codex_test

import (
	"context"
	"os"
	"path/filepath"
	"sort"
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

// TestRunSession_MCPConfig_Rejected 는 MCP 설정이 붙은 경우 **명시적으로 거부**되는지
// 검증합니다 (이슈 #585).
//
// codex 의 `exec` 파서에는 --mcp-config 옵션이 없어, 설정을 조용히 무시하거나 잘못된
// 플래그를 붙이면 프롬프트 실행 전 인자 파싱 단계에서 실패합니다 — 원인 추적이 어려운
// 런타임 실패가 되므로 여기서 먼저 끊습니다.
func TestRunSession_MCPConfig_Rejected(t *testing.T) {
	runner := &mockRunner{execStdout: "ok"}
	w := newStartedWorker(t, runner)
	w.WithMCPConfig(&agentdb.MCPConfig{})

	_, err := w.RunSession(t.Context(), "enrich-extract", nil, "prompt")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "MCP")

	_, exec, _ := runner.counts()
	assert.Zero(t, exec, "지원하지 않는 설정은 exec 전에 끊는다")
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
