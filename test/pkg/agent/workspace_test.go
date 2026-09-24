package agent_test

// workspace_test.go — 이슈 #656: claude / codex 가 공유하는 고아 workspace 정리.

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/pkg/agent"
	"issuetracker/pkg/logger"
)

// deadPID: 존재할 가능성이 없는 PID. PID 1 은 init 이라 살아있어 쓸 수 없다.
const deadPID = 4194303

func testLog() *logger.Logger { return logger.New(logger.DefaultConfig()) }

// newWorkspaceWithPrefix 는 TempDir 안에 workspace 를 흉내 낸 디렉토리를 만듭니다.
// owner 가 음수면 .owner 파일을 만들지 않습니다.
func newWorkspaceWithPrefix(t *testing.T, prefix string, owner int) string {
	t.Helper()

	dir, err := os.MkdirTemp("", prefix+"*")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	if owner >= 0 {
		require.NoError(t, os.WriteFile(
			filepath.Join(dir, agent.OwnerFileName), []byte(strconv.Itoa(owner)), 0o644))
	}
	// 세션 잔여물 — 디렉토리째 지워지는지 확인용
	require.NoError(t, os.WriteFile(filepath.Join(dir, "leftover.html"), []byte("<html>"), 0o644))

	return dir
}

// 테스트 전용 prefix 를 써서 claude / codex 실제 workspace 와 간섭하지 않는다.
const testPrefix = "agent-workspace-test-"

func TestCleanupOrphanedWorkspaces_DeadOwner_Removed(t *testing.T) {
	dir := newWorkspaceWithPrefix(t, testPrefix, deadPID)

	removed := agent.CleanupOrphanedWorkspaces(testPrefix, testLog())

	assert.GreaterOrEqual(t, removed, 1)
	_, err := os.Stat(dir)
	assert.True(t, os.IsNotExist(err), "죽은 프로세스의 workspace 는 제거되어야 합니다")
}

func TestCleanupOrphanedWorkspaces_LiveOwner_Kept(t *testing.T) {
	dir := newWorkspaceWithPrefix(t, testPrefix, os.Getpid())

	agent.CleanupOrphanedWorkspaces(testPrefix, testLog())

	_, err := os.Stat(dir)
	assert.NoError(t, err, "살아있는 프로세스의 workspace 는 보존되어야 합니다")
}

// .owner 가 없으면 판별 불가 — 살아있는 것을 지우는 위험보다 남기는 쪽이 안전하다.
func TestCleanupOrphanedWorkspaces_NoOwnerFile_Kept(t *testing.T) {
	dir := newWorkspaceWithPrefix(t, testPrefix, -1)

	agent.CleanupOrphanedWorkspaces(testPrefix, testLog())

	_, err := os.Stat(dir)
	assert.NoError(t, err, ".owner 부재 시 보존되어야 합니다")
}

func TestCleanupOrphanedWorkspaces_MalformedOwner_Kept(t *testing.T) {
	dir := newWorkspaceWithPrefix(t, testPrefix, -1)
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, agent.OwnerFileName), []byte("not-a-pid"), 0o644))

	agent.CleanupOrphanedWorkspaces(testPrefix, testLog())

	_, err := os.Stat(dir)
	assert.NoError(t, err, "파싱 불가한 .owner 는 보존되어야 합니다")
}

// prefix 가 다른 디렉토리는 건드리면 안 된다 — claude 정리가 codex 것을 지우거나 그 반대.
func TestCleanupOrphanedWorkspaces_OtherPrefix_Untouched(t *testing.T) {
	other := newWorkspaceWithPrefix(t, "agent-workspace-other-", deadPID)

	agent.CleanupOrphanedWorkspaces(testPrefix, testLog())

	_, err := os.Stat(other)
	assert.NoError(t, err, "다른 prefix 의 workspace 를 지웠습니다")
}

func TestCleanupOrphanedWorkspaces_NilLogger_DoesNotPanic(t *testing.T) {
	newWorkspaceWithPrefix(t, testPrefix, deadPID)

	assert.NotPanics(t, func() { agent.CleanupOrphanedWorkspaces(testPrefix, nil) })
}

// ─────────────────────────────────────────────────────────────────────────────
// WriteWorkspaceOwner
// ─────────────────────────────────────────────────────────────────────────────

func TestWriteWorkspaceOwner_WritesCurrentPID(t *testing.T) {
	dir := t.TempDir()

	require.NoError(t, agent.WriteWorkspaceOwner(dir))

	data, err := os.ReadFile(filepath.Join(dir, agent.OwnerFileName))
	require.NoError(t, err)
	assert.Equal(t, strconv.Itoa(os.Getpid()), string(data))
}

// 기록에 실패하면 호출자가 기동을 중단해야 한다 — 조용히 넘기면 고아 정리가 영영 건너뛴다.
func TestWriteWorkspaceOwner_MissingDir_ReturnsError(t *testing.T) {
	err := agent.WriteWorkspaceOwner(filepath.Join(t.TempDir(), "does-not-exist"))

	require.Error(t, err)
}
