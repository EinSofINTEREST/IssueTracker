package config_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	fetchercfg "issuetracker/pkg/config/fetcher"
	processorcfg "issuetracker/pkg/config/processor"
)

func clearStaleRelearnEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"STALE_RELEARN_ENABLED", "STALE_RELEARN_THRESHOLD", "STALE_RELEARN_WINDOW",
	} {
		t.Setenv(k, "")
	}
}

// TestLoadStaleRelearn_Defaults 는 기본값을 고정합니다.
func TestLoadStaleRelearn_Defaults(t *testing.T) {
	clearStaleRelearnEnv(t)

	cfg, err := processorcfg.LoadStaleRelearn(nonexistentEnv)
	require.NoError(t, err)

	assert.True(t, cfg.Enabled)
	assert.Equal(t, 10, cfg.Threshold)
	assert.Equal(t, 2*time.Hour, cfg.Window)
}

// TestStaleRelearn_TriggersAfterChromedpUpgrade 는 **두 임계값의 상대 관계** 를
// 고정합니다 (이슈 #610).
//
// stale 재학습은 LLM 호출 비용이 들고, chromedp 자동 전환은 그렇지 않다. 설계 의도는
// chromedp 를 먼저 시도하고 그래도 실패가 지속될 때 재학습하는 것이다 — 그래서 재학습
// 임계값이 더 높아야 한다.
//
// 한쪽 기본값만 바꾸면 순서가 뒤집혀 **비싼 경로가 먼저 트리거** 되는데, 각 로더를 따로
// 보면 드러나지 않는다. 관계 자체를 여기서 고정한다.
func TestStaleRelearn_TriggersAfterChromedpUpgrade(t *testing.T) {
	clearStaleRelearnEnv(t)
	for _, k := range []string{
		"FETCHER_AUTO_UPGRADE_ENABLED", "FETCHER_AUTO_UPGRADE_THRESHOLD", "FETCHER_AUTO_UPGRADE_WINDOW",
	} {
		t.Setenv(k, "")
	}

	relearn, err := processorcfg.LoadStaleRelearn(nonexistentEnv)
	require.NoError(t, err)
	upgrade, err := fetchercfg.LoadFetcherAutoUpgrade(nonexistentEnv)
	require.NoError(t, err)

	assert.Greater(t, relearn.Threshold, upgrade.Threshold,
		"stale 재학습(LLM 비용)은 chromedp 자동 전환보다 늦게 트리거돼야 한다")
	assert.Greater(t, relearn.Window, upgrade.Window,
		"재학습 관찰 기간이 더 길어야 보수적 판단이 된다")
}

// TestLoadStaleRelearn_Overrides 는 정상 override 반영을 검증합니다.
func TestLoadStaleRelearn_Overrides(t *testing.T) {
	clearStaleRelearnEnv(t)
	t.Setenv("STALE_RELEARN_ENABLED", "false")
	t.Setenv("STALE_RELEARN_THRESHOLD", "3")
	t.Setenv("STALE_RELEARN_WINDOW", "45m")

	cfg, err := processorcfg.LoadStaleRelearn(nonexistentEnv)
	require.NoError(t, err)

	assert.False(t, cfg.Enabled)
	assert.Equal(t, 3, cfg.Threshold)
	assert.Equal(t, 45*time.Minute, cfg.Window)
}

// TestLoadStaleRelearn_InvalidValues 는 잘못된 값이 기동에서 거부되는지 검증합니다.
//
// 기본값으로 조용히 떨어뜨리지 않는다 — 운영자가 지정한 임계값이 무시되면 재학습이
// 의도와 다른 시점에 돈다.
func TestLoadStaleRelearn_InvalidValues(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"파싱 불가 enabled", map[string]string{"STALE_RELEARN_ENABLED": "maybe"}, "STALE_RELEARN_ENABLED"},
		{"파싱 불가 threshold", map[string]string{"STALE_RELEARN_THRESHOLD": "abc"}, "STALE_RELEARN_THRESHOLD"},
		{"threshold 0", map[string]string{"STALE_RELEARN_THRESHOLD": "0"}, "1 or greater"},
		{"threshold 음수", map[string]string{"STALE_RELEARN_THRESHOLD": "-1"}, "1 or greater"},
		{"파싱 불가 window", map[string]string{"STALE_RELEARN_WINDOW": "abc"}, "STALE_RELEARN_WINDOW"},
		{"window 0", map[string]string{"STALE_RELEARN_WINDOW": "0s"}, "must be positive"},
		{"window 음수", map[string]string{"STALE_RELEARN_WINDOW": "-1h"}, "must be positive"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearStaleRelearnEnv(t)
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			_, err := processorcfg.LoadStaleRelearn(nonexistentEnv)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

// ── LoadBlacklist ───────────────────────────────────────────────────────────

// TestLoadBlacklist_Default 는 기본 활성을 고정합니다.
func TestLoadBlacklist_Default(t *testing.T) {
	t.Setenv("BLACKLIST_ENABLED", "")

	cfg, err := processorcfg.LoadBlacklist(nonexistentEnv)
	require.NoError(t, err)
	assert.True(t, cfg.Enabled)
}

// TestLoadBlacklist_Override 는 명시적 비활성이 반영되는지 검증합니다.
func TestLoadBlacklist_Override(t *testing.T) {
	t.Setenv("BLACKLIST_ENABLED", "false")

	cfg, err := processorcfg.LoadBlacklist(nonexistentEnv)
	require.NoError(t, err)
	assert.False(t, cfg.Enabled)
}

// TestLoadBlacklist_InvalidValue 는 파싱 불가 값이 **기본값으로 떨어지지 않고 에러** 가
// 되는지 검증합니다.
//
// bool 하나뿐이라 사소해 보이지만, 오타난 값이 조용히 기본값(true)으로 처리되면 운영자는
// 기능을 껐다고 믿는 상태가 된다.
func TestLoadBlacklist_InvalidValue(t *testing.T) {
	t.Setenv("BLACKLIST_ENABLED", "nope")

	_, err := processorcfg.LoadBlacklist(nonexistentEnv)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "BLACKLIST_ENABLED")
}
