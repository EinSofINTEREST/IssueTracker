package processorcfg

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/joho/godotenv"

	"issuetracker/pkg/config/internal/parse"
)

// JobBufferConfig 는 normal/low priority crawl 메시지의 Redis 버퍼링 정책 설정입니다 (이슈 #510).
//
// Enabled=false (default) 면 BufferingProducer wiring 자체 skip — 기존 직접 publish 동작 유지.
// Enabled=true 면 cmd/issuetracker/main.go 가 BufferingProducer + BufferDrainer 를 wire.
type JobBufferConfig struct {
	// Enabled — 본 기능 활성화. PUBLISHER_REDIS_BUFFER_ENABLED (default: false).
	Enabled bool

	// DrainInterval — BufferDrainer goroutine 의 tick 주기.
	// PUBLISHER_REDIS_BUFFER_DRAIN_INTERVAL (default: 30s).
	DrainInterval time.Duration

	// TargetBacklog — drainer 가 유지하려는 Kafka consumer-group lag 상한.
	// scheduler.MaxBacklog (보통 5000) 의 60% 권장 (예: 3000).
	// PUBLISHER_REDIS_BUFFER_TARGET_BACKLOG (default: 3000).
	TargetBacklog int64

	// DrainBatch — 한 tick 당 priority 별로 최대 drain 할 메시지 수.
	// PUBLISHER_REDIS_BUFFER_DRAIN_BATCH (default: 100).
	DrainBatch int

	// MaxLen — Redis LIST 의 최대 길이. >0 이면 EnqueueJob 시 LTRIM 으로 oldest 제거.
	// 0 이면 길이 제한 없음 (운영자가 모니터링 책임).
	// PUBLISHER_REDIS_BUFFER_MAX_LEN (default: 100000).
	MaxLen int64

	// CheckTimeout — Backlog() 호출 한 번에 적용할 deadline.
	// PUBLISHER_REDIS_BUFFER_CHECK_TIMEOUT (default: 5s).
	CheckTimeout time.Duration
}

// DefaultJobBufferConfig 는 기본 JobBufferConfig 를 반환합니다.
func DefaultJobBufferConfig() JobBufferConfig {
	return JobBufferConfig{
		Enabled:       false,
		DrainInterval: 30 * time.Second,
		TargetBacklog: 3000,
		DrainBatch:    100,
		MaxLen:        100000,
		CheckTimeout:  5 * time.Second,
	}
}

// LoadJobBuffer 는 .env + 환경변수로 JobBufferConfig 를 구성합니다.
func LoadJobBuffer(envFiles ...string) (JobBufferConfig, error) {
	if len(envFiles) == 0 {
		envFiles = []string{".env"}
	}
	if err := godotenv.Load(envFiles...); err != nil && !errors.Is(err, os.ErrNotExist) {
		return JobBufferConfig{}, fmt.Errorf("failed to load env files %v: %w", envFiles, err)
	}

	cfg := DefaultJobBufferConfig()

	for _, op := range []error{
		parse.Bool("PUBLISHER_REDIS_BUFFER_ENABLED", &cfg.Enabled),
		parse.PositiveDuration("PUBLISHER_REDIS_BUFFER_DRAIN_INTERVAL", &cfg.DrainInterval),
		parse.PositiveInt64("PUBLISHER_REDIS_BUFFER_TARGET_BACKLOG", &cfg.TargetBacklog),
		parse.PositiveInt("PUBLISHER_REDIS_BUFFER_DRAIN_BATCH", &cfg.DrainBatch),
		parse.NonNegativeInt64("PUBLISHER_REDIS_BUFFER_MAX_LEN", &cfg.MaxLen), // 0 = unlimited
		parse.PositiveDuration("PUBLISHER_REDIS_BUFFER_CHECK_TIMEOUT", &cfg.CheckTimeout),
	} {
		if op != nil {
			return JobBufferConfig{}, op
		}
	}

	return cfg, nil
}
