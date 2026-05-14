package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"

	"github.com/joho/godotenv"
)

// RetrySchedulerConfig 는 RedisDelayedRetryScheduler 의 운영자 노출 설정입니다.
//
// 본 struct 는 cmd 가 worker.RedisRetrySchedulerConfig 의 default 를 override 할 때
// 사용합니다. 다른 필드 (PollInterval / BatchSize / RepublishFailureBackoff) 는
// 현재 코드 default 가 운영 가치 있음 — 노출 필요 시 본 struct 에 추가하면 됨.
type RetrySchedulerConfig struct {
	// HeartbeatEveryNIdleTicks: idle (due 0) tick 이 연속 N 회 발생할 때마다 1회
	// "retry pipeline idle heartbeat" DEBUG 로그를 남김.
	// 환경변수 RETRY_HEARTBEAT_EVERY_N_IDLE_TICKS (default 60). 0=legacy (매 tick).
	HeartbeatEveryNIdleTicks int
}

// DefaultRetrySchedulerConfig 는 운영 기본값을 반환합니다.
func DefaultRetrySchedulerConfig() RetrySchedulerConfig {
	return RetrySchedulerConfig{HeartbeatEveryNIdleTicks: 60}
}

// LoadRetryScheduler 는 .env 를 로드한 후 환경변수로 RetrySchedulerConfig 를 구성합니다.
//
// 지원 환경변수:
//   - RETRY_HEARTBEAT_EVERY_N_IDLE_TICKS: int >= 0 (default 60)
//     0 은 legacy 동작 (매 idle tick 마다 1줄). 음수는 invalid.
func LoadRetryScheduler(envFiles ...string) (RetrySchedulerConfig, error) {
	if len(envFiles) == 0 {
		envFiles = []string{".env"}
	}
	if err := godotenv.Load(envFiles...); err != nil && !errors.Is(err, os.ErrNotExist) {
		return RetrySchedulerConfig{}, fmt.Errorf("failed to load env files %v: %w", envFiles, err)
	}

	cfg := DefaultRetrySchedulerConfig()
	if v := os.Getenv("RETRY_HEARTBEAT_EVERY_N_IDLE_TICKS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return RetrySchedulerConfig{}, fmt.Errorf("parse RETRY_HEARTBEAT_EVERY_N_IDLE_TICKS %q: %w", v, err)
		}
		if n < 0 {
			return RetrySchedulerConfig{}, fmt.Errorf("invalid RETRY_HEARTBEAT_EVERY_N_IDLE_TICKS %d: must be >= 0", n)
		}
		cfg.HeartbeatEveryNIdleTicks = n
	}
	return cfg, nil
}
