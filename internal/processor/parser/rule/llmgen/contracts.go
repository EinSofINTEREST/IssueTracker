package llmgen

import "issuetracker/pkg/llm/prompt"

// LLM 룰 생성 prompt 이름 — BuildPrompt 가 target type 으로 user prompt 를 분기합니다.
const (
	PromptNameSystem = "parser/llmgen/system"
	PromptNamePage   = "parser/llmgen/page.user"
	PromptNameList   = "parser/llmgen/list.user"
)

// userPlaceholders 는 BuildPrompt 가 user prompt 렌더링에 공급하는 토큰 전체입니다.
var userPlaceholders = []string{
	"{{HOST}}",
	"{{TARGET_TYPE}}",
	"{{MAX_HTML_BYTES}}",
	"{{HTML}}",
}

// PromptContracts 는 본 패키지가 로드하는 prompt 의 placeholder 계약을 반환합니다.
//
// system prompt 는 치환 없이 그대로 전달 — placeholder 목록이 빈 상태가 정상입니다.
func PromptContracts() []prompt.Contract {
	return []prompt.Contract{
		{Name: PromptNameSystem},
		{Name: PromptNamePage, Placeholders: userPlaceholders},
		{Name: PromptNameList, Placeholders: userPlaceholders},
	}
}
