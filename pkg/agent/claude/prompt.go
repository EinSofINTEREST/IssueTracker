package claude

import (
	"errors"
	"fmt"
	"strings"

	"issuetracker/internal/storage/model"
	"issuetracker/pkg/llm/prompt"
)

// rejectReasonPlaceholder 는 validation reason 블록이 삽입될 prompt placeholder 토큰입니다.
const rejectReasonPlaceholder = "{{VALIDATION_REJECT_REASON_CONTEXT}}"

// ErrRejectReasonPlaceholderMissing 은 reparse 경로에서 prompt 템플릿이 placeholder 를
// 가지지 않을 때 발생합니다 (Copilot 반영 PR #368).
//
// 운영자가 LLM_PROMPT_DIR 로 외부 prompt 를 override 했는데 본 placeholder 를 추가하지 않은
// 경우 — reason 블록이 silently drop 되어 reparse cycle 이 reason 없이 LLM 을 호출하는
// 잠재 버그. fail-fast 로 운영자에게 즉시 알림.
var ErrRejectReasonPlaceholderMissing = errors.New("claude: prompt template missing " + rejectReasonPlaceholder + " placeholder (LLM_PROMPT_DIR override may need update)")

// buildPrompt 는 Claude Code 에 전달할 프롬프트를 생성합니다.
// sessionPath 는 컨테이너 내 세션 디렉토리 경로 (예: /workspace/<sessionID>) 입니다.
// Claude Code 는 해당 경로의 page.html 을 파일 읽기 툴로 직접 접근합니다.
//
// rejectReason 은 validator → parser 재학습 cycle 의 reject 사유 (이슈 #365).
//   - 빈 문자열 (정상 경로) → placeholder 가 빈 문자열로 치환. 템플릿이 placeholder 미포함이어도 무영향.
//   - 비어있지 않음 (reparse 경로) → 컨텍스트 블록으로 변환되어 prompt 에 삽입.
//     템플릿에 placeholder 가 없으면 ErrRejectReasonPlaceholderMissing 반환 (fail-fast).
func buildPrompt(loader prompt.Loader, host string, targetType model.TargetType, sessionPath, rejectReason string) (string, error) {
	name := promptNameFor(targetType)
	template, err := loader.Load(name)
	if err != nil {
		return "", fmt.Errorf("load claude prompt %q: %w", name, err)
	}

	// reparse 경로 인데 템플릿에 placeholder 미포함 — silent drop 위험 차단.
	// 정상 경로 (reason 빈 문자열) 에서는 placeholder 부재여도 결과 동일하므로 검증 skip.
	if rejectReason != "" && !strings.Contains(template, rejectReasonPlaceholder) {
		return "", fmt.Errorf("build claude prompt %q: %w", name, ErrRejectReasonPlaceholderMissing)
	}

	return prompt.Render(template,
		"{{SESSION_PATH}}", sessionPath,
		"{{HOST}}", host,
		"{{TARGET_TYPE}}", string(targetType),
		rejectReasonPlaceholder, formatRejectReasonContext(rejectReason),
	), nil
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

func promptNameFor(targetType model.TargetType) string {
	if targetType == model.TargetTypeList {
		return "claudegen/list.user"
	}
	return "claudegen/page.user"
}
