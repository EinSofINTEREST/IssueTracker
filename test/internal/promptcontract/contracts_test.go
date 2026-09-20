package promptcontract_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/promptcontract"
	"issuetracker/pkg/llm/prompt"
)

// 내장 asset 이 계약을 만족하는지 — 본 테스트가 prompt drift 의 실질적 게이트입니다.
// 템플릿에 토큰을 추가하고 호출자 Render 를 갱신하지 않으면 여기서 실패합니다.
func TestAll_EmbeddedAssetsSatisfyContracts(t *testing.T) {
	err := prompt.Verify(prompt.NewEmbedLoader(), promptcontract.All())
	assert.NoError(t, err)
}

// 계약이 asset 전부를 덮는지 — 새 prompt 파일만 추가하고 등록을 빠뜨리면
// 그 prompt 는 placeholder 검증 없이 운영에 나가므로 여기서 잡습니다.
func TestAll_CoversEveryEmbeddedPrompt(t *testing.T) {
	names, err := prompt.EmbeddedNames()
	require.NoError(t, err)
	require.NotEmpty(t, names)

	covered := make(map[string]struct{})
	for _, c := range promptcontract.All() {
		covered[c.Name] = struct{}{}
	}

	for _, n := range names {
		_, ok := covered[n]
		assert.True(t, ok, "embedded prompt %q has no registered Contract — 소유 패키지의 PromptContracts 에 추가 필요", n)
	}
}

// 계약이 존재하지 않는 asset 을 가리키지 않는지 — 이름 오타 / asset 삭제 후 계약 잔존 탐지.
func TestAll_EveryContractNameLoads(t *testing.T) {
	loader := prompt.NewEmbedLoader()
	for _, c := range promptcontract.All() {
		_, err := loader.Load(c.Name)
		assert.NoError(t, err, "contract %q does not resolve to an embedded prompt", c.Name)
	}
}

func TestAll_NoDuplicateContractNames(t *testing.T) {
	seen := make(map[string]struct{})
	for _, c := range promptcontract.All() {
		_, dup := seen[c.Name]
		assert.False(t, dup, "duplicate contract for %q", c.Name)
		seen[c.Name] = struct{}{}
	}
}
