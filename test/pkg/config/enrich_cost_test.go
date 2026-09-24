package config_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	runtimecfg "issuetracker/pkg/config/runtime"
)

// nonexistentEnv 는 .env 를 읽지 않도록 하는 더미 경로입니다 — 로컬 .env 가 테스트를
// 흔들지 않게 합니다 (stages_test.go 와 동일 관행).
const nonexistentEnv = "/tmp/nonexistent-env-file.env"

func clearEnrichCostEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"ENRICH_DAILY_CALL_LIMIT", "ENRICH_MAX_BACKLOG",
		"ENRICH_EXTRACT_ENABLED", "ENRICH_VERIFY_ENABLED",
		"ENRICH_CONTEXT_ENABLED", "ENRICH_SCORE_ENABLED",
	} {
		t.Setenv(k, "")
	}
}

// TestLoadEnrichCost_Defaults_PreserveExistingBehavior 는 env 미설정 시 기존 동작이
// 그대로 유지되는지 확인합니다 — 한도 없음, 전 단계 활성.
//
// 이 기본값이 흔들리면 배포만으로 enrichment 가 조용히 줄어듭니다.
func TestLoadEnrichCost_Defaults_PreserveExistingBehavior(t *testing.T) {
	clearEnrichCostEnv(t)

	cfg, err := runtimecfg.LoadEnrichCost(nonexistentEnv)
	require.NoError(t, err)

	assert.Equal(t, 0, cfg.DailyCallLimit, "기본은 무제한")
	assert.Equal(t, int64(0), cfg.MaxBacklog, "기본은 throttle 비활성")
	assert.True(t, cfg.ExtractEnabled)
	assert.True(t, cfg.VerifyEnabled)
	assert.True(t, cfg.ContextEnabled)
	assert.True(t, cfg.ScoreEnabled)
}

func TestLoadEnrichCost_ReadsValues(t *testing.T) {
	clearEnrichCostEnv(t)
	t.Setenv("ENRICH_DAILY_CALL_LIMIT", "500")
	t.Setenv("ENRICH_MAX_BACKLOG", "10000")
	t.Setenv("ENRICH_VERIFY_ENABLED", "false")
	t.Setenv("ENRICH_SCORE_ENABLED", "false")

	cfg, err := runtimecfg.LoadEnrichCost(nonexistentEnv)
	require.NoError(t, err)

	assert.Equal(t, 500, cfg.DailyCallLimit)
	assert.Equal(t, int64(10000), cfg.MaxBacklog)
	assert.True(t, cfg.ExtractEnabled, "지정하지 않은 토글은 기본값 유지")
	assert.False(t, cfg.VerifyEnabled)
	assert.True(t, cfg.ContextEnabled)
	assert.False(t, cfg.ScoreEnabled)
}

// TestLoadEnrichCost_NegativeRejected 는 음수를 거부하는지 확인합니다.
//
// 음수를 0 으로 뭉개면 "비활성" 과 구분되지 않아, 운영자의 오타가 조용히 무제한으로
// 해석됩니다. 에러로 올려 기동 시점에 드러나게 합니다.
func TestLoadEnrichCost_NegativeRejected(t *testing.T) {
	tests := []struct {
		name string
		key  string
	}{
		{name: "daily limit", key: "ENRICH_DAILY_CALL_LIMIT"},
		{name: "max backlog", key: "ENRICH_MAX_BACKLOG"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearEnrichCostEnv(t)
			t.Setenv(tt.key, "-1")

			_, err := runtimecfg.LoadEnrichCost(nonexistentEnv)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.key)
		})
	}
}

func TestLoadEnrichCost_InvalidValueRejected(t *testing.T) {
	clearEnrichCostEnv(t)
	t.Setenv("ENRICH_DAILY_CALL_LIMIT", "not-a-number")

	_, err := runtimecfg.LoadEnrichCost(nonexistentEnv)
	require.Error(t, err)
}
