// 본 파일은 enrich 단계 (이슈 #447) 가 사용할 generic session primitive 를 제공합니다.
//
// ExtractEnriched 는 parser-rule extraction 전용 시그니처 (host/targetType/html →
// llmgen.ExtractResult) 라 enrich 의 다른 prompt / 다른 출력 schema 에는 직접 재사용
// 불가. 본 파일의 RunSession 은 한 단계 더 generic 한 primitive:
// (files + promptText) → raw stdout. enrich 패키지가 자체 prompt template 을 빌드해서
// 넘기고 자체 출력 schema 로 파싱.
//
// ExtractEnriched 의 session lifecycle 과 의도적으로 분리 — 기존 parser 경로에 무영향.

package codex

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// RunSession 은 새 세션을 만들어 files 를 세션 디렉토리에 기록한 뒤 promptText 로
// codex 를 호출하고 stdout 을 반환합니다.
//
// 매개변수:
//   - sessionLabel: 로그·디버그용 라벨 (예: "enrich-extract"). 컨테이너 동작에는 영향 없음.
//   - files: 세션 디렉토리에 기록할 파일들 — filename (확장자 포함) → contents.
//     prompt 안에서 {{SESSION_PATH}}/<filename> 으로 참조됩니다.
//   - promptText: codex 에 -p 로 전달할 최종 프롬프트 (caller 가 이미 placeholder 치환 완료).
//
// 세션 디렉토리는 호출 종료 시 자동 삭제됩니다 (성공/실패 무관).
//
// 본 메소드는 ExtractEnriched 와 동일하게 wg.Add/Done 으로 진행 중 호출을 추적 — Stop() 의
// wg.Wait() 이 본 호출을 놓치지 않음.
//
// 호출자 역할: stdout 을 자체 schema 로 파싱. RunSession 은 JSON 파싱 / blacklist 분기
// 등을 일체 수행하지 않습니다 (parser-specific 인 ExtractEnriched 와 다른 점).
func (w *Worker) RunSession(
	ctx context.Context,
	sessionLabel string,
	files map[string][]byte,
	promptText string,
) (string, error) {
	w.wg.Add(1)
	defer w.wg.Done()

	w.mu.RLock()
	containerID := w.containerID
	workDir := w.workDir
	w.mu.RUnlock()

	if containerID == "" {
		return "", errors.New("codex: worker not started — call Start() first")
	}

	sessionID, err := newSessionID()
	if err != nil {
		return "", fmt.Errorf("generate session id: %w", err)
	}

	sessionHostDir := filepath.Join(workDir, sessionID)
	if err := os.MkdirAll(sessionHostDir, 0o755); err != nil {
		return "", fmt.Errorf("create session dir: %w", err)
	}
	defer os.RemoveAll(sessionHostDir)

	for name, data := range files {
		// name 검증 (gemini #3321915441 — Security-High):
		// filepath.Base("..") / filepath.Base(".") 는 자기 자신 반환 → 기존 baseName 비교만으로는 부족.
		// "." / ".." / 경로 구분자 (/ 또는 \) 포함 모두 명시 거부.
		if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\") {
			return "", fmt.Errorf("invalid session file name %q", name)
		}
		if err := os.WriteFile(filepath.Join(sessionHostDir, name), data, 0o644); err != nil {
			return "", fmt.Errorf("write session file %q: %w", name, err)
		}
	}

	// 이슈 #472 — MCP 설정이 등록되어 있으면 세션 디렉토리에 .mcp.json 작성. session 종료 시
	// sessionHostDir 전체가 삭제되므로 자격증명도 함께 사라짐.
	var mcpConfigContainerPath string
	if w.mcpConfig != nil {
		mcpBytes, err := w.mcpConfig.Marshal()
		if err != nil {
			return "", fmt.Errorf("marshal mcp config: %w", err)
		}
		// 0o600 — 자격증명 포함 파일이므로 user-only.
		mcpHostPath := filepath.Join(sessionHostDir, ".mcp.json")
		if err := os.WriteFile(mcpHostPath, mcpBytes, 0o600); err != nil {
			return "", fmt.Errorf("write mcp config: %w", err)
		}
		// 컨테이너 내부 경로 — workDir 이 /workspace 로 마운트되므로 sessionID 만 join.
		// 컨테이너는 Linux 라 항상 '/' separator 사용 — filepath.Join 은 호스트 separator
		// (Windows 의 '\') 를 쓸 수 있어 부적합 (gemini-review PR #473).
		mcpConfigContainerPath = "/workspace/" + sessionID + "/.mcp.json"
	}

	runCtx, cancel := context.WithTimeout(ctx, w.sessionTimeout)
	defer cancel()

	// Codex CLI 비대화 모드 — `codex exec`. 권한 / sandbox 플래그는 codex CLI 버전 진화
	// 빈도가 높아 본 PR 골격에서는 지정 안 함. MCP wiring 도 codex CLI 의 mcp 플래그 형식이
	// 안정화된 후 후속 PR 에서 통합 — 본 패치는 transport 골격 유지.
	args := []string{
		"codex", "exec",
		"--model", w.model,
	}
	if mcpConfigContainerPath != "" {
		// 본 플래그 이름은 codex CLI 최신 버전 확인 후 조정 — 운영 검증 단계 책임.
		args = append(args, "--mcp-config", mcpConfigContainerPath)
	}
	args = append(args, promptText)

	w.log.WithFields(map[string]interface{}{
		"session_label": sessionLabel,
		"container_id":  containerID,
		"session_id":    sessionID,
		"file_count":    len(files),
	}).Debug("starting codex enrich session")

	stdout, stderr, err := w.runner.ExecSession(runCtx, containerID, args)
	if err != nil {
		return "", fmt.Errorf("codex enrich session: %w (stderr: %s)",
			err, truncate(stderr, truncateStderrLen))
	}

	return stdout, nil
}
