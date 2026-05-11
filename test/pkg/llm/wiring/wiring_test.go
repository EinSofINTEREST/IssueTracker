package wiring_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/pkg/llm/wiring"
	"issuetracker/pkg/logger"
)

// llmEnvKeys 는 본 테스트가 격리해야 하는 LLM 관련 환경변수 키들입니다.
//
// LLM_POLICY / LLM_HYBRID_WEIGHTS 포함 — 로컬 운영자 .env 가 set 된 상태에서 hermetic 보장
// (Copilot 피드백 #342).
var llmEnvKeys = []string{
	"LLM_ENABLED", "LLM_PROVIDER", "LLM_MODEL", "LLM_TIMEOUT", "LLM_API_KEY",
	"LLM_POLICY", "LLM_HYBRID_WEIGHTS",
	"GEMINI_API_KEY", "OPENAI_API_KEY", "ANTHROPIC_API_KEY",
}

func clearLLMEnv(t *testing.T) {
	t.Helper()
	for _, k := range llmEnvKeys {
		t.Setenv(k, "")
	}
}

// captureLog 는 BuildProvider 의 로그 출력을 캡처할 (logger, *bytes.Buffer) 페어를 반환합니다.
//
// chain 구성 검증을 위해 "LLM provider chain enabled" info 로그의 chain / first_in_candidates /
// configured_primary 필드 값을 assert (PR #280 Copilot 리뷰).
func captureLog(t *testing.T) (*logger.Logger, *bytes.Buffer) {
	t.Helper()
	buf := &bytes.Buffer{}
	cfg := logger.DefaultConfig()
	cfg.Output = buf
	cfg.Level = logger.LevelDebug
	return logger.New(cfg), buf
}

// findChainEnabledLog 는 캡처 버퍼에서 "LLM provider chain enabled" 메시지를 찾아 fields 를 반환합니다.
// 발견 못하면 nil + t.Fatal.
func findChainEnabledLog(t *testing.T, buf *bytes.Buffer) map[string]interface{} {
	t.Helper()
	for _, line := range strings.Split(buf.String(), "\n") {
		if line == "" {
			continue
		}
		var entry map[string]interface{}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		msg, _ := entry["message"].(string)
		if strings.HasPrefix(msg, "LLM provider chain enabled") {
			return entry
		}
	}
	t.Fatalf("chain enabled log not found in: %s", buf.String())
	return nil
}

// chainNames 는 로그 entry 의 chain 필드를 string slice 로 변환합니다.
func chainNames(t *testing.T, entry map[string]interface{}) []string {
	t.Helper()
	raw, ok := entry["chain"].([]interface{})
	require.True(t, ok, "chain field missing or wrong type: %v", entry["chain"])
	out := make([]string, len(raw))
	for i, v := range raw {
		s, ok := v.(string)
		require.True(t, ok, "chain[%d] not string: %v", i, v)
		out[i] = s
	}
	return out
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

// TestBuildProvider_SingleKey_ChainHasOnlyThatProvider 는 한 provider 의 key 만 있을 때 chain 에
// 그 provider 만 포함됨을 로그로 검증합니다 (PR #280 Copilot — 약한 assertion 강화).
func TestBuildProvider_SingleKey_ChainHasOnlyThatProvider(t *testing.T) {
	clearLLMEnv(t)
	t.Setenv("LLM_ENABLED", "true")
	t.Setenv("GEMINI_API_KEY", "fake-gemini-key")

	log, buf := captureLog(t)
	p := wiring.BuildProvider(log)
	require.NotNil(t, p)
	assert.Equal(t, "chain", p.Name())

	entry := findChainEnabledLog(t, buf)
	assert.Equal(t, []string{"gemini"}, chainNames(t, entry), "단일 key 환경 — chain 1개")
	assert.Equal(t, "gemini", entry["first_in_candidates"])
}

// TestBuildProvider_AllKeys_ChainHasAllInOrder 는 3개 key 모두 있을 때 fallbackOrder 순서대로 chain 구성됨을 검증합니다.
func TestBuildProvider_AllKeys_ChainHasAllInOrder(t *testing.T) {
	clearLLMEnv(t)
	t.Setenv("LLM_ENABLED", "true")
	t.Setenv("GEMINI_API_KEY", "fake-gemini-key")
	t.Setenv("OPENAI_API_KEY", "fake-openai-key")
	t.Setenv("ANTHROPIC_API_KEY", "fake-anthropic-key")

	log, buf := captureLog(t)
	p := wiring.BuildProvider(log)
	require.NotNil(t, p)

	entry := findChainEnabledLog(t, buf)
	assert.Equal(t, []string{"gemini", "openai", "anthropic"}, chainNames(t, entry),
		"3개 key 환경 — fallbackOrder 순서로 chain 구성")
	assert.Equal(t, "gemini", entry["first_in_candidates"])
}

// TestBuildProvider_LLMAPIKeyFallback_OnlyPrimary 는 LLM_API_KEY fallback 이 primary (LLM_PROVIDER) 에만
// 적용되어 다른 provider 로 cascade 되지 않는지 검증합니다 (이슈 #216).
//
// 시나리오: LLM_PROVIDER=anthropic + LLM_API_KEY=fallback-key 만 설정 — anthropic 만 chain 에 포함.
// gemini / openai 에는 cascade 되지 않아야 함 (잘못된 key 로 auth 실패하며 chain 시간 낭비 회피).
func TestBuildProvider_LLMAPIKeyFallback_OnlyPrimary(t *testing.T) {
	clearLLMEnv(t)
	t.Setenv("LLM_ENABLED", "true")
	t.Setenv("LLM_PROVIDER", "anthropic")
	t.Setenv("LLM_API_KEY", "fallback-key")

	log, buf := captureLog(t)
	p := wiring.BuildProvider(log)
	require.NotNil(t, p)

	entry := findChainEnabledLog(t, buf)
	assert.Equal(t, []string{"anthropic"}, chainNames(t, entry),
		"LLM_API_KEY 는 primary (anthropic) 에만 적용 — gemini/openai 미포함")
	assert.Equal(t, "anthropic", entry["configured_primary"])
}

// TestBuildProvider_LLMAPIKeyFallback_ClaudeAlias 는 LLM_PROVIDER=claude alias 가 anthropic 으로
// 정규화되어 LLM_API_KEY fallback 이 정확히 anthropic 항목에 적용되는지 검증합니다 (PR #280 gemini).
func TestBuildProvider_LLMAPIKeyFallback_ClaudeAlias(t *testing.T) {
	clearLLMEnv(t)
	t.Setenv("LLM_ENABLED", "true")
	t.Setenv("LLM_PROVIDER", "claude")
	t.Setenv("LLM_API_KEY", "fallback-key")

	log, buf := captureLog(t)
	p := wiring.BuildProvider(log)
	require.NotNil(t, p, "claude alias 가 anthropic 으로 정규화되어 chain 에 포함")

	entry := findChainEnabledLog(t, buf)
	assert.Equal(t, []string{"anthropic"}, chainNames(t, entry))
	assert.Equal(t, "anthropic", entry["configured_primary"], "claude → anthropic 정규화")
}

// TestBuildProvider_PrimaryDifferentFromFirstInChain 는 LLM_PROVIDER 가 chain 에 포함되지 않은 경우
// configured_primary 와 first_in_candidates 이 다르게 로깅되는지 검증합니다 (PR #280 Copilot).
//
// 시나리오: LLM_PROVIDER=openai (primary 의도) 인데 OPENAI_API_KEY 부재, GEMINI_API_KEY 만 있음 →
// chain=[gemini], first_in_candidates=gemini, configured_primary=openai 으로 운영자가 의도 vs 실제 차이를 감지 가능.
func TestBuildProvider_PrimaryDifferentFromFirstInChain(t *testing.T) {
	clearLLMEnv(t)
	t.Setenv("LLM_ENABLED", "true")
	t.Setenv("LLM_PROVIDER", "openai")
	t.Setenv("GEMINI_API_KEY", "fake-gemini-key")

	log, buf := captureLog(t)
	p := wiring.BuildProvider(log)
	require.NotNil(t, p)

	entry := findChainEnabledLog(t, buf)
	assert.Equal(t, []string{"gemini"}, chainNames(t, entry))
	assert.Equal(t, "gemini", entry["first_in_candidates"])
	assert.Equal(t, "openai", entry["configured_primary"],
		"운영자 의도 (openai) 와 실제 chain 첫 시도 (gemini) 가 다른 상황을 운영 로그로 식별 가능")
}
