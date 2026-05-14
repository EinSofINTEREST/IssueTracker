package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/joho/godotenv"
)

// FetcherAutoUpgradeConfig 는 host 단위 fetcher 실패 누적 → chromedp 자동 전환 정책 설정입니다.
//
// 본 단계는 카운팅 + 임계값 도달 신호 발신까지만 — 실제 fetcher_rules UPSERT / 실패 raw republish 는 단계 3 (#221) 의 책임.
//
// Window / Threshold 의 의미:
//   - 윈도우 (default 1h) 안에 같은 host 의 실패가 Threshold (default 5) 회 누적되면 trigger.
//   - 윈도우는 sliding — 마지막 실패 시각에서 Window 만큼 이전까지 카운트.
type FetcherAutoUpgradeConfig struct {
	// Enabled: false 면 카운팅 자체 skip (Noop FailureCounter 사용 — 성능 저하 0).
	// 환경변수 FETCHER_AUTO_UPGRADE_ENABLED (default true).
	Enabled bool

	// Threshold: window 내 실패 횟수 임계값 (이상이면 trigger). 환경변수 FETCHER_AUTO_UPGRADE_THRESHOLD (default 5).
	Threshold int

	// Window: sliding window 길이. 환경변수 FETCHER_AUTO_UPGRADE_WINDOW (Go duration, default 1h).
	Window time.Duration

	// EmptyBodyTitleMin / EmptyBodyContentMin: 빈본문 판정 임계값.
	// parse 자체는 성공했지만 결과 텍스트가 너무 짧은 경우도 실패 신호로 카운팅.
	// 환경변수 FETCHER_EMPTY_BODY_TITLE_MIN (default 5), FETCHER_EMPTY_BODY_CONTENT_MIN (default 100).
	EmptyBodyTitleMin   int
	EmptyBodyContentMin int
}

// DefaultFetcherAutoUpgradeConfig 는 기본 FetcherAutoUpgradeConfig 를 반환합니다.
func DefaultFetcherAutoUpgradeConfig() FetcherAutoUpgradeConfig {
	return FetcherAutoUpgradeConfig{
		Enabled:             true,
		Threshold:           5,
		Window:              1 * time.Hour,
		EmptyBodyTitleMin:   5,
		EmptyBodyContentMin: 100,
	}
}

// LoadFetcherAutoUpgrade 는 .env 를 로드한 후 OS 환경변수로 FetcherAutoUpgradeConfig 를 구성합니다.
//
// 지원 환경변수:
//   - FETCHER_AUTO_UPGRADE_ENABLED: true | false (default true)
//   - FETCHER_AUTO_UPGRADE_THRESHOLD: 양의 정수 (default 5)
//   - FETCHER_AUTO_UPGRADE_WINDOW: Go duration (default "1h")
//   - FETCHER_EMPTY_BODY_TITLE_MIN: 0 이상의 정수 (default 5)
//   - FETCHER_EMPTY_BODY_CONTENT_MIN: 0 이상의 정수 (default 100)
func LoadFetcherAutoUpgrade(envFiles ...string) (FetcherAutoUpgradeConfig, error) {
	if len(envFiles) == 0 {
		envFiles = []string{".env"}
	}
	if err := godotenv.Load(envFiles...); err != nil && !errors.Is(err, os.ErrNotExist) {
		return FetcherAutoUpgradeConfig{}, fmt.Errorf("failed to load env files %v: %w", envFiles, err)
	}

	cfg := DefaultFetcherAutoUpgradeConfig()

	if v := os.Getenv("FETCHER_AUTO_UPGRADE_ENABLED"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return FetcherAutoUpgradeConfig{}, fmt.Errorf("parse FETCHER_AUTO_UPGRADE_ENABLED %q: %w", v, err)
		}
		cfg.Enabled = b
	}
	if v := os.Getenv("FETCHER_AUTO_UPGRADE_THRESHOLD"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return FetcherAutoUpgradeConfig{}, fmt.Errorf("parse FETCHER_AUTO_UPGRADE_THRESHOLD %q: %w", v, err)
		}
		if n < 1 {
			return FetcherAutoUpgradeConfig{}, fmt.Errorf("invalid FETCHER_AUTO_UPGRADE_THRESHOLD %d: must be 1 or greater", n)
		}
		cfg.Threshold = n
	}
	if v := os.Getenv("FETCHER_AUTO_UPGRADE_WINDOW"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return FetcherAutoUpgradeConfig{}, fmt.Errorf("parse FETCHER_AUTO_UPGRADE_WINDOW %q: %w", v, err)
		}
		if d <= 0 {
			return FetcherAutoUpgradeConfig{}, fmt.Errorf("invalid FETCHER_AUTO_UPGRADE_WINDOW %q: must be positive", v)
		}
		cfg.Window = d
	}
	if v := os.Getenv("FETCHER_EMPTY_BODY_TITLE_MIN"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return FetcherAutoUpgradeConfig{}, fmt.Errorf("parse FETCHER_EMPTY_BODY_TITLE_MIN %q: %w", v, err)
		}
		if n < 0 {
			return FetcherAutoUpgradeConfig{}, fmt.Errorf("invalid FETCHER_EMPTY_BODY_TITLE_MIN %d: must be 0 or greater", n)
		}
		cfg.EmptyBodyTitleMin = n
	}
	if v := os.Getenv("FETCHER_EMPTY_BODY_CONTENT_MIN"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return FetcherAutoUpgradeConfig{}, fmt.Errorf("parse FETCHER_EMPTY_BODY_CONTENT_MIN %q: %w", v, err)
		}
		if n < 0 {
			return FetcherAutoUpgradeConfig{}, fmt.Errorf("invalid FETCHER_EMPTY_BODY_CONTENT_MIN %d: must be 0 or greater", n)
		}
		cfg.EmptyBodyContentMin = n
	}

	return cfg, nil
}
