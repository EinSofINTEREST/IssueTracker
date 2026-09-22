package claude_test

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/pkg/agent/claude"
	"issuetracker/pkg/logger"
)

func testLog() *logger.Logger { return logger.New(logger.DefaultConfig()) }

// newWorkspace 는 TempDir 안에 claudegen workspace 를 흉내 낸 디렉토리를 만듭니다.
// owner 가 음수면 .owner 파일을 만들지 않습니다.
func newWorkspace(t *testing.T, owner int) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "claudegen-workspace-*")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })

	if owner >= 0 {
		require.NoError(t, os.WriteFile(
			filepath.Join(dir, ".owner"), []byte(strconv.Itoa(owner)), 0o644))
	}
	// 세션 잔여물 — 정리 대상에 함께 포함되는지 확인용
	require.NoError(t, os.WriteFile(filepath.Join(dir, "leftover.json"), []byte("{}"), 0o600))
	return dir
}

func TestCleanupOrphanedWorkspaces_DeadOwner_Removed(t *testing.T) {
	// PID 1 은 init — 살아있으므로 쓸 수 없다. 존재할 가능성이 없는 큰 PID 를 사용.
	dead := newWorkspace(t, 4194303)

	claude.CleanupOrphanedWorkspaces(testLog())

	_, err := os.Stat(dead)
	assert.True(t, os.IsNotExist(err), "죽은 프로세스의 workspace 는 제거되어야 함")
}

func TestCleanupOrphanedWorkspaces_LiveOwner_Kept(t *testing.T) {
	live := newWorkspace(t, os.Getpid())

	claude.CleanupOrphanedWorkspaces(testLog())

	_, err := os.Stat(live)
	assert.NoError(t, err, "살아있는 프로세스의 workspace 는 보존되어야 함")
}

// .owner 가 없으면 판별 불가 — 살아있는 것을 지우는 위험보다 남기는 쪽이 안전.
func TestCleanupOrphanedWorkspaces_NoOwnerFile_Kept(t *testing.T) {
	unknown := newWorkspace(t, -1)

	claude.CleanupOrphanedWorkspaces(testLog())

	_, err := os.Stat(unknown)
	assert.NoError(t, err, ".owner 부재 시 보존되어야 함")
}

func TestCleanupOrphanedWorkspaces_MalformedOwner_Kept(t *testing.T) {
	dir, err := os.MkdirTemp("", "claudegen-workspace-*")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".owner"), []byte("not-a-pid"), 0o644))

	claude.CleanupOrphanedWorkspaces(testLog())

	_, statErr := os.Stat(dir)
	assert.NoError(t, statErr, "손상된 .owner 는 보존되어야 함")
}

func TestCleanupOrphanedWorkspaces_NilLogger_DoesNotPanic(t *testing.T) {
	newWorkspace(t, 4194303)
	assert.NotPanics(t, func() { claude.CleanupOrphanedWorkspaces(nil) })
}

// .owner 기록 실패는 기동을 중단시켜야 한다 (CodeRabbit 피드백).
//
// 경고만 남기고 진행하면, 이후 비정상 종료 시 자격증명이 담긴 workspace 에 .owner 가 없어
// CleanupOrphanedWorkspaces 가 영영 건너뛴다 — 고아 정리 기능이 무력화된다.
func TestCleanupOrphanedWorkspaces_OwnerlessDir_IsPreserved(t *testing.T) {
	// 도입 이전 버전이 만든 workspace 를 흉내 — 살아있는 프로세스의 것일 수 있으므로 보존.
	legacy := newWorkspace(t, -1)

	claude.CleanupOrphanedWorkspaces(testLog())

	_, err := os.Stat(legacy)
	assert.NoError(t, err, "소유자를 알 수 없는 legacy workspace 는 일괄 회수하지 않아야 함")
}
