package config_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"issuetracker/pkg/config"
)

func TestLoadStageGate_DefaultValues(t *testing.T) {
	t.Setenv("FETCHER_MAX_CONCURRENT_PER_STAGE", "")
	t.Setenv("PARSER_MAX_CONCURRENT_PER_STAGE", "")
	t.Setenv("VALIDATE_MAX_CONCURRENT_PER_STAGE", "")
	cfg, err := config.LoadStageGate("/tmp/nonexistent-env-file.env")
	require.NoError(t, err)
	require.Equal(t, 0, cfg.FetcherMaxConcurrentPerStage)
	require.Equal(t, 0, cfg.ParserMaxConcurrentPerStage)
	require.Equal(t, 0, cfg.ValidateMaxConcurrentPerStage)
}

func TestLoadStageGate_EnvOverride(t *testing.T) {
	t.Setenv("FETCHER_MAX_CONCURRENT_PER_STAGE", "2")
	t.Setenv("PARSER_MAX_CONCURRENT_PER_STAGE", "4")
	t.Setenv("VALIDATE_MAX_CONCURRENT_PER_STAGE", "5")
	cfg, err := config.LoadStageGate("/tmp/nonexistent-env-file.env")
	require.NoError(t, err)
	require.Equal(t, 2, cfg.FetcherMaxConcurrentPerStage)
	require.Equal(t, 4, cfg.ParserMaxConcurrentPerStage)
	require.Equal(t, 5, cfg.ValidateMaxConcurrentPerStage)
}

func TestLoadStageGate_InvalidValue(t *testing.T) {
	t.Setenv("PARSER_MAX_CONCURRENT_PER_STAGE", "not-a-number")
	_, err := config.LoadStageGate("/tmp/nonexistent-env-file.env")
	require.Error(t, err)
}

func TestLoadStageGate_InvalidFetcherValue(t *testing.T) {
	t.Setenv("FETCHER_MAX_CONCURRENT_PER_STAGE", "abc")
	_, err := config.LoadStageGate("/tmp/nonexistent-env-file.env")
	require.Error(t, err)
}

func TestLoadStageGate_InvalidValidateValue(t *testing.T) {
	t.Setenv("VALIDATE_MAX_CONCURRENT_PER_STAGE", "xyz")
	_, err := config.LoadStageGate("/tmp/nonexistent-env-file.env")
	require.Error(t, err)
}

func TestCapPerStage(t *testing.T) {
	tests := []struct {
		name        string
		workerCount int
		configured  int
		want        int
	}{
		{"workerCount<2 returns 1", 1, 0, 1},
		{"workerCount<2 ignores configured", 1, 99, 1},
		{"zero configured uses half", 6, 0, 3},
		{"negative configured uses half", 6, -1, 3},
		{"configured caps at half", 6, 5, 3},
		{"configured smaller than half", 6, 2, 2},
		{"configured equals half", 6, 3, 3},
		{"odd worker count floor", 7, 0, 3},
		{"even worker count 4", 4, 0, 2},
		{"worker count 2 gives 1", 2, 0, 1},
		{"explicit cap below worker_count/2 honored", 8, 1, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := config.CapPerStage(tt.workerCount, tt.configured)
			require.Equal(t, tt.want, got)
		})
	}
}
