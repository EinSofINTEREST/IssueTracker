// 본 파일은 enrich 단계 (이슈 #447) 가 사용할 generic session primitive 를 제공합니다.
//
// ExtractEnriched 는 parser-rule extraction 전용 시그니처 (host/targetType/html →
// llmgen.ExtractResult) 라 enrich 의 다른 prompt / 다른 출력 schema 에는 직접 재사용
// 불가. 본 파일의 RunEnrichSession 은 한 단계 더 generic 한 primitive:
// (files + promptText) → raw stdout. enrich 패키지가 자체 prompt template 을 빌드해서
// 넘기고 자체 출력 schema 로 파싱.
//
// ExtractEnriched 의 session lifecycle 과 의도적으로 분리 — 기존 parser 경로에 무영향.

package claude

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// RunEnrichSession 은 새 세션을 만들어 files 를 세션 디렉토리에 기록한 뒤 promptText 로
// claude code 를 호출하고 stdout 을 반환합니다.
//
// 매개변수:
//   - sessionLabel: 로그·디버그용 라벨 (예: "enrich-extract"). 컨테이너 동작에는 영향 없음.
//   - files: 세션 디렉토리에 기록할 파일들 — filename (확장자 포함) → contents.
//     prompt 안에서 {{SESSION_PATH}}/<filename> 으로 참조됩니다.
//   - promptText: claude code 에 -p 로 전달할 최종 프롬프트 (caller 가 이미 placeholder 치환 완료).
//
// 세션 디렉토리는 호출 종료 시 자동 삭제됩니다 (성공/실패 무관).
//
// 본 메소드는 ExtractEnriched 와 동일하게 wg.Add/Done 으로 진행 중 호출을 추적 — Stop() 의
// wg.Wait() 이 본 호출을 놓치지 않음.
//
// 호출자 역할: stdout 을 자체 schema 로 파싱. RunEnrichSession 은 JSON 파싱 / blacklist 분기
// 등을 일체 수행하지 않습니다 (parser-specific 인 ExtractEnriched 와 다른 점).
func (w *Worker) RunEnrichSession(
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
		return "", errors.New("claude: worker not started — call Start() first")
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
		// name 검증: 호출자가 ".." 같은 path traversal 을 넣지 못하도록 baseName 만 허용.
		if name == "" || name != filepath.Base(name) {
			return "", fmt.Errorf("invalid session file name %q", name)
		}
		if err := os.WriteFile(filepath.Join(sessionHostDir, name), data, 0o644); err != nil {
			return "", fmt.Errorf("write session file %q: %w", name, err)
		}
	}

	runCtx, cancel := context.WithTimeout(ctx, w.sessionTimeout)
	defer cancel()

	args := []string{
		"claude",
		"--model", w.model,
		"-p", promptText,
	}

	w.log.WithFields(map[string]interface{}{
		"session_label": sessionLabel,
		"container_id":  containerID,
		"session_id":    sessionID,
		"file_count":    len(files),
	}).Debug("starting claude code enrich session")

	stdout, stderr, err := w.runner.ExecSession(runCtx, containerID, args)
	if err != nil {
		return "", fmt.Errorf("claude code enrich session: %w (stderr: %s)",
			err, truncate(stderr, truncateStderrLen))
	}

	return stdout, nil
}
