package codex_test

// workspace_test.go — 이슈 #656: codex 도 고아 workspace 를 정리한다.
//
// claude 에만 정리 로직이 있어 codex 는 SIGKILL / OOM 시 /tmp 에 workspace 를 남겼다.

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/pkg/agent"
	"issuetracker/pkg/agent/codex"
)

const codexDeadPID = 4194303

// newCodexWorkspace 는 codex worker 가 만드는 것과 같은 형태의 디렉토리를 만듭니다.
//
// prefix 를 코드와 맞추는 것이 요점이다 — Start 의 MkdirTemp 패턴과 정리의 glob 이
// 어긋나면 정리가 아무것도 찾지 못한 채 조용히 통과한다.
func newCodexWorkspace(t *testing.T, owner int) string {
	t.Helper()

	dir, err := os.MkdirTemp("", "codex-workspace-*")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	if owner >= 0 {
		require.NoError(t, os.WriteFile(
			filepath.Join(dir, agent.OwnerFileName), []byte(strconv.Itoa(owner)), 0o644))
	}
	// 세션별 페이지 파일 — 이게 쌓이는 것이 누수의 실체다.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "page.html"), []byte("<html>"), 0o644))

	return dir
}

func TestCodexCleanupOrphanedWorkspaces_DeadOwner_Removed(t *testing.T) {
	dir := newCodexWorkspace(t, codexDeadPID)

	removed := codex.CleanupOrphanedWorkspaces(testLogger())

	assert.GreaterOrEqual(t, removed, 1)
	_, err := os.Stat(dir)
	assert.True(t, os.IsNotExist(err), "죽은 프로세스의 codex workspace 가 남았습니다")
}

func TestCodexCleanupOrphanedWorkspaces_LiveOwner_Kept(t *testing.T) {
	dir := newCodexWorkspace(t, os.Getpid())

	codex.CleanupOrphanedWorkspaces(testLogger())

	_, err := os.Stat(dir)
	assert.NoError(t, err, "살아있는 프로세스의 workspace 를 지웠습니다")
}

func TestCodexCleanupOrphanedWorkspaces_NilLogger_DoesNotPanic(t *testing.T) {
	newCodexWorkspace(t, codexDeadPID)

	assert.NotPanics(t, func() { codex.CleanupOrphanedWorkspaces(nil) })
}
