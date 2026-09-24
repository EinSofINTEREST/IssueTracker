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
// 본 메소드는 ExtractEnriched 와 동일하게 admit() 으로 입장 허가를 받습니다 — 상태 확인과
// wg.Add 가 한 임계구역에서 일어나 Stop() 의 wg.Wait() 과 경쟁하지 않습니다 (이슈 #532 리뷰).
//
// 호출자 역할: stdout 을 자체 schema 로 파싱. RunSession 은 JSON 파싱 / blacklist 분기
// 등을 일체 수행하지 않습니다 (parser-specific 인 ExtractEnriched 와 다른 점).
func (w *Worker) RunSession(
	ctx context.Context,
	sessionLabel string,
	files map[string][]byte,
	promptText string,
) (string, error) {
	containerID, workDir, err := w.admit()
	if err != nil {
		return "", err
	}
	defer w.wg.Done()

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

	// 이슈 #585 — MCP 설정은 `-c mcp_servers.<name>...` override 로 전달한다.
	//
	// codex 의 exec 파서에는 --mcp-config 가 없다 (claude 의 .mcp.json 방식과 다른 점).
	// 파일로 넣으려면 $CODEX_HOME/config.toml 에 써야 하는데, 그 경로는 호스트 ~/.codex 가
	// RW 로 마운트된 곳이라 자격증명이 호스트에 영구 기록된다. 그래서 호출 단위로 끝나는
	// override 를 쓴다 — 자세한 근거는 mcp.go 참조.
	//
	// 변환 실패는 세션 시작 전에 끊는다. 잘못된 키/값을 그대로 넘기면 codex 가
	// "invalid transport" 같은 간접적인 메시지를 내 원인 추적이 어렵다.
	mcpArgs, err := mcpOverrideArgs(w.mcpConfig)
	if err != nil {
		return "", err
	}

	runCtx, cancel := context.WithTimeout(ctx, w.sessionTimeout)
	defer cancel()

	// Codex CLI 비대화 모드 — `codex exec`. 권한 / sandbox 플래그는 codex CLI 버전 진화
	// 빈도가 높아 본 PR 골격에서는 지정 안 함. MCP wiring 도 codex CLI 의 mcp 플래그 형식이
	// 안정화된 후 후속 PR 에서 통합 — 본 패치는 transport 골격 유지.
	args := []string{
		"codex", "exec",
		// --skip-git-repo-check: codex 는 기본적으로 git 작업 트리 안에서만 실행을 허용한다.
		// 컨테이너 WORKDIR (/workspace) 은 git repo 가 아니므로 (이미지에 git 도 없다) 이
		// 플래그가 없으면 프롬프트 실행 전에 거부된다 — 이슈 #591.
		"--skip-git-repo-check",
		"--model", w.model,
	}
	args = append(args, mcpArgs...)
	// 프롬프트는 마지막 위치 인자여야 한다 — 뒤에 무엇도 붙이지 않는다.
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
