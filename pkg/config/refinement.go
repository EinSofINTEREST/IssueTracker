package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/joho/godotenv"
)

// RefinementConfig 는 점진적 정밀화 워크플로의 설정입니다.
//
// catch-all + llm-auto rule 의 누적 sample URL 로부터 path_pattern 을 추론하여 자동 갱신.
//
// 환경변수:
//   - REFINEMENT_ENABLED: true | false (default true) — 비활성 시 background goroutine 시작 X
//   - REFINEMENT_INTERVAL: polling 주기 (default 5m)
//   - REFINEMENT_MIN_SAMPLES: rule 당 트리거 임계값 (default 5)
type RefinementConfig struct {
	Enabled    bool
	Interval   time.Duration
	MinSamples int
}

// DefaultRefinementConfig 는 기본 RefinementConfig 를 반환합니다.
func DefaultRefinementConfig() RefinementConfig {
	return RefinementConfig{
		Enabled:    true,
		Interval:   5 * time.Minute,
		MinSamples: 5,
	}
}

// LoadRefinement 는 .env 를 로드한 후 OS 환경변수로 RefinementConfig 를 구성합니다.
func LoadRefinement(envFiles ...string) (RefinementConfig, error) {
	if len(envFiles) == 0 {
		envFiles = []string{".env"}
	}
	if err := godotenv.Load(envFiles...); err != nil && !errors.Is(err, os.ErrNotExist) {
		return RefinementConfig{}, fmt.Errorf("failed to load env files %v: %w", envFiles, err)
	}

	cfg := DefaultRefinementConfig()

	if v := os.Getenv("REFINEMENT_ENABLED"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return RefinementConfig{}, fmt.Errorf("parse REFINEMENT_ENABLED %q: %w", v, err)
		}
		cfg.Enabled = b
	}
	if v := os.Getenv("REFINEMENT_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return RefinementConfig{}, fmt.Errorf("parse REFINEMENT_INTERVAL %q: %w", v, err)
		}
		if d <= 0 {
			return RefinementConfig{}, fmt.Errorf("invalid REFINEMENT_INTERVAL %q: must be positive", v)
		}
		cfg.Interval = d
	}
	if v := os.Getenv("REFINEMENT_MIN_SAMPLES"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return RefinementConfig{}, fmt.Errorf("parse REFINEMENT_MIN_SAMPLES %q: %w", v, err)
		}
		if n < 1 {
			return RefinementConfig{}, fmt.Errorf("invalid REFINEMENT_MIN_SAMPLES %d: must be 1 or greater", n)
		}
		cfg.MinSamples = n
	}

	return cfg, nil
}
