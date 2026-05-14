// config 패키지는 .env 파일과 환경변수를 통해 IssueTracker 컴포넌트 설정을
// 중앙에서 관리합니다. godotenv.Load() 후 OS 환경변수가 우선 적용됩니다.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"

	"github.com/joho/godotenv"
)

// LogConfig는 로거 설정을 나타냅니다.
type LogConfig struct {
	Level  string // LOG_LEVEL: debug | info | warn | error (default: info)
	Pretty bool   // LOG_PRETTY: true | false (default: false)
}

// DefaultLogConfig는 기본 LogConfig를 반환합니다.
func DefaultLogConfig() LogConfig {
	return LogConfig{
		Level:  "info",
		Pretty: false,
	}
}

// LoadLog는 .env 파일을 로드한 후 OS 환경변수로 LogConfig를 구성합니다.
// 지원 환경변수: LOG_LEVEL (debug|info|warn|error), LOG_PRETTY (true|false)
func LoadLog(envFiles ...string) (LogConfig, error) {
	if len(envFiles) == 0 {
		envFiles = []string{".env"}
	}
	if err := godotenv.Load(envFiles...); err != nil && !errors.Is(err, os.ErrNotExist) {
		return LogConfig{}, fmt.Errorf("failed to load env files %v: %w", envFiles, err)
	}

	cfg := DefaultLogConfig()

	if v := os.Getenv("LOG_LEVEL"); v != "" {
		switch v {
		case "debug", "info", "warn", "error":
			cfg.Level = v
		default:
			return LogConfig{}, fmt.Errorf("invalid LOG_LEVEL %q: must be one of debug, info, warn, error", v)
		}
	}
	if v := os.Getenv("LOG_PRETTY"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return LogConfig{}, fmt.Errorf("parse LOG_PRETTY %q: %w", v, err)
		}
		cfg.Pretty = b
	}

	return cfg, nil
}
