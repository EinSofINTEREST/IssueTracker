package config_test

import (
	runtimecfg "issuetracker/pkg/config/runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadStages_DefaultValues(t *testing.T) {
	t.Setenv("STAGES_FETCHER_ENABLED", "")
	t.Setenv("STAGES_PARSER_ENABLED", "")
	t.Setenv("STAGES_VALIDATE_ENABLED", "")
	t.Setenv("STAGES_SCHEDULER_ENABLED", "")
	cfg, err := runtimecfg.LoadStages("/tmp/nonexistent-env-file.env")
	require.NoError(t, err)
	require.True(t, cfg.FetcherEnabled)
	require.True(t, cfg.ParserEnabled)
	require.True(t, cfg.ValidateEnabled)
	require.True(t, cfg.SchedulerEnabled)
}

func TestLoadStages_FetcherOnly(t *testing.T) {
	t.Setenv("STAGES_FETCHER_ENABLED", "true")
	t.Setenv("STAGES_PARSER_ENABLED", "false")
	t.Setenv("STAGES_VALIDATE_ENABLED", "false")
	t.Setenv("STAGES_SCHEDULER_ENABLED", "false")
	cfg, err := runtimecfg.LoadStages("/tmp/nonexistent-env-file.env")
	require.NoError(t, err)
	require.True(t, cfg.FetcherEnabled)
	require.False(t, cfg.ParserEnabled)
	require.False(t, cfg.ValidateEnabled)
	require.False(t, cfg.SchedulerEnabled)
}

func TestLoadStages_ValidateOnly(t *testing.T) {
	t.Setenv("STAGES_FETCHER_ENABLED", "false")
	t.Setenv("STAGES_PARSER_ENABLED", "false")
	t.Setenv("STAGES_VALIDATE_ENABLED", "true")
	t.Setenv("STAGES_SCHEDULER_ENABLED", "false")
	cfg, err := runtimecfg.LoadStages("/tmp/nonexistent-env-file.env")
	require.NoError(t, err)
	require.False(t, cfg.FetcherEnabled)
	require.False(t, cfg.ParserEnabled)
	require.True(t, cfg.ValidateEnabled)
	require.False(t, cfg.SchedulerEnabled)
}

func TestLoadStages_AllDisabled_Rejected(t *testing.T) {
	t.Setenv("STAGES_FETCHER_ENABLED", "false")
	t.Setenv("STAGES_PARSER_ENABLED", "false")
	t.Setenv("STAGES_VALIDATE_ENABLED", "false")
	t.Setenv("STAGES_SCHEDULER_ENABLED", "false")
	_, err := runtimecfg.LoadStages("/tmp/nonexistent-env-file.env")
	require.Error(t, err, "모두 false 면 의미 없으므로 명시적 거부")
}

func TestLoadStages_InvalidValue(t *testing.T) {
	t.Setenv("STAGES_FETCHER_ENABLED", "not-a-bool")
	_, err := runtimecfg.LoadStages("/tmp/nonexistent-env-file.env")
	require.Error(t, err)
}
