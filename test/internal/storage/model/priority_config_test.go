package model_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/storage/model"
)

func f64(v float64) *float64 { return &v }

// TestParsePriorityConfig_Empty 는 빈 값이 "override 없음" 으로 처리되는지 검증합니다.
func TestParsePriorityConfig_Empty(t *testing.T) {
	for _, raw := range [][]byte{nil, {}, []byte("null")} {
		cfg, err := model.ParsePriorityConfig(raw)
		require.NoError(t, err)
		assert.Nil(t, cfg)
	}
}

// TestParsePriorityConfig_Full 은 전체 필드가 파싱되는지 검증합니다.
func TestParsePriorityConfig_Full(t *testing.T) {
	raw := []byte(`{
		"base_priority": "high",
		"override_until": "2026-05-20T00:00:00Z",
		"signal_weights": {"freshness": 1.5, "host_trust": 1.2},
		"score_threshold": 0.55
	}`)

	cfg, err := model.ParsePriorityConfig(raw)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	assert.Equal(t, "high", cfg.BasePriority)
	require.NotNil(t, cfg.OverrideUntil)
	assert.Equal(t, 2026, cfg.OverrideUntil.Year())
	require.NotNil(t, cfg.SignalWeights)
	require.NotNil(t, cfg.SignalWeights.Freshness)
	assert.InDelta(t, 1.5, *cfg.SignalWeights.Freshness, 0.0001)
	assert.Nil(t, cfg.SignalWeights.Impact, "미지정 weight 는 nil — 기본값을 쓴다는 뜻")
	require.NotNil(t, cfg.ScoreThreshold)
	assert.InDelta(t, 0.55, *cfg.ScoreThreshold, 0.0001)
}

// TestParsePriorityConfig_UnknownKey_Rejected 는 알 수 없는 키가 거부되는지 검증합니다.
//
// **이것이 본 파서의 핵심이다.** 오타난 키를 조용히 무시하면 운영자는 설정이 적용된 줄
// 알지만 동작은 그대로이고, 그 사실이 어디에도 드러나지 않는다.
func TestParsePriorityConfig_UnknownKey_Rejected(t *testing.T) {
	for _, raw := range []string{
		`{"base_priorty": "high"}`,                 // 오타
		`{"signal_weights": {"cost": 1.0}}`,        // Sub B 가 쓰지 않는 signal
		`{"signal_weights": {"reliability": 1.0}}`, // 동상
		`{"base_priority": "high", "extra": 1}`,    // 미지원 키
	} {
		t.Run(raw, func(t *testing.T) {
			_, err := model.ParsePriorityConfig([]byte(raw))
			require.Error(t, err, "알 수 없는 키는 거부돼야 한다")
		})
	}
}

// TestParsePriorityConfig_InvalidValues 는 값 검증을 확인합니다.
func TestParsePriorityConfig_InvalidValues(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"알 수 없는 priority", `{"base_priority": "urgent"}`, "invalid base_priority"},
		{"만료만 있고 대상 없음", `{"override_until": "2026-05-20T00:00:00Z"}`, "without base_priority"},
		{"임계값 범위 초과", `{"score_threshold": 1.5}`, "score_threshold"},
		{"임계값 음수", `{"score_threshold": -0.1}`, "score_threshold"},
		{"음수 weight", `{"signal_weights": {"impact": -1}}`, "must not be negative"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := model.ParsePriorityConfig([]byte(tt.raw))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

// TestOverrideActive 는 만료 판정을 검증합니다.
func TestOverrideActive(t *testing.T) {
	until := time.Date(2026, 5, 20, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		cfg  *model.PriorityConfig
		now  time.Time
		want bool
	}{
		{"nil", nil, until, false},
		{"base 없음", &model.PriorityConfig{}, until, false},
		{"무기한", &model.PriorityConfig{BasePriority: "high"}, until, true},
		{"만료 전", &model.PriorityConfig{BasePriority: "high", OverrideUntil: &until}, until.Add(-time.Hour), true},
		{"만료 경계", &model.PriorityConfig{BasePriority: "high", OverrideUntil: &until}, until, false},
		{"만료 후", &model.PriorityConfig{BasePriority: "high", OverrideUntil: &until}, until.Add(time.Hour), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.cfg.OverrideActive(tt.now))
		})
	}
}

// TestPriorityConfig_Validate_CaseInsensitive 는 priority 값의 대소문자를 허용하는지
// 검증합니다 — 운영자가 "High" 로 적어도 동작해야 합니다.
func TestPriorityConfig_Validate_CaseInsensitive(t *testing.T) {
	cfg := &model.PriorityConfig{BasePriority: "HIGH"}
	assert.NoError(t, cfg.Validate())
}

// TestPriorityConfig_PartialWeights 는 일부 weight 만 지정하는 구성이 유효한지 검증합니다.
func TestPriorityConfig_PartialWeights(t *testing.T) {
	cfg := &model.PriorityConfig{
		SignalWeights: &model.SignalWeights{HostTrust: f64(2.0)},
	}
	assert.NoError(t, cfg.Validate())
	assert.Nil(t, cfg.SignalWeights.Freshness)
}
