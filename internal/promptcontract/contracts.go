// Package promptcontract 는 시스템이 사용하는 모든 LLM prompt 의 placeholder 계약을 한 곳에
// 모읍니다 (이슈 #539).
//
// 계약 자체는 prompt 를 로드·렌더링하는 각 패키지가 소유합니다 (PromptContracts). 본 패키지는
// 그것을 합치기만 — 소유권을 옮기지 않으므로, 렌더링 코드를 고치는 사람이 같은 파일에서 계약을
// 보게 되는 성질이 유지됩니다.
//
// 용도:
//   - 기동 시 prompt.Verify 로 전 계약 검증 (cmd/issuetracker)
//   - 테스트에서 내장 asset 이 계약을 만족하는지, 계약이 asset 전부를 덮는지 대조
package promptcontract

import (
	enrichcore "issuetracker/internal/processor/enrich/core"
	"issuetracker/internal/processor/parser/rule/llmgen"
	"issuetracker/internal/processor/parser/rule/pathinfer"
	"issuetracker/internal/processor/parser/rule/validator"
	"issuetracker/pkg/agent/claude"
	"issuetracker/pkg/llm/prompt"
)

// All 은 시스템 전체의 prompt 계약을 반환합니다.
//
// 새 prompt asset 을 추가하면 소유 패키지의 PromptContracts 에 등록해야 합니다 — 누락 시
// 테스트 (TestAll_CoversEveryEmbeddedPrompt) 가 실패시킵니다.
func All() []prompt.Contract {
	groups := [][]prompt.Contract{
		claude.PromptContracts(),
		llmgen.PromptContracts(),
		pathinfer.PromptContracts(),
		validator.PromptContracts(),
		enrichcore.PromptContracts(),
	}

	total := 0
	for _, g := range groups {
		total += len(g)
	}

	all := make([]prompt.Contract, 0, total)
	for _, g := range groups {
		all = append(all, g...)
	}
	return all
}
