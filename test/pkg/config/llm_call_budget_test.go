package config_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	llmcfg "issuetracker/pkg/config/llm"
)

func clearLLMBudgetEnv(t *testing.T) {
	t.Helper()
	t.Setenv("LLM_DAILY_CAP", "")
	t.Setenv("LLM_HOURLY_CAP", "")
}

func TestLoadCallBudget_Defaults(t *testing.T) {
	clearLLMBudgetEnv(t)
	cfg, err := llmcfg.LoadCallBudget("/tmp/nonexistent-env-file.env")
	require.NoError(t, err)
	assert.Equal(t, 1000, cfg.DailyCap, "Gemini 무료 한도 기준")
	assert.Equal(t, 100, cfg.HourlyCap)
}

func TestLoadCallBudget_ReadsValues(t *testing.T) {
	clearLLMBudgetEnv(t)
	t.Setenv("LLM_DAILY_CAP", "500")
	t.Setenv("LLM_HOURLY_CAP", "50")

	cfg, err := llmcfg.LoadCallBudget("/tmp/nonexistent-env-file.env")
	require.NoError(t, err)
	assert.Equal(t, 500, cfg.DailyCap)
	assert.Equal(t, 50, cfg.HourlyCap)
}

// TestLoadCallBudget_HourlyExceedingDailyRejected 는 시간당 한도가 일일 한도보다 큰
// 설정을 거부하는지 확인합니다 — 사실상 무의미한 조합이라 운영자의 오타일 가능성이 높습니다.
func TestLoadCallBudget_HourlyExceedingDailyRejected(t *testing.T) {
	clearLLMBudgetEnv(t)
	t.Setenv("LLM_DAILY_CAP", "100")
	t.Setenv("LLM_HOURLY_CAP", "500")

	_, err := llmcfg.LoadCallBudget("/tmp/nonexistent-env-file.env")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "LLM_HOURLY_CAP")
}

// TestLoadCallBudget_UnlimitedDaily_AllowsAnyHourly 는 일일 무제한(0) 일 때
// 시간당 한도 검증이 적용되지 않는지 확인합니다.
func TestLoadCallBudget_UnlimitedDaily_AllowsAnyHourly(t *testing.T) {
	clearLLMBudgetEnv(t)
	t.Setenv("LLM_DAILY_CAP", "0")
	t.Setenv("LLM_HOURLY_CAP", "500")

	cfg, err := llmcfg.LoadCallBudget("/tmp/nonexistent-env-file.env")
	require.NoError(t, err)
	assert.Equal(t, 0, cfg.DailyCap)
	assert.Equal(t, 500, cfg.HourlyCap)
}

func TestLoadCallBudget_NegativeRejected(t *testing.T) {
	clearLLMBudgetEnv(t)
	t.Setenv("LLM_DAILY_CAP", "-1")

	_, err := llmcfg.LoadCallBudget("/tmp/nonexistent-env-file.env")
	require.Error(t, err)
}
