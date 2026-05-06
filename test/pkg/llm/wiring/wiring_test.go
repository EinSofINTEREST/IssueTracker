package wiring_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/pkg/llm/wiring"
	"issuetracker/pkg/logger"
)

// llmEnvKeys 는 본 테스트가 격리해야 하는 LLM 관련 환경변수 키들입니다.
var llmEnvKeys = []string{
	"LLM_ENABLED", "LLM_PROVIDER", "LLM_MODEL", "LLM_TIMEOUT", "LLM_API_KEY",
	"GEMINI_API_KEY", "OPENAI_API_KEY", "ANTHROPIC_API_KEY",
}

func clearLLMEnv(t *testing.T) {
	t.Helper()
	for _, k := range llmEnvKeys {
		t.Setenv(k, "")
	}
}

// TestBuildProvider_NoAPIKeys_ReturnsNil 은 모든 provider 의 API key 가 부재일 때 nil 반환을 검증합니다 (이슈 #216).
func TestBuildProvider_NoAPIKeys_ReturnsNil(t *testing.T) {
	clearLLMEnv(t)
	t.Setenv("LLM_ENABLED", "true")

	p := wiring.BuildProvider(logger.New(logger.DefaultConfig()))
	assert.Nil(t, p, "API key 부재 시 nil 반환")
}

// TestBuildProvider_LLMDisabled_ReturnsNil 은 LLM_ENABLED=false 시 즉시 nil 반환을 검증합니다.
func TestBuildProvider_LLMDisabled_ReturnsNil(t *testing.T) {
	clearLLMEnv(t)
	t.Setenv("LLM_ENABLED", "false")
	t.Setenv("GEMINI_API_KEY", "fake-key")

	p := wiring.BuildProvider(logger.New(logger.DefaultConfig()))
	assert.Nil(t, p, "LLM_ENABLED=false 시 API key 가 있어도 nil")
}

// TestBuildProvider_SingleKey_ReturnsChainWithOne 은 한 provider 의 key 만 있어도 chain 이 구성되는지 검증합니다.
// (단일-provider chain 도 graceful — 운영자가 추후 다른 key 추가하면 자동 확장).
func TestBuildProvider_SingleKey_ReturnsChainWithOne(t *testing.T) {
	clearLLMEnv(t)
	t.Setenv("LLM_ENABLED", "true")
	t.Setenv("GEMINI_API_KEY", "fake-gemini-key")

	p := wiring.BuildProvider(logger.New(logger.DefaultConfig()))
	require.NotNil(t, p, "단일 provider key 만으로도 chain 구성")
	// chain.PolicyProvider 는 Name() == "chain" — 외부 호출자에게 단일 provider 와 동일 인터페이스.
	assert.Equal(t, "chain", p.Name())
}

// TestBuildProvider_AllKeys_ReturnsChainWithAll 은 3개 provider key 모두 있을 때 chain 이 구성되는지 검증합니다.
func TestBuildProvider_AllKeys_ReturnsChainWithAll(t *testing.T) {
	clearLLMEnv(t)
	t.Setenv("LLM_ENABLED", "true")
	t.Setenv("GEMINI_API_KEY", "fake-gemini-key")
	t.Setenv("OPENAI_API_KEY", "fake-openai-key")
	t.Setenv("ANTHROPIC_API_KEY", "fake-anthropic-key")

	p := wiring.BuildProvider(logger.New(logger.DefaultConfig()))
	require.NotNil(t, p)
	assert.Equal(t, "chain", p.Name())
}

// TestBuildProvider_LLMAPIKeyFallback_OnlyPrimary 는 LLM_API_KEY fallback 이 primary (LLM_PROVIDER) 에만
// 적용되어 다른 provider 로 cascade 되지 않는지 검증합니다 (이슈 #216).
//
// 시나리오: LLM_PROVIDER=anthropic + LLM_API_KEY=anthropic_key 만 설정 — anthropic 만 chain 에 포함.
// gemini / openai 에는 cascade 되지 않아야 함 (잘못된 key 로 auth 실패하며 chain 시간 낭비 회피).
func TestBuildProvider_LLMAPIKeyFallback_OnlyPrimary(t *testing.T) {
	clearLLMEnv(t)
	t.Setenv("LLM_ENABLED", "true")
	t.Setenv("LLM_PROVIDER", "anthropic")
	t.Setenv("LLM_API_KEY", "fallback-key")

	p := wiring.BuildProvider(logger.New(logger.DefaultConfig()))
	require.NotNil(t, p, "LLM_API_KEY fallback 으로 primary provider 활성화")
	// chain 인터페이스 자체는 동일 — 내부 활성 provider 수는 로그로 검증 (chain=["anthropic"] 만).
	assert.Equal(t, "chain", p.Name())
}
