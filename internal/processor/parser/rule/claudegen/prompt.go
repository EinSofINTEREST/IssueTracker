package claudegen

import (
	"fmt"
	"strings"

	"issuetracker/internal/storage"
	"issuetracker/pkg/llm/prompt"
)

// buildPrompt 는 Claude Code 에 전달할 프롬프트를 생성합니다.
// sessionPath 는 컨테이너 내 세션 디렉토리 경로 (예: /workspace/<sessionID>) 입니다.
// Claude Code 는 해당 경로의 page.html 을 파일 읽기 툴로 직접 접근합니다.
//
// rejectReason 은 validator → parser 재학습 cycle 의 reject 사유 (이슈 #365).
//   - 빈 문자열 (정상 경로) → placeholder 가 빈 줄로 치환되어 prompt 영향 무.
//   - 비어있지 않음 (reparse 경로) → 컨텍스트 블록으로 변환되어 prompt 에 삽입.
//     multi-turn agent 가 reason 보고 selector 보강 또는 validity=blacklist 결정 활용.
func buildPrompt(loader prompt.Loader, host string, targetType storage.TargetType, sessionPath, rejectReason string) (string, error) {
	name := promptNameFor(targetType)
	template, err := loader.Load(name)
	if err != nil {
		return "", fmt.Errorf("load claudegen prompt %q: %w", name, err)
	}
	rendered := prompt.Render(template,
		"{{SESSION_PATH}}", sessionPath,
		"{{HOST}}", host,
		"{{TARGET_TYPE}}", string(targetType),
		"{{VALIDATION_REJECT_REASON_CONTEXT}}", formatRejectReasonContext(rejectReason),
	)
	// reason 부재 시 placeholder 가 빈 문자열로 치환됨 → 빈 줄이 남을 수 있어 정리.
	// 양쪽 공백 (특히 빈 줄) 제거하지 않고 단순 trailing 공백만 제거하여 의도된 layout 보존.
	return strings.TrimRight(rendered, " \t\n") + "\n", nil
}

// formatRejectReasonContext 는 reason 텍스트를 prompt 안에 삽입할 컨텍스트 블록으로 변환합니다.
//
// 빈 문자열이면 빈 문자열 반환 — placeholder 가 prompt 에 영향 없게.
// 그 외에는 "IMPORTANT — Validation feedback from previous attempt: ..." 형식의 블록 반환.
func formatRejectReasonContext(reason string) string {
	if reason == "" {
		return ""
	}
	return fmt.Sprintf(`
IMPORTANT — Validation feedback from previous attempt:
The validator rejected the previously extracted content for the following reason(s):
%s

Use this context to improve your selector extraction. If the page genuinely has
no valid content for the required fields (e.g., it's a meta/index page without
article body or published_at metadata), return validity="blacklist".`, reason)
}

func promptNameFor(targetType storage.TargetType) string {
	if targetType == storage.TargetTypeList {
		return "claudegen/list.user"
	}
	return "claudegen/page.user"
}
