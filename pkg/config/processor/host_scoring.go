package processorcfg

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/joho/godotenv"
)

// HostScoringConfig 는 host 단위 동적 priority scoring 설정입니다 (이슈 #382, 메타 #380 Sub B).
//
// 기본 비활성이다. 점수가 우선순위를 바꾸는 기능이라, 켜는 것은 운영자의 명시적 결정이어야
// 한다 — 기본 활성이면 배포만으로 트래픽 분배가 달라진다.
type HostScoringConfig struct {
	// Enabled: false 면 scorer goroutine 과 resolver 등록을 모두 skip.
	// 환경변수 HOST_SCORING_ENABLED (default false).
	Enabled bool

	// Interval: 집계 주기. 환경변수 HOST_SCORING_INTERVAL (default 5m).
	Interval time.Duration

	// WindowMinutes: 집계 구간(분). 환경변수 HOST_SCORING_WINDOW_MINUTES (default 360 = 6h).
	//
	// Interval 보다 충분히 길어야 한다 — 짧으면 표본이 모자라 대부분 cold-start 로 빠진다.
	WindowMinutes int

	// Threshold: 이 값 이상이면 High. 환경변수 HOST_SCORING_THRESHOLD (default 0.7).
	//
	// 0.5 (중립) 가 아니라 0.7 인 이유: 미지정 signal 이 중립값 0.5 로 채워지므로,
	// 0.5 를 기준으로 두면 정보가 거의 없는 host 가 경계에서 흔들린다. 명확히 좋은
	// host 만 승급시킨다.
	Threshold float64

	// 가중치 — 환경변수 HOST_SCORING_W_{FRESHNESS,IMPACT,TRUST}.
	WeightFreshness float64
	WeightImpact    float64
	WeightTrust     float64
}

// DefaultHostScoringConfig 는 기본값을 반환합니다.
func DefaultHostScoringConfig() HostScoringConfig {
	return HostScoringConfig{
		Enabled:         false,
		Interval:        5 * time.Minute,
		WindowMinutes:   360,
		Threshold:       0.7,
		WeightFreshness: 0.3,
		WeightImpact:    0.2,
		WeightTrust:     0.5,
	}
}

// LoadHostScoring 는 .env 를 로드한 후 OS 환경변수로 설정을 구성합니다.
func LoadHostScoring(envFiles ...string) (HostScoringConfig, error) {
	if len(envFiles) == 0 {
		envFiles = []string{".env"}
	}
	if err := godotenv.Load(envFiles...); err != nil && !errors.Is(err, os.ErrNotExist) {
		return HostScoringConfig{}, fmt.Errorf("failed to load env files %v: %w", envFiles, err)
	}

	cfg := DefaultHostScoringConfig()

	if v := os.Getenv("HOST_SCORING_ENABLED"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return HostScoringConfig{}, fmt.Errorf("parse HOST_SCORING_ENABLED %q: %w", v, err)
		}
		cfg.Enabled = b
	}
	if v := os.Getenv("HOST_SCORING_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return HostScoringConfig{}, fmt.Errorf("parse HOST_SCORING_INTERVAL %q: %w", v, err)
		}
		if d <= 0 {
			return HostScoringConfig{}, fmt.Errorf("invalid HOST_SCORING_INTERVAL %q: must be positive", v)
		}
		cfg.Interval = d
	}
	if v := os.Getenv("HOST_SCORING_WINDOW_MINUTES"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return HostScoringConfig{}, fmt.Errorf("parse HOST_SCORING_WINDOW_MINUTES %q: %w", v, err)
		}
		if n < 1 {
			return HostScoringConfig{}, fmt.Errorf("invalid HOST_SCORING_WINDOW_MINUTES %d: must be 1 or greater", n)
		}
		cfg.WindowMinutes = n
	}
	if v := os.Getenv("HOST_SCORING_THRESHOLD"); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return HostScoringConfig{}, fmt.Errorf("parse HOST_SCORING_THRESHOLD %q: %w", v, err)
		}
		if f < 0 || f > 1 {
			return HostScoringConfig{}, fmt.Errorf("invalid HOST_SCORING_THRESHOLD %v: must be within [0,1]", f)
		}
		cfg.Threshold = f
	}

	for _, w := range []struct {
		env string
		dst *float64
	}{
		{"HOST_SCORING_W_FRESHNESS", &cfg.WeightFreshness},
		{"HOST_SCORING_W_IMPACT", &cfg.WeightImpact},
		{"HOST_SCORING_W_TRUST", &cfg.WeightTrust},
	} {
		v := os.Getenv(w.env)
		if v == "" {
			continue
		}
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return HostScoringConfig{}, fmt.Errorf("parse %s %q: %w", w.env, v, err)
		}
		if f < 0 {
			return HostScoringConfig{}, fmt.Errorf("invalid %s %v: must not be negative", w.env, f)
		}
		*w.dst = f
	}

	// 가중치가 전부 0 이면 모든 host 점수가 0 이 되어 scoring 이 무의미해진다.
	// 조용히 동작하면 "켰는데 아무 효과가 없다" 는 진단 불가 상태가 되므로 기동에서 끊는다.
	if cfg.Enabled && cfg.WeightFreshness+cfg.WeightImpact+cfg.WeightTrust <= 0 {
		return HostScoringConfig{}, errors.New(
			"invalid host scoring weights: at least one of HOST_SCORING_W_* must be positive")
	}

	return cfg, nil
}
