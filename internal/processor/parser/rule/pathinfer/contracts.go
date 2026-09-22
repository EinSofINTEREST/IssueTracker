package pathinfer

import "issuetracker/pkg/llm/prompt"

// path_pattern 추론 prompt 이름.
const (
	PromptNameSystem = "parser/pathinfer/system"
	PromptNameUser   = "parser/pathinfer/user"
)

// PromptContracts 는 본 패키지가 로드하는 prompt 의 placeholder 계약을 반환합니다.
func PromptContracts() []prompt.Contract {
	return []prompt.Contract{
		{Name: PromptNameSystem},
		{Name: PromptNameUser, Placeholders: []string{"{{ARTICLES}}", "{{NON_ARTICLES}}"}},
	}
}
