package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"

	"github.com/joho/godotenv"
)

// PathInferConfig 는 pathinfer 휴리스틱의 설정입니다.
//
// pathinfer 패키지의 InferHeuristic 동작을 운영자가 환경변수로 조정 가능하도록.
type PathInferConfig struct {
	// MinSamples: 추론에 필요한 최소 sample URL 수.
	// 환경변수: PATHINFER_MIN_SAMPLES (default 3).
	// 너무 낮으면 (1-2) 공통 vs 변수 구분 의미 없음, 너무 높으면 (10+) sample 수집 지연.
	MinSamples int
}

// DefaultPathInferConfig 는 기본 PathInferConfig 를 반환합니다.
func DefaultPathInferConfig() PathInferConfig {
	return PathInferConfig{
		MinSamples: 3,
	}
}

// LoadPathInfer 는 .env 를 로드한 후 OS 환경변수로 PathInferConfig 를 구성합니다.
// 지원 환경변수: PATHINFER_MIN_SAMPLES (양의 정수, default 3).
func LoadPathInfer(envFiles ...string) (PathInferConfig, error) {
	if len(envFiles) == 0 {
		envFiles = []string{".env"}
	}
	if err := godotenv.Load(envFiles...); err != nil && !errors.Is(err, os.ErrNotExist) {
		return PathInferConfig{}, fmt.Errorf("failed to load env files %v: %w", envFiles, err)
	}

	cfg := DefaultPathInferConfig()

	if v := os.Getenv("PATHINFER_MIN_SAMPLES"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return PathInferConfig{}, fmt.Errorf("parse PATHINFER_MIN_SAMPLES %q: %w", v, err)
		}
		if n < 1 {
			return PathInferConfig{}, fmt.Errorf("invalid PATHINFER_MIN_SAMPLES %d: must be 1 or greater", n)
		}
		cfg.MinSamples = n
	}

	return cfg, nil
}
