package claude

import "issuetracker/pkg/llm/prompt"

// parser 룰 자동 생성 (claudegen) prompt 이름.
//
// promptNameFor 가 target type 으로 분기하며, 본 상수가 계약 (PromptContracts) 과 렌더링
// 지점의 단일 출처입니다.
const (
	PromptNameParserPage = "parser/claude/page.user"
	PromptNameParserList = "parser/claude/list.user"
)

// parserPlaceholders 는 buildPrompt 가 Render 에 공급하는 토큰 전체입니다.
//
// buildPrompt 를 수정해 토큰을 추가/제거하면 본 목록도 함께 갱신해야 합니다 — 누락 시
// prompt.Verify 가 기동 시점에 실패시킵니다.
var parserPlaceholders = []string{
	"{{SESSION_PATH}}",
	"{{HOST}}",
	"{{TARGET_TYPE}}",
	rejectReasonPlaceholder,
}

// PromptContracts 는 본 패키지가 로드하는 prompt 의 placeholder 계약을 반환합니다.
func PromptContracts() []prompt.Contract {
	return []prompt.Contract{
		{Name: PromptNameParserPage, Placeholders: parserPlaceholders},
		{Name: PromptNameParserList, Placeholders: parserPlaceholders},
	}
}
