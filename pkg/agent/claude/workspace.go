package claude

import (
	"issuetracker/pkg/agent"
	"issuetracker/pkg/logger"
)

// workspacePrefix 는 worker 별 호스트 workspace 디렉토리의 접두사입니다.
const workspacePrefix = "claudegen-workspace-"

// writeWorkspaceOwner 는 workspace 에 현재 PID 를 기록합니다.
//
// 실패해도 치명적이지 않습니다 (고아 정리가 그 디렉토리를 건너뛸 뿐) — 호출자는 경고만 남깁니다.
func writeWorkspaceOwner(dir string) error {
	return agent.WriteWorkspaceOwner(dir)
}

// CleanupOrphanedWorkspaces 는 죽은 프로세스가 남긴 claudegen workspace 를 제거합니다 (이슈 #539).
//
// 판별 로직은 codex 와 공유합니다 (이슈 #656) — 실제 구현은 agent.CleanupOrphanedWorkspaces.
// claudegen workspace 에는 진행 중이던 세션의 .mcp.json (enricher_ro 자격증명, 0600) 이 남을
// 수 있어 방치하면 안 됩니다.
func CleanupOrphanedWorkspaces(log *logger.Logger) int {
	return agent.CleanupOrphanedWorkspaces(workspacePrefix, log)
}
