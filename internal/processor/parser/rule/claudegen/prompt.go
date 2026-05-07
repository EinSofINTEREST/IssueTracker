package claudegen

import (
	"fmt"

	"issuetracker/internal/storage"
	"issuetracker/pkg/llm/prompt"
)

// buildPrompt 는 Claude Code 에 전달할 프롬프트를 생성합니다.
// sessionPath 는 컨테이너 내 세션 디렉토리 경로 (예: /workspace/<sessionID>) 입니다.
// Claude Code 는 해당 경로의 page.html 을 파일 읽기 툴로 직접 접근합니다.
func buildPrompt(loader prompt.Loader, host string, targetType storage.TargetType, sessionPath string) (string, error) {
	name := promptNameFor(targetType)
	template, err := loader.Load(name)
	if err != nil {
		return "", fmt.Errorf("load claudegen prompt %q: %w", name, err)
	}
	return prompt.Render(template,
		"{{SESSION_PATH}}", sessionPath,
		"{{HOST}}", host,
		"{{TARGET_TYPE}}", string(targetType),
	), nil
}

func promptNameFor(targetType storage.TargetType) string {
	if targetType == storage.TargetTypeList {
		return "claudegen/list.user"
	}
	return "claudegen/page.user"
}
