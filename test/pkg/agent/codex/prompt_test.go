// 본 파일은 codex 가 참조하는 prompt 이름이 **운영 loader 로 실제 해석되는지** 검증합니다
// (이슈 #594).
//
// 다른 codex 테스트들은 prompt.MapLoader 에 키를 직접 넣어 주므로, 이름이 실재하는
// asset 을 가리키는지는 검증하지 못한다. 그 공백 때문에 parser 경로가 존재하지 않는
// `parser/codex/*` 를 가리킨 채 머지됐고, prompt 로드 단계에서 항상 실패했다.
package codex_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/pkg/agent/codex"
	"issuetracker/pkg/llm/prompt"
)

// TestPromptNames_ResolveWithEmbedLoader 는 codex 의 prompt 이름이 내장 asset 으로
// 해석되는지 검증합니다 — MapLoader 가 아니라 운영이 쓰는 EmbedLoader 로 확인한다.
func TestPromptNames_ResolveWithEmbedLoader(t *testing.T) {
	loader := prompt.NewEmbedLoader()

	for _, name := range []string{
		codex.PromptNameParserPage,
		codex.PromptNameParserList,
	} {
		t.Run(name, func(t *testing.T) {
			body, err := loader.Load(name)
			require.NoError(t, err, "codex 가 참조하는 prompt 이름이 내장 asset 으로 해석되지 않는다")
			assert.NotEmpty(t, body)
		})
	}
}

// TestPromptContracts_SatisfiedByEmbeddedAssets 는 codex 가 공급하는 placeholder 집합이
// 실제 asset 과 맞는지 검증합니다.
//
// codex 는 claude 와 asset 을 공용하므로 `internal/promptcontract.All()` 에 중복 등록하지
// 않는다. 그래서 claude 의 buildPrompt 가 바뀌어 두 backend 가 갈라지는 상황을 잡을 곳이
// 여기밖에 없다.
func TestPromptContracts_SatisfiedByEmbeddedAssets(t *testing.T) {
	err := prompt.Verify(prompt.NewEmbedLoader(), codex.PromptContracts())
	assert.NoError(t, err)
}

// TestPromptContracts_MatchClaudeAssets 는 codex 가 claude 와 **같은 asset** 을 가리키는지
// 고정합니다.
//
// 분리가 필요해지면 이 테스트를 고치면서 분리 의도를 드러내게 된다 — 조용히 갈라지는 것을
// 막는 장치다.
func TestPromptContracts_MatchClaudeAssets(t *testing.T) {
	assert.Equal(t, "parser/claude/page.user", codex.PromptNameParserPage)
	assert.Equal(t, "parser/claude/list.user", codex.PromptNameParserList)
}
