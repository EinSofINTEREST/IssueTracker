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
	"issuetracker/pkg/logger"
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

// VerifiedLoader 는 기동 시점에 prompt 계약을 검증하고, 깨졌으면 내장 prompt 로 격하합니다
// (이슈 #539).
//
// Loader 는 lazy 라 검증이 없으면 이름 오타나 LLM_PROMPT_DIR override 누락이 해당 경로의 첫
// 실제 요청에서야 드러납니다. 더 나쁜 경우는 템플릿이 쓰는 {{TOKEN}} 을 호출자가 공급하지 않는
// 상황 — 치환되지 않은 토큰이 그대로 LLM 에 전달되어 응답 품질이 조용히 무너지고 호출 비용만
// 나갑니다.
//
// 정책: 계약 위반 시 embed-only 로 격하 (내장 asset 은 테스트가 계약 만족을 강제하므로 안전한
// 기준점). 내장까지 위반이면 빌드 시점 결함이므로 ERROR 만 남기고 그대로 진행 — prompt 문제로
// 부팅이 막히는 가용성 사고를 만들지 않는다는 pkg/llm/prompt 의 정책과 일관.
//
// **모든 진입점에서 호출해야 합니다** — cmd/issuetracker 와 cmd/rule-validator 가 같은 prompt
// 자산을 공유하므로, 한쪽만 검증하면 다른 쪽으로 잘못된 override 가 그대로 흘러갑니다.
func VerifiedLoader(loader prompt.Loader, log *logger.Logger) prompt.Loader {
	contracts := All()
	err := prompt.Verify(loader, contracts)
	if err == nil {
		log.WithField("prompt_count", len(contracts)).Info("prompt contracts verified")
		return loader
	}

	embedOnly := prompt.NewEmbedLoader()
	if fallbackErr := prompt.Verify(embedOnly, contracts); fallbackErr != nil {
		// 내장 asset 까지 계약을 어긴 경우 — 빌드 시점 결함이다. 진단에 필요한 것은 **내장 쪽**
		// 실패 내용이므로 그것을 주 오류로 싣고, 외부 loader 의 오류는 별도 필드로 보존한다.
		log.WithField("outer_loader_error", err.Error()).WithError(fallbackErr).
			Error("prompt contract verification failed for embedded prompts, continuing as-is")
		return loader
	}

	log.WithError(err).Error("prompt contract verification failed, falling back to embedded prompts")
	return embedOnly
}
