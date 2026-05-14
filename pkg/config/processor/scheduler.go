package processorcfg

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/joho/godotenv"

	"issuetracker/pkg/config/internal/parse"
)

// SchedulerConfig는 Job Scheduler의 크롤 주기 설정을 나타냅니다.
// 소스 타입별로 독립적으로 조정할 수 있습니다.
type SchedulerConfig struct {
	CategoryInterval time.Duration // 카테고리 목록 폴링 주기 — SCHEDULER_CATEGORY_INTERVAL (default: 30m, 이슈 #329)
	JobTimeout       time.Duration // 개별 Job 최대 실행 시간 — SCHEDULER_JOB_TIMEOUT (default: 30s)
	MaxRetries       int           // Job 최대 재시도 횟수 — SCHEDULER_MAX_RETRIES (default: 3)

	// Backlog throttle: publish 직전 Kafka crawl 토픽의
	// consumer-group lag 가 임계값 초과 시 발행 차단.
	// MaxBacklog <= 0 → throttle 비활성 (기본).
	MaxBacklog          int64         // SCHEDULER_MAX_BACKLOG (default: 0 — disabled)
	BacklogCheckTimeout time.Duration // SCHEDULER_BACKLOG_CHECK_TIMEOUT (default: 5s)
}

// DefaultSchedulerConfig는 기본 SchedulerConfig를 반환합니다.
func DefaultSchedulerConfig() SchedulerConfig {
	return SchedulerConfig{
		// migration 020 (#329) 이 DB 의 news interval 을 30m 으로 단축한 것과 일관 —
		// DB 부재 환경에서 DefaultEntries fallback 도 동일 주기로 동작.
		CategoryInterval:    30 * time.Minute,
		JobTimeout:          30 * time.Second,
		MaxRetries:          3,
		MaxBacklog:          0, // disabled by default — opt-in via env
		BacklogCheckTimeout: 5 * time.Second,
	}
}

// LoadScheduler는 .env 파일을 로드한 후 OS 환경변수로 SchedulerConfig를 구성합니다.
// 환경변수 값이 설정되어 있지만 파싱에 실패하면 에러를 반환합니다.
func LoadScheduler(envFiles ...string) (SchedulerConfig, error) {
	if len(envFiles) == 0 {
		envFiles = []string{".env"}
	}
	if err := godotenv.Load(envFiles...); err != nil && !errors.Is(err, os.ErrNotExist) {
		return SchedulerConfig{}, fmt.Errorf("failed to load env files %v: %w", envFiles, err)
	}

	cfg := DefaultSchedulerConfig()

	// 검증 강화 (이슈 #439): duration 양수 / retries 비음수 / backlog 비음수.
	for _, op := range []error{
		parse.PositiveDuration("SCHEDULER_CATEGORY_INTERVAL", &cfg.CategoryInterval),
		parse.PositiveDuration("SCHEDULER_JOB_TIMEOUT", &cfg.JobTimeout),
		parse.NonNegativeInt("SCHEDULER_MAX_RETRIES", &cfg.MaxRetries),
		parse.NonNegativeInt64("SCHEDULER_MAX_BACKLOG", &cfg.MaxBacklog), // 0 = throttle disabled
		parse.PositiveDuration("SCHEDULER_BACKLOG_CHECK_TIMEOUT", &cfg.BacklogCheckTimeout),
	} {
		if op != nil {
			return SchedulerConfig{}, op
		}
	}

	return cfg, nil
}
