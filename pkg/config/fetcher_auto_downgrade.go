package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/joho/godotenv"
)

// FetcherAutoDowngradeConfig 는 자동 upgrade 된 host 를 주기적으로 goquery 로 되돌리는 안전장치 설정입니다.
//
// upgrade-only 비대칭으로 인해 일시적 트래픽 에러로 잘못 upgrade 된 host 가 영원히 chromedp 로
// 처리되어 시간 누적 시 모든 host 가 chromedp 로 수렴 → Chrome 자원 압박. 본 cron 이 주기적
// 으로 reason='auto_upgrade_validation' row 를 일괄 goquery 로 reset 한다.
// 진짜 SPA host 는 단계 2 의 카운터가 24h 내 재upgrade 하므로 영향 없음. manual 룰은 보호.
type FetcherAutoDowngradeConfig struct {
	// Enabled: false 면 cron 자체 비활성. 환경변수 FETCHER_AUTO_DOWNGRADE_ENABLED (default true).
	Enabled bool

	// Interval: downgrade cron 실행 주기. 환경변수 FETCHER_AUTO_DOWNGRADE_INTERVAL (Go duration, default 168h = 7일).
	// 너무 짧으면 정상 chromedp host 가 자주 reset 되어 fetch latency ↑ — 24h 이상 권장.
	Interval time.Duration
}

// DefaultFetcherAutoDowngradeConfig 는 기본 FetcherAutoDowngradeConfig 를 반환합니다.
func DefaultFetcherAutoDowngradeConfig() FetcherAutoDowngradeConfig {
	return FetcherAutoDowngradeConfig{
		Enabled:  true,
		Interval: 7 * 24 * time.Hour,
	}
}

// LoadFetcherAutoDowngrade 는 .env 를 로드한 후 OS 환경변수로 FetcherAutoDowngradeConfig 를 구성합니다.
//
// 지원 환경변수:
//   - FETCHER_AUTO_DOWNGRADE_ENABLED: true | false (default true)
//   - FETCHER_AUTO_DOWNGRADE_INTERVAL: Go duration (default "168h" = 7일)
func LoadFetcherAutoDowngrade(envFiles ...string) (FetcherAutoDowngradeConfig, error) {
	if len(envFiles) == 0 {
		envFiles = []string{".env"}
	}
	if err := godotenv.Load(envFiles...); err != nil && !errors.Is(err, os.ErrNotExist) {
		return FetcherAutoDowngradeConfig{}, fmt.Errorf("failed to load env files %v: %w", envFiles, err)
	}

	cfg := DefaultFetcherAutoDowngradeConfig()

	if v := os.Getenv("FETCHER_AUTO_DOWNGRADE_ENABLED"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return FetcherAutoDowngradeConfig{}, fmt.Errorf("parse FETCHER_AUTO_DOWNGRADE_ENABLED %q: %w", v, err)
		}
		cfg.Enabled = b
	}
	if v := os.Getenv("FETCHER_AUTO_DOWNGRADE_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return FetcherAutoDowngradeConfig{}, fmt.Errorf("parse FETCHER_AUTO_DOWNGRADE_INTERVAL %q: %w", v, err)
		}
		if d <= 0 {
			return FetcherAutoDowngradeConfig{}, fmt.Errorf("invalid FETCHER_AUTO_DOWNGRADE_INTERVAL %q: must be positive", v)
		}
		cfg.Interval = d
	}

	return cfg, nil
}
