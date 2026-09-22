package core

import "issuetracker/pkg/llm/prompt"

// PromptContracts 는 enrich 4단계 (extract / cross-verify / context / score) 가 로드하는
// prompt 의 placeholder 계약을 반환합니다.
//
// 각 이름 상수는 해당 단계 파일 (claude.go / verifier.go / contextualizer.go / scorer.go) 에
// 정의되어 있으며, 본 함수는 그 상수와 렌더링 지점의 공급 토큰을 한 곳에 모읍니다.
func PromptContracts() []prompt.Contract {
	return []prompt.Contract{
		{Name: promptName, Placeholders: []string{
			"{{SESSION_PATH}}",
			"{{HOST}}",
			"{{URL}}",
			"{{TITLE}}",
		}},
		{Name: verifierPromptName, Placeholders: []string{
			"{{URL}}",
			"{{HOST}}",
			"{{TITLE}}",
			"{{CLAIMS_JSON}}",
		}},
		{Name: contextPromptName, Placeholders: []string{
			"{{URL}}",
			"{{HOST}}",
			"{{TITLE}}",
			"{{ENTITIES_JSON}}",
			"{{CLAIMS_JSON}}",
		}},
		{Name: scorerPromptName, Placeholders: []string{
			"{{URL}}",
			"{{HOST}}",
			"{{TITLE}}",
			"{{FACTS_JSON}}",
			"{{VERIFICATIONS_JSON}}",
			"{{CONTEXT_JSON}}",
		}},
	}
}
