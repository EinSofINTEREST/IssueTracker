package runtimecfg

import (
	"errors"
	"fmt"
	"os"
	"strconv"

	"github.com/joho/godotenv"
)

// StageGateConfig 는 stage 별 StageGate 의 동시 처리 슬롯 상한을 나타냅니다 (이슈 #353/#355/#356).
//
// 각 stage 의 Semaphore capacity 는 worker_count/2 를 넘지 않아야 함 — 이슈 #353 설계 제약.
// 환경변수로 명시적 cap 을 받아 worker_count/2 와 비교 후 작은 값을 채택.
type StageGateConfig struct {
	// FetcherMaxConcurrentPerStage: fetcher stage 의 Semaphore capacity 상한.
	// 0 이하면 worker_count/2 (floor) 사용. 양수면 min(value, worker_count/2) 채택.
	// fetcher 는 priority pool (high/normal/low) 별 worker 수가 다르므로 pool 별로 동일 cap 적용.
	FetcherMaxConcurrentPerStage int

	// ParserMaxConcurrentPerStage: parser stage 의 Semaphore capacity 상한.
	// 0 이하면 worker_count/2 (floor) 사용. 양수면 min(value, worker_count/2) 채택.
	ParserMaxConcurrentPerStage int

	// ValidateMaxConcurrentPerStage: validate stage 의 Semaphore capacity 상한.
	// 0 이하면 worker_count/2 (floor) 사용. 양수면 min(value, worker_count/2) 채택.
	ValidateMaxConcurrentPerStage int
}

// DefaultStageGateConfig 는 모든 stage 가 0 (worker_count/2 자동) 인 기본값을 반환합니다.
func DefaultStageGateConfig() StageGateConfig {
	return StageGateConfig{
		FetcherMaxConcurrentPerStage:  0,
		ParserMaxConcurrentPerStage:   0,
		ValidateMaxConcurrentPerStage: 0,
	}
}

// LoadStageGate 는 .env + OS 환경변수로 StageGateConfig 를 구성합니다.
func LoadStageGate(envFiles ...string) (StageGateConfig, error) {
	if len(envFiles) == 0 {
		envFiles = []string{".env"}
	}
	if err := godotenv.Load(envFiles...); err != nil && !errors.Is(err, os.ErrNotExist) {
		return StageGateConfig{}, fmt.Errorf("failed to load env files %v: %w", envFiles, err)
	}

	cfg := DefaultStageGateConfig()
	parseInt := func(key string, dest *int) error {
		if v := os.Getenv(key); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				return fmt.Errorf("parse %s %q: %w", key, v, err)
			}
			*dest = n
		}
		return nil
	}
	for _, op := range []error{
		parseInt("FETCHER_MAX_CONCURRENT_PER_STAGE", &cfg.FetcherMaxConcurrentPerStage),
		parseInt("PARSER_MAX_CONCURRENT_PER_STAGE", &cfg.ParserMaxConcurrentPerStage),
		parseInt("VALIDATE_MAX_CONCURRENT_PER_STAGE", &cfg.ValidateMaxConcurrentPerStage),
	} {
		if op != nil {
			return StageGateConfig{}, op
		}
	}
	return cfg, nil
}

// CapPerStage 는 worker_count 와 설정된 explicit cap 을 조합하여 Semaphore capacity 를 결정합니다.
//
// 규칙 (이슈 #353):
//   - workerCount < 2 면 1 반환 (semaphore 최소 1 보장)
//   - configured <= 0 이면 workerCount/2 (floor)
//   - configured > 0 이면 min(configured, workerCount/2)
//
// floor 결과가 0 이면 1 로 보정 — semaphore.NewSemaphore 가 capacity >= 1 요구.
func CapPerStage(workerCount, configured int) int {
	if workerCount < 2 {
		return 1
	}
	half := workerCount / 2
	if half < 1 {
		half = 1
	}
	if configured <= 0 {
		return half
	}
	if configured < half {
		return configured
	}
	return half
}
