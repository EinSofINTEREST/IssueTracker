package codex

import (
	"issuetracker/pkg/agent"
	"issuetracker/pkg/logger"
)

// workspacePrefix 는 worker 별 호스트 workspace 디렉토리의 접두사입니다.
//
// Start 의 os.MkdirTemp("", workspacePrefix+"*") 와 짝을 이룹니다 — 한쪽만 바꾸면
// 고아 정리가 아무것도 찾지 못한 채 조용히 통과합니다.
const workspacePrefix = "codex-workspace-"

// CleanupOrphanedWorkspaces 는 죽은 프로세스가 남긴 codex workspace 를 제거합니다 (이슈 #656).
//
// claude 와 달리 codex workspace 에는 **자격증명이 없습니다** — MCP 설정을 파일이 아니라
// `-c` CLI override 로 넘기기 때문입니다 (이슈 #585). 남는 것은 세션별 페이지 파일이므로
// 보안이 아니라 **디스크 / tmpfs 누수** 가 정리의 이유입니다.
//
// 판별 로직은 claude 와 공유합니다 — agent.CleanupOrphanedWorkspaces.
func CleanupOrphanedWorkspaces(log *logger.Logger) int {
	return agent.CleanupOrphanedWorkspaces(workspacePrefix, log)
}
