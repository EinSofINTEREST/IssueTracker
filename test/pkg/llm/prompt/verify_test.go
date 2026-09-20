package prompt_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/pkg/llm/prompt"
)

func TestVerify_AllPlaceholdersSupplied_ReturnsNil(t *testing.T) {
	loader := prompt.MapLoader{
		"a/b": "host={{HOST}} type={{TARGET_TYPE}}",
		"a/c": "no placeholders here",
	}

	err := prompt.Verify(loader, []prompt.Contract{
		{Name: "a/b", Placeholders: []string{"{{HOST}}", "{{TARGET_TYPE}}"}},
		{Name: "a/c"},
	})

	assert.NoError(t, err)
}

func TestVerify_UnsuppliedPlaceholder_ReturnsError(t *testing.T) {
	loader := prompt.MapLoader{"a/b": "host={{HOST}} leaked={{NOT_SUPPLIED}}"}

	err := prompt.Verify(loader, []prompt.Contract{
		{Name: "a/b", Placeholders: []string{"{{HOST}}"}},
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "{{NOT_SUPPLIED}}")
	assert.Contains(t, err.Error(), "a/b")
}

// 같은 토큰이 템플릿에 여러 번 나와도 에러 메시지에는 1회만 — 운영자 메시지 가독성.
func TestVerify_DuplicateUnsuppliedPlaceholder_ReportedOnce(t *testing.T) {
	loader := prompt.MapLoader{"a/b": "{{DUP}} and {{DUP}} again"}

	err := prompt.Verify(loader, []prompt.Contract{{Name: "a/b"}})

	require.Error(t, err)
	assert.Equal(t, 1, countSubstring(err.Error(), "{{DUP}}"))
}

// 호출자가 템플릿이 쓰지 않는 토큰을 공급하는 것은 무해한 no-op — 실패시키지 않음.
func TestVerify_ExtraSuppliedPlaceholder_ReturnsNil(t *testing.T) {
	loader := prompt.MapLoader{"a/b": "host={{HOST}}"}

	err := prompt.Verify(loader, []prompt.Contract{
		{Name: "a/b", Placeholders: []string{"{{HOST}}", "{{UNUSED}}"}},
	})

	assert.NoError(t, err)
}

func TestVerify_LoadFailure_ReturnsError(t *testing.T) {
	err := prompt.Verify(prompt.MapLoader{}, []prompt.Contract{{Name: "missing/one"}})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing/one")
}

// 여러 계약이 동시에 깨졌을 때 한 번에 전부 보고 — 운영자가 한 회차에 모두 고칠 수 있도록.
func TestVerify_MultipleProblems_AllReported(t *testing.T) {
	loader := prompt.MapLoader{"a/b": "{{LEAK}}"}

	err := prompt.Verify(loader, []prompt.Contract{
		{Name: "a/b"},
		{Name: "gone"},
		{Name: ""},
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "{{LEAK}}")
	assert.Contains(t, err.Error(), "gone")
	assert.Contains(t, err.Error(), "empty name")
}

func TestVerify_NilLoader_ReturnsError(t *testing.T) {
	assert.Error(t, prompt.Verify(nil, []prompt.Contract{{Name: "a/b"}}))
}

func TestVerify_NoContracts_ReturnsNil(t *testing.T) {
	assert.NoError(t, prompt.Verify(prompt.MapLoader{}, nil))
}

func TestEmbeddedNames_ReturnsKnownPrompts(t *testing.T) {
	names, err := prompt.EmbeddedNames()
	require.NoError(t, err)
	require.NotEmpty(t, names)

	// 이름은 Load 인자와 동일 형식이어야 함 — prefix/suffix 가 남아 있으면 계약 대조가 어긋남.
	for _, n := range names {
		assert.NotContains(t, n, "assets/")
		assert.NotContains(t, n, ".txt")
		_, loadErr := prompt.NewEmbedLoader().Load(n)
		assert.NoError(t, loadErr, "EmbeddedNames returned %q but Load failed", n)
	}
	assert.Contains(t, names, "parser/llmgen/system")
}

func TestContractNames_ExtractsNames(t *testing.T) {
	got := prompt.ContractNames([]prompt.Contract{{Name: "a"}, {Name: "b"}})
	assert.Equal(t, []string{"a", "b"}, got)
}

func countSubstring(s, sub string) int {
	n := 0
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			n++
		}
	}
	return n
}
