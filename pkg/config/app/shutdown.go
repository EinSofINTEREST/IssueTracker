package appcfg

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/joho/godotenv"
)

// ShutdownConfig 는 graceful shutdown 시 대기 시간 설정입니다.
//
// 배경: claudegen LLM 추출기 활성 시 in-flight Extract 호출 latency 가 p95 110s 까지 늘어나,
// 기존 hardcode 30s shutdownCtx 가 정기적으로 deadline exceeded 를 트리거. 운영자가
// 사용 중인 LLM provider latency 에 맞춰 timeout 을 조정할 수 있도록 외부화.
type ShutdownConfig struct {
	// Timeout: stages.Stop / worker.Stop 전체 timeout (default 30s).
	// claudegen 활성 환경에서는 120s 권장.
	Timeout time.Duration

	// ClaudegenTimeout: claudegen 컨테이너 cleanup (docker rm -f) timeout (default 10s).
	// docker 명령 자체는 빠르나, daemon 응답 지연 케이스 대비 환경변수화.
	ClaudegenTimeout time.Duration
}

// DefaultShutdownConfig 는 기본 ShutdownConfig 를 반환합니다.
func DefaultShutdownConfig() ShutdownConfig {
	return ShutdownConfig{
		Timeout:          30 * time.Second,
		ClaudegenTimeout: 10 * time.Second,
	}
}

// LoadShutdown 은 .env 파일을 로드한 후 OS 환경변수로 ShutdownConfig 를 구성합니다.
// 지원 환경변수:
//   - SHUTDOWN_TIMEOUT (default 30s)
//   - CLAUDE_CODE_SHUTDOWN_TIMEOUT (default 10s)
func LoadShutdown(envFiles ...string) (ShutdownConfig, error) {
	if len(envFiles) == 0 {
		envFiles = []string{".env"}
	}
	if err := godotenv.Load(envFiles...); err != nil && !errors.Is(err, os.ErrNotExist) {
		return ShutdownConfig{}, fmt.Errorf("failed to load env files %v: %w", envFiles, err)
	}

	cfg := DefaultShutdownConfig()

	parseDuration := func(key string, dest *time.Duration) error {
		v := os.Getenv(key)
		if v == "" {
			return nil
		}
		d, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("parse %s %q: %w", key, v, err)
		}
		if d <= 0 {
			return fmt.Errorf("%s must be positive, got %s", key, d)
		}
		*dest = d
		return nil
	}

	if err := parseDuration("SHUTDOWN_TIMEOUT", &cfg.Timeout); err != nil {
		return ShutdownConfig{}, err
	}
	if err := parseDuration("CLAUDE_CODE_SHUTDOWN_TIMEOUT", &cfg.ClaudegenTimeout); err != nil {
		return ShutdownConfig{}, err
	}
	return cfg, nil
}
