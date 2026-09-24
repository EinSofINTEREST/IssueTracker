package config_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	processorcfg "issuetracker/pkg/config/processor"
)

// clearHostScoringEnv 는 로컬 .env 나 이전 케이스가 결과를 흔들지 않도록 비웁니다.
func clearHostScoringEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"HOST_SCORING_ENABLED", "HOST_SCORING_INTERVAL", "HOST_SCORING_WINDOW_MINUTES",
		"HOST_SCORING_THRESHOLD", "HOST_SCORING_W_FRESHNESS", "HOST_SCORING_W_IMPACT",
		"HOST_SCORING_W_TRUST",
	} {
		t.Setenv(k, "")
	}
}

// TestLoadHostScoring_Defaults 는 미설정 시 기본값을 고정합니다.
//
// 특히 Enabled=false 가 기본이라는 점 — 점수가 우선순위를 바꾸는 기능이라 배포만으로
// 트래픽 분배가 달라지면 안 된다.
func TestLoadHostScoring_Defaults(t *testing.T) {
	clearHostScoringEnv(t)

	cfg, err := processorcfg.LoadHostScoring(nonexistentEnv)
	require.NoError(t, err)

	assert.False(t, cfg.Enabled, "기본 비활성이어야 한다")
	assert.Equal(t, 5*time.Minute, cfg.Interval)
	assert.Equal(t, 360, cfg.WindowMinutes)
	assert.InDelta(t, 0.7, cfg.Threshold, 0.0001)
	assert.InDelta(t, 0.3, cfg.WeightFreshness, 0.0001)
	assert.InDelta(t, 0.2, cfg.WeightImpact, 0.0001)
	assert.InDelta(t, 0.5, cfg.WeightTrust, 0.0001)
}

// TestLoadHostScoring_ZeroWeights_EnabledRejected 는 **조용한 no-op 방지 가드** 를
// 검증합니다 (이슈 #608).
//
// 가중치가 전부 0 이면 모든 host 점수가 0 이 되어 scoring 이 무의미해진다. 조용히
// 동작하면 "켰는데 아무 효과가 없다" 는 진단 불가 상태가 되므로 기동에서 끊어야 한다.
func TestLoadHostScoring_ZeroWeights_EnabledRejected(t *testing.T) {
	clearHostScoringEnv(t)
	t.Setenv("HOST_SCORING_ENABLED", "true")
	t.Setenv("HOST_SCORING_W_FRESHNESS", "0")
	t.Setenv("HOST_SCORING_W_IMPACT", "0")
	t.Setenv("HOST_SCORING_W_TRUST", "0")

	_, err := processorcfg.LoadHostScoring(nonexistentEnv)
	require.Error(t, err, "전부 0 인 가중치는 기동에서 끊어야 한다")
	assert.Contains(t, err.Error(), "HOST_SCORING_W_")
}

// TestLoadHostScoring_ZeroWeights_DisabledAllowed 는 끈 기능의 가중치를 강제하지
// 않는지 검증합니다.
//
// Enabled=false 면 scorer 가 아예 뜨지 않으므로 weight 가 0 이어도 문제가 없다.
// 여기서 막으면 기능을 끄려는 운영자가 쓰지도 않을 값을 맞춰야 한다.
func TestLoadHostScoring_ZeroWeights_DisabledAllowed(t *testing.T) {
	clearHostScoringEnv(t)
	t.Setenv("HOST_SCORING_ENABLED", "false")
	t.Setenv("HOST_SCORING_W_FRESHNESS", "0")
	t.Setenv("HOST_SCORING_W_IMPACT", "0")
	t.Setenv("HOST_SCORING_W_TRUST", "0")

	cfg, err := processorcfg.LoadHostScoring(nonexistentEnv)
	require.NoError(t, err)
	assert.False(t, cfg.Enabled)
}

// TestLoadHostScoring_PartialWeights_Allowed 는 일부만 0 인 구성이 통과하는지
// 검증합니다 — 합이 양수이면 정규화가 성립한다.
func TestLoadHostScoring_PartialWeights_Allowed(t *testing.T) {
	clearHostScoringEnv(t)
	t.Setenv("HOST_SCORING_ENABLED", "true")
	t.Setenv("HOST_SCORING_W_FRESHNESS", "0")
	t.Setenv("HOST_SCORING_W_IMPACT", "0")
	t.Setenv("HOST_SCORING_W_TRUST", "1")

	cfg, err := processorcfg.LoadHostScoring(nonexistentEnv)
	require.NoError(t, err)
	assert.InDelta(t, 1.0, cfg.WeightTrust, 0.0001)
}

// TestLoadHostScoring_Overrides 는 정상 override 가 반영되는지 검증합니다.
func TestLoadHostScoring_Overrides(t *testing.T) {
	clearHostScoringEnv(t)
	t.Setenv("HOST_SCORING_ENABLED", "true")
	t.Setenv("HOST_SCORING_INTERVAL", "90s")
	t.Setenv("HOST_SCORING_WINDOW_MINUTES", "120")
	t.Setenv("HOST_SCORING_THRESHOLD", "0.42")
	t.Setenv("HOST_SCORING_W_FRESHNESS", "1.5")

	cfg, err := processorcfg.LoadHostScoring(nonexistentEnv)
	require.NoError(t, err)

	assert.True(t, cfg.Enabled)
	assert.Equal(t, 90*time.Second, cfg.Interval)
	assert.Equal(t, 120, cfg.WindowMinutes)
	assert.InDelta(t, 0.42, cfg.Threshold, 0.0001)
	assert.InDelta(t, 1.5, cfg.WeightFreshness, 0.0001)
	assert.InDelta(t, 0.2, cfg.WeightImpact, 0.0001, "미지정 weight 는 기본값 유지")
}

// TestLoadHostScoring_InvalidValues 는 잘못된 값이 기동에서 거부되는지 검증합니다.
//
// 기본값으로 조용히 떨어뜨리지 않는 이유: 운영자가 지정한 값이 무시되면 설정이 적용된 줄
// 알지만 동작은 다르다.
func TestLoadHostScoring_InvalidValues(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"파싱 불가 interval", map[string]string{"HOST_SCORING_INTERVAL": "abc"}, "HOST_SCORING_INTERVAL"},
		{"0 interval", map[string]string{"HOST_SCORING_INTERVAL": "0s"}, "must be positive"},
		{"음수 interval", map[string]string{"HOST_SCORING_INTERVAL": "-5m"}, "must be positive"},
		{"window 0", map[string]string{"HOST_SCORING_WINDOW_MINUTES": "0"}, "1 or greater"},
		{"window 음수", map[string]string{"HOST_SCORING_WINDOW_MINUTES": "-1"}, "1 or greater"},
		{"파싱 불가 window", map[string]string{"HOST_SCORING_WINDOW_MINUTES": "abc"}, "HOST_SCORING_WINDOW_MINUTES"},
		{"threshold 1 초과", map[string]string{"HOST_SCORING_THRESHOLD": "1.5"}, "within [0,1]"},
		{"threshold 음수", map[string]string{"HOST_SCORING_THRESHOLD": "-0.1"}, "within [0,1]"},
		{"파싱 불가 threshold", map[string]string{"HOST_SCORING_THRESHOLD": "abc"}, "HOST_SCORING_THRESHOLD"},
		{"음수 weight", map[string]string{"HOST_SCORING_W_TRUST": "-1"}, "must not be negative"},
		{"파싱 불가 weight", map[string]string{"HOST_SCORING_W_IMPACT": "abc"}, "HOST_SCORING_W_IMPACT"},
		{"파싱 불가 enabled", map[string]string{"HOST_SCORING_ENABLED": "yesss"}, "HOST_SCORING_ENABLED"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearHostScoringEnv(t)
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			_, err := processorcfg.LoadHostScoring(nonexistentEnv)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

// TestLoadHostScoring_ThresholdBoundaries 는 경계값이 허용되는지 검증합니다.
func TestLoadHostScoring_ThresholdBoundaries(t *testing.T) {
	for _, v := range []string{"0", "1"} {
		t.Run(v, func(t *testing.T) {
			clearHostScoringEnv(t)
			t.Setenv("HOST_SCORING_THRESHOLD", v)
			_, err := processorcfg.LoadHostScoring(nonexistentEnv)
			assert.NoError(t, err, "[0,1] 경계값은 허용된다")
		})
	}
}
