package validator

import "issuetracker/pkg/llm/prompt"

// selector map 의미 검증 prompt 이름 — target type 별로 user prompt 가 분기합니다.
const (
	PromptNameSystem = "parser/validator/system"
	PromptNamePage   = "parser/validator/page.user"
	PromptNameList   = "parser/validator/list.user"
)

// PromptContracts 는 본 패키지가 로드하는 prompt 의 placeholder 계약을 반환합니다.
func PromptContracts() []prompt.Contract {
	return []prompt.Contract{
		{Name: PromptNameSystem},
		{Name: PromptNamePage, Placeholders: []string{
			"{{TITLE}}",
			"{{BODY}}",
			"{{PUBLISHED_AT_LINE}}",
			"{{PUBLISHED_AT_CRITERIA}}",
		}},
		{Name: PromptNameList, Placeholders: []string{
			"{{ITEM_CONTAINER}}",
			"{{ITEM_LINKS_LINE}}",
		}},
	}
}
