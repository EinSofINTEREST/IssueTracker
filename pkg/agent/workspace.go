package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"issuetracker/pkg/logger"
)

// OwnerFileName 은 workspace 를 만든 프로세스의 PID 를 기록하는 파일입니다.
//
// 정상 종료 경로 (Worker.Stop) 는 workspace 전체를 지우지만, SIGKILL / OOM 으로 프로세스가
// 죽으면 남습니다. PID 를 남겨두면 다음 기동 때 "만든 프로세스가 이미 죽었는가" 로 고아를
// 정확히 판별할 수 있습니다 — 나이(mtime) 기준과 달리 장기 실행 중인 다른 인스턴스의
// workspace 를 오삭제하지 않습니다.
const OwnerFileName = ".owner"

// WriteWorkspaceOwner 는 workspace 에 현재 PID 를 기록합니다.
//
// 실패하면 그 workspace 는 이후 CleanupOrphanedWorkspaces 가 영영 건너뜁니다 — 고아 정리가
// 무력화되므로 호출자는 에러를 무시하지 말아야 합니다.
func WriteWorkspaceOwner(dir string) error {
	path := filepath.Join(dir, OwnerFileName)
	if err := os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		return fmt.Errorf("write workspace owner: %w", err)
	}
	return nil
}

// CleanupOrphanedWorkspaces 는 죽은 프로세스가 남긴 workspace 를 제거합니다 (이슈 #539 / #656).
//
// prefix 는 os.TempDir() 아래에서 찾을 디렉토리 접두사입니다 (예: "claudegen-workspace-").
//
// 판별: workspace 안의 .owner 가 가리키는 PID 가 더 이상 살아있지 않으면 고아.
//   - .owner 가 없으면 건너뜁니다 — 본 기능 도입 이전 버전이 만든 workspace 로, 지금도
//     살아있는 프로세스의 것일 수 있습니다. 소유자를 알 수 없는 디렉토리를 일괄 회수하면
//     그런 legacy workspace 를 실행 중에 지우게 되므로 건너뛰는 편이 안전합니다.
//   - 자기 자신의 PID 는 당연히 살아있으므로 제외됩니다.
//
// 반환: 제거한 디렉토리 수. 에러는 개별 디렉토리 단위로 경고만 남기고 계속 진행 —
// 정리 실패가 기동을 막지 않도록.
func CleanupOrphanedWorkspaces(prefix string, log *logger.Logger) int {
	pattern := filepath.Join(os.TempDir(), prefix+"*")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		if log != nil {
			log.WithField("prefix", prefix).WithError(err).Warn("failed to scan for orphaned workspaces")
		}
		return 0
	}

	removed := 0
	for _, dir := range matches {
		info, statErr := os.Stat(dir)
		if statErr != nil || !info.IsDir() {
			continue
		}

		pid, ok := readWorkspaceOwner(dir)
		if !ok || processAlive(pid) {
			continue
		}

		if rmErr := os.RemoveAll(dir); rmErr != nil {
			if log != nil {
				log.WithFields(map[string]interface{}{"workspace": dir, "owner_pid": pid}).
					WithError(rmErr).Warn("failed to remove orphaned workspace")
			}
			continue
		}
		removed++
		if log != nil {
			log.WithFields(map[string]interface{}{"workspace": dir, "owner_pid": pid}).
				Info("removed orphaned workspace from a dead process")
		}
	}
	return removed
}

// readWorkspaceOwner 는 workspace 의 .owner PID 를 읽습니다.
// 파일 부재 / 파싱 실패 시 ok=false — 호출자는 해당 디렉토리를 건너뜁니다.
func readWorkspaceOwner(dir string) (int, bool) {
	data, err := os.ReadFile(filepath.Join(dir, OwnerFileName))
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

// processAlive 는 pid 프로세스의 생존 여부를 반환합니다.
//
// signal 0 은 실제 시그널을 보내지 않고 존재 + 권한만 확인하는 POSIX 관용구입니다.
// EPERM (존재하나 다른 사용자 소유) 은 살아있는 것으로 간주 — 오삭제 회피.
func processAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	if err = proc.Signal(syscall.Signal(0)); err == nil {
		return true
	}
	return os.IsPermission(err)
}
