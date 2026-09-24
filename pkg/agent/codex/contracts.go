package codex

import "issuetracker/pkg/llm/prompt"

// parser 룰 자동 생성 prompt 이름 (이슈 #594).
//
// **경로가 `parser/claude/` 인 것은 의도된 것이다.** 해당 asset 은 backend 중립이며
// (모델·제공자 언급 없음, placeholder 도 동일), codex 의 buildPrompt 가 채우는 토큰과
// 정확히 일치한다. enrich 경로도 두 backend 가 `enrich/claude/*` 를 공용한다 — 같은 선례.
//
// 과거에는 `parser/codex/*` 를 가리켰으나 그 asset 이 존재한 적이 없어, parser 경로가
// prompt 로드 단계에서 항상 실패했다 (이슈 #594). 지금 두 prompt 를 다르게 쓸 이유가
// 없으므로 복제 대신 공용한다. codex 전용 튜닝이 필요해지면 그때 분리한다.
const (
	PromptNameParserPage = "parser/claude/page.user"
	PromptNameParserList = "parser/claude/list.user"
)

// parserPlaceholders 는 buildPrompt 가 Render 에 공급하는 토큰 전체입니다.
//
// buildPrompt 를 수정해 토큰을 추가/제거하면 본 목록도 함께 갱신해야 합니다.
var parserPlaceholders = []string{
	"{{SESSION_PATH}}",
	"{{HOST}}",
	"{{TARGET_TYPE}}",
	rejectReasonPlaceholder,
}

// PromptContracts 는 본 패키지가 로드하는 prompt 의 placeholder 계약을 반환합니다.
//
// **`internal/promptcontract.All()` 에는 등록하지 않는다.** claude 패키지가 같은 이름으로
// 이미 등록하고 있고, 집계에는 중복 이름을 금지하는 테스트가 있다
// (`TestAll_NoDuplicateContractNames`). 기동 시 검증은 claude 쪽 등록이 같은 경로를
// 덮으므로 공백이 생기지 않는다.
//
// 그럼에도 본 함수를 노출하는 이유: codex 가 공급하는 placeholder 집합이 asset 과
// 어긋나는지를 이 패키지 자신의 테스트가 검증할 수 있어야 한다. claude 의 buildPrompt 가
// 바뀌어 두 backend 가 갈라지면 그 테스트가 잡는다.
func PromptContracts() []prompt.Contract {
	return []prompt.Contract{
		{Name: PromptNameParserPage, Placeholders: parserPlaceholders},
		{Name: PromptNameParserList, Placeholders: parserPlaceholders},
	}
}
