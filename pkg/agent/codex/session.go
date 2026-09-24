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

	// 이슈 #472 — MCP 설정은 codex backend 에서 **아직 지원하지 않습니다** (CodeRabbit 피드백).
	//
	// 기존 구현은 .mcp.json 을 쓰고 `codex exec --mcp-config <path>` 를 붙였으나, codex 의
	// exec 파서는 그 옵션을 정의하지 않습니다. 따라서 MCP 를 설정한 순간 **프롬프트 실행 전
	// argument parsing 단계에서 세션이 통째로 실패** 합니다. claude backend 의 플래그를
	// 그대로 옮겨 쓴 것이 원인입니다.
	//
	// 올바른 경로는 config.toml 의 [mcp_servers.<name>] 또는 지원되는 -c key=value override
	// 이지만, 정확한 키 구조는 codex CLI 버전에 묶여 있어 검증 없이 추측하지 않습니다.
	// 잘못된 플래그를 그대로 두면 런타임에 원인을 알기 어려운 실패가 나므로, 여기서
	// **명시적으로 거부** 합니다 — 지원은 이슈 #585 에서 다룹니다.
	if w.mcpConfig != nil {
		return "", errors.New(
			"codex: MCP config is not supported by this backend yet " +
				"(codex exec has no --mcp-config option; see issue #585)")
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
