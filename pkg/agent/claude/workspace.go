package claude

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"issuetracker/pkg/logger"
)

const (
	// workspacePrefix 는 worker 별 호스트 workspace 디렉토리의 접두사입니다.
	workspacePrefix = "claudegen-workspace-"

	// ownerFileName 은 workspace 를 만든 프로세스의 PID 를 기록하는 파일입니다.
	//
	// 정상 종료 경로 (Worker.Stop) 는 workspace 전체를 지우지만, SIGKILL / OOM 으로 프로세스가
	// 죽으면 남습니다. 그 안에는 진행 중이던 세션의 .mcp.json (enricher_ro 자격증명, 0600) 이
	// 함께 남을 수 있어 방치하면 안 됩니다. PID 를 남겨두면 다음 기동 때 "만든 프로세스가 이미
	// 죽었는가" 로 고아를 정확히 판별할 수 있습니다 — 나이(mtime) 기준과 달리 장기 실행 중인
	// 다른 인스턴스의 workspace 를 오삭제하지 않습니다.
	ownerFileName = ".owner"
)

// writeWorkspaceOwner 는 workspace 에 현재 PID 를 기록합니다.
//
// 실패해도 치명적이지 않습니다 (고아 정리가 그 디렉토리를 건너뛸 뿐) — 호출자는 경고만 남깁니다.
func writeWorkspaceOwner(dir string) error {
	path := filepath.Join(dir, ownerFileName)
	if err := os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		return fmt.Errorf("write workspace owner: %w", err)
	}
	return nil
}

// CleanupOrphanedWorkspaces 는 죽은 프로세스가 남긴 claudegen workspace 를 제거합니다 (이슈 #539).
//
// 판별: workspace 안의 .owner 가 가리키는 PID 가 더 이상 살아있지 않으면 고아.
//   - .owner 가 없으면 건너뜁니다 — 본 기능 도입 이전 버전이 만든 것이거나 기록에 실패한 경우로,
//     살아있는 workspace 를 지우는 위험보다 남기는 쪽이 안전합니다.
//   - 자기 자신의 PID 는 당연히 살아있으므로 제외됩니다.
//
// 반환: 제거한 디렉토리 수. 에러는 개별 디렉토리 단위로 경고만 남기고 계속 진행 —
// 정리 실패가 기동을 막지 않도록.
func CleanupOrphanedWorkspaces(log *logger.Logger) int {
	pattern := filepath.Join(os.TempDir(), workspacePrefix+"*")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		if log != nil {
			log.WithError(err).Warn("failed to scan for orphaned claudegen workspaces")
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
					WithError(rmErr).Warn("failed to remove orphaned claudegen workspace")
			}
			continue
		}
		removed++
		if log != nil {
			log.WithFields(map[string]interface{}{"workspace": dir, "owner_pid": pid}).
				Info("removed orphaned claudegen workspace from a dead process")
		}
	}
	return removed
}

// readWorkspaceOwner 는 workspace 의 .owner PID 를 읽습니다.
// 파일 부재 / 파싱 실패 시 ok=false — 호출자는 해당 디렉토리를 건너뜁니다.
func readWorkspaceOwner(dir string) (int, bool) {
	data, err := os.ReadFile(filepath.Join(dir, ownerFileName))
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
	err = proc.Signal(syscall.Signal(0))
	if err == nil {
		return true
	}
	return errorsIsPermission(err)
}

// errorsIsPermission 은 EPERM 여부를 판별합니다 (os.IsPermission 은 syscall.Errno 도 처리).
func errorsIsPermission(err error) bool {
	return os.IsPermission(err)
}
