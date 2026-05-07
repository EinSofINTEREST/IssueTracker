package prompt_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/pkg/llm/prompt"
)

// EmbedLoader 는 binary 에 컴파일된 assets/ 의 실 파일을 사용 — 4개 패키지 prompt 가 포함됐는지
// 한 케이스로 sanity 확인. 파일이 누락 / 빈 채로 빌드되면 본 테스트가 즉시 실패.
func TestEmbedLoader_Load_KnownPrompt(t *testing.T) {
	l := prompt.NewEmbedLoader()

	for _, name := range []string{
		"llmgen/system",
		"pathinfer/system",
		"validator/system",
		"claudegen/page.user",
	} {
		t.Run(name, func(t *testing.T) {
			body, err := l.Load(name)
			require.NoError(t, err, "embedded prompt %q 가 binary 에 포함되어야 함", name)
			assert.NotEmpty(t, body)
		})
	}
}

func TestEmbedLoader_Load_MissingKey_ReturnsError(t *testing.T) {
	l := prompt.NewEmbedLoader()
	_, err := l.Load("nonexistent/missing")
	require.Error(t, err)
}

func TestEmbedLoader_Load_EmptyName_ReturnsError(t *testing.T) {
	l := prompt.NewEmbedLoader()
	_, err := l.Load("")
	require.Error(t, err)
}

// stubLoader 는 ChainLoader 동작을 검증하는 수동 stub.
type stubLoader struct {
	resp string
	err  error
}

func (s stubLoader) Load(_ string) (string, error) {
	return s.resp, s.err
}

func TestChainLoader_FirstSuccess_ReturnsImmediately(t *testing.T) {
	first := stubLoader{resp: "first"}
	second := stubLoader{resp: "second"}
	chain := prompt.NewChainLoader(first, second)
	body, err := chain.Load("any")
	require.NoError(t, err)
	assert.Equal(t, "first", body)
}

func TestChainLoader_FirstFails_FallsBackToSecond(t *testing.T) {
	first := stubLoader{err: errors.New("first failed")}
	second := stubLoader{resp: "second-ok"}
	chain := prompt.NewChainLoader(first, second)
	body, err := chain.Load("any")
	require.NoError(t, err)
	assert.Equal(t, "second-ok", body)
}

func TestChainLoader_AllFail_ReturnsFirstError(t *testing.T) {
	first := stubLoader{err: errors.New("err-A")}
	second := stubLoader{err: errors.New("err-B")}
	chain := prompt.NewChainLoader(first, second)
	_, err := chain.Load("any")
	require.Error(t, err)
	// 첫 chain 의 에러가 진단상 가장 유용 — 호출자 의도된 우선순위 실패.
	assert.Contains(t, err.Error(), "err-A")
}

func TestNewChainLoader_Empty_ReturnsErrorOnLoad(t *testing.T) {
	chain := prompt.NewChainLoader()
	require.NotNil(t, chain, "빈 chain 도 비-nil 반환 — 인터페이스 변수에서 nil 수신자 패닉 회피")

	_, err := chain.Load("any")
	require.Error(t, err, "빈 chain 의 Load 는 silent ('',nil) 가 아닌 명시적 에러")
}

func TestChainLoader_NilReceiver_ReturnsError(t *testing.T) {
	var chain *prompt.ChainLoader
	_, err := chain.Load("any")
	require.Error(t, err, "nil 수신자도 panic 없이 에러 반환")
}

func TestNewDefaultLoader_NoEnv_NoDefaultDir_EmbedOnly(t *testing.T) {
	t.Setenv(prompt.EnvPromptsDir, "")
	// DefaultDir ("pkg/llm/prompt/assets") 는 cwd 기준 — 테스트가 임시 디렉토리에서 돌면 부재.
	tmp := t.TempDir()
	cwd, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(tmp))
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	loader, warn := prompt.NewDefaultLoader()
	assert.Empty(t, warn, "default 부재는 silent — embed 로 graceful fallback")
	require.NotNil(t, loader)

	// embed 가 실 동작하는지 한 번 호출 — assets 안의 파일이 로드되어야 함.
	body, err := loader.Load("llmgen/system")
	require.NoError(t, err)
	assert.NotEmpty(t, body)
}

func TestNewDefaultLoader_EnvSet_UsesFile(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "custom"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "custom", "ovr.txt"), []byte("from-file"), 0o600))
	t.Setenv(prompt.EnvPromptsDir, dir)

	loader, warn := prompt.NewDefaultLoader()
	require.Empty(t, warn)

	body, err := loader.Load("custom/ovr")
	require.NoError(t, err)
	assert.Equal(t, "from-file", body)

	// 외부 파일에 없는 prompt 는 embed 로 fallback.
	body, err = loader.Load("llmgen/system")
	require.NoError(t, err)
	assert.NotEmpty(t, body)
}

func TestNewDefaultLoader_EnvSet_BadDir_FallbackToEmbed(t *testing.T) {
	t.Setenv(prompt.EnvPromptsDir, "/nonexistent/path/should/not/exist")

	loader, warn := prompt.NewDefaultLoader()
	require.NotNil(t, loader)
	assert.NotEmpty(t, warn, "잘못된 경로는 warn 메시지로 호출자에게 전달")

	body, err := loader.Load("llmgen/system")
	require.NoError(t, err)
	assert.NotEmpty(t, body, "embed-only 로 graceful 동작")
}
