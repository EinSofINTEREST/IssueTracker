package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"

	"github.com/joho/godotenv"
)

// WorkerCountsConfig 는 처리 stage 별 worker goroutine 수를 일괄 관리합니다 (이슈 #376).
//
// 운영자가 처리량 튜닝 시 env 만 조정하면 재빌드 없이 다음 재시작에 반영 — 즉시
// hot-reload 는 아님 (LoadWorkerCounts 가 startup 1회 호출). 변경 후 프로세스
// 재시작 필요. 모든 필드 default 는 기존 hardcoded 값과 동일하므로 env 미설정 시
// 동작 100% 보존.
//
// 명명 규약: 기존 `CLAUDE_CODE_WORKER_COUNT` / `FETCHER_CHROMEDP_WORKER_COUNT` 와 동일하게
// `<STAGE>_WORKER_COUNT` 형식.
type WorkerCountsConfig struct {
	// FetcherHigh: priority=high crawler pool 의 worker 수.
	// 환경변수 FETCHER_HIGH_WORKER_COUNT (default 3).
	FetcherHigh int

	// FetcherNormal: priority=normal crawler pool 의 worker 수.
	// 환경변수 FETCHER_NORMAL_WORKER_COUNT (default 6).
	FetcherNormal int

	// FetcherLow: priority=low crawler pool 의 worker 수.
	// 환경변수 FETCHER_LOW_WORKER_COUNT (default 2).
	FetcherLow int

	// Parser: parser worker (issuetracker.fetched consumer) 의 worker 수.
	// 환경변수 PARSER_WORKER_COUNT (default 6).
	Parser int

	// Validate: validate worker (issuetracker.normalized consumer) 의 worker 수.
	// 환경변수 VALIDATE_WORKER_COUNT (default 8).
	Validate int
}

// DefaultWorkerCountsConfig 는 운영 기본값을 반환합니다.
// 모든 값은 기존 cmd/issuetracker hardcoded 와 동일.
func DefaultWorkerCountsConfig() WorkerCountsConfig {
	return WorkerCountsConfig{
		FetcherHigh:   3,
		FetcherNormal: 6,
		FetcherLow:    2,
		Parser:        6,
		Validate:      8,
	}
}

// LoadWorkerCounts 는 .env 를 로드한 후 환경변수로 WorkerCountsConfig 를 구성합니다.
//
// 지원 환경변수 (모두 양의 정수 — 0 / 음수 / 비숫자는 invalid):
//   - FETCHER_HIGH_WORKER_COUNT   (default 3)
//   - FETCHER_NORMAL_WORKER_COUNT (default 6)
//   - FETCHER_LOW_WORKER_COUNT    (default 2)
//   - PARSER_WORKER_COUNT         (default 6)
//   - VALIDATE_WORKER_COUNT       (default 8)
func LoadWorkerCounts(envFiles ...string) (WorkerCountsConfig, error) {
	if len(envFiles) == 0 {
		envFiles = []string{".env"}
	}
	if err := godotenv.Load(envFiles...); err != nil && !errors.Is(err, os.ErrNotExist) {
		return WorkerCountsConfig{}, fmt.Errorf("failed to load env files %v: %w", envFiles, err)
	}

	cfg := DefaultWorkerCountsConfig()

	type binding struct {
		envKey string
		dest   *int
	}
	bindings := []binding{
		{"FETCHER_HIGH_WORKER_COUNT", &cfg.FetcherHigh},
		{"FETCHER_NORMAL_WORKER_COUNT", &cfg.FetcherNormal},
		{"FETCHER_LOW_WORKER_COUNT", &cfg.FetcherLow},
		{"PARSER_WORKER_COUNT", &cfg.Parser},
		{"VALIDATE_WORKER_COUNT", &cfg.Validate},
	}
	for _, b := range bindings {
		v := os.Getenv(b.envKey)
		if v == "" {
			continue
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			return WorkerCountsConfig{}, fmt.Errorf("parse %s %q: %w", b.envKey, v, err)
		}
		if n < 1 {
			return WorkerCountsConfig{}, fmt.Errorf("invalid %s %d: must be 1 or greater", b.envKey, n)
		}
		*b.dest = n
	}
	return cfg, nil
}
