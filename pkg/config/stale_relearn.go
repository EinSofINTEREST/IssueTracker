package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/joho/godotenv"
)

// StaleRelearnConfig 는 stale rule (parse_failure / empty_selector) 누적 → LLM 자동 재학습 정책 설정입니다.
//
// FetcherAutoUpgrade 와 별개의 keyspace + 더 긴 윈도우 / 더 높은 임계값 — chromedp 자동 전환을
// 먼저 시도한 후, 그래도 실패가 지속되면 LLM 재학습 트리거. 임계 도달 시 Generator.EnqueueStale
// 가 InsertNextVersion 으로 동일 자연키 (source, host, path, type) 의 다음 버전을 INSERT.
type StaleRelearnConfig struct {
	// Enabled: false 면 카운팅 자체 skip (Noop StaleCounter — 성능 저하 0).
	// 환경변수 STALE_RELEARN_ENABLED (default true).
	Enabled bool

	// Threshold: 윈도우 내 stale parse failure 임계값. 환경변수 STALE_RELEARN_THRESHOLD (default 10).
	// chromedp 자동 전환 (default 5) 보다 높게 — chromedp 가 먼저 시도되도록.
	Threshold int

	// Window: sliding window 길이. 환경변수 STALE_RELEARN_WINDOW (Go duration, default 2h).
	// chromedp 자동 전환 (default 1h) 보다 길게 — 더 보수적 관찰 기간.
	Window time.Duration
}

// DefaultStaleRelearnConfig 는 기본 StaleRelearnConfig 를 반환합니다.
func DefaultStaleRelearnConfig() StaleRelearnConfig {
	return StaleRelearnConfig{
		Enabled:   true,
		Threshold: 10,
		Window:    2 * time.Hour,
	}
}

// LoadStaleRelearn 는 .env 를 로드한 후 OS 환경변수로 StaleRelearnConfig 를 구성합니다.
//
// 지원 환경변수:
//   - STALE_RELEARN_ENABLED   : true | false (default true)
//   - STALE_RELEARN_THRESHOLD : 양의 정수 (default 10)
//   - STALE_RELEARN_WINDOW    : Go duration (default "2h")
func LoadStaleRelearn(envFiles ...string) (StaleRelearnConfig, error) {
	if len(envFiles) == 0 {
		envFiles = []string{".env"}
	}
	if err := godotenv.Load(envFiles...); err != nil && !errors.Is(err, os.ErrNotExist) {
		return StaleRelearnConfig{}, fmt.Errorf("failed to load env files %v: %w", envFiles, err)
	}

	cfg := DefaultStaleRelearnConfig()

	if v := os.Getenv("STALE_RELEARN_ENABLED"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return StaleRelearnConfig{}, fmt.Errorf("parse STALE_RELEARN_ENABLED %q: %w", v, err)
		}
		cfg.Enabled = b
	}
	if v := os.Getenv("STALE_RELEARN_THRESHOLD"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return StaleRelearnConfig{}, fmt.Errorf("parse STALE_RELEARN_THRESHOLD %q: %w", v, err)
		}
		if n < 1 {
			return StaleRelearnConfig{}, fmt.Errorf("invalid STALE_RELEARN_THRESHOLD %d: must be 1 or greater", n)
		}
		cfg.Threshold = n
	}
	if v := os.Getenv("STALE_RELEARN_WINDOW"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return StaleRelearnConfig{}, fmt.Errorf("parse STALE_RELEARN_WINDOW %q: %w", v, err)
		}
		if d <= 0 {
			return StaleRelearnConfig{}, fmt.Errorf("invalid STALE_RELEARN_WINDOW %q: must be positive", v)
		}
		cfg.Window = d
	}

	return cfg, nil
}
