package llmcfg

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/joho/godotenv"
)

// GoogleCSEConfig 는 Google Custom Search Engine API client wiring 설정입니다.
//
// 환경변수:
//   - GOOGLE_CSE_API_KEY (필수, 빈 문자열이면 client 미생성 → search handler 비활성화)
//   - GOOGLE_CSE_CX      (필수, search engine ID)
//   - GOOGLE_CSE_TIMEOUT (default 10s)
//
// 운영 정책:
//   - APIKey 또는 CX 가 미설정이면 cmd 측 wiring 이 search handler 를 skip (warn 로그) — search
//     기능은 optional, 미설정 환경에서도 fetcher / parser / validate 흐름은 정상.
type GoogleCSEConfig struct {
	APIKey  string
	CX      string
	Timeout time.Duration
}

// DefaultGoogleCSEConfig 는 기본 GoogleCSEConfig 를 반환합니다 (Timeout 만 default).
func DefaultGoogleCSEConfig() GoogleCSEConfig {
	return GoogleCSEConfig{Timeout: 10 * time.Second}
}

// LoadGoogleCSE 는 .env 파일을 로드한 후 OS 환경변수로 GoogleCSEConfig 를 구성합니다.
//
// 빈 APIKey / CX 는 에러 아님 — 호출자가 IsConfigured 로 분기.
func LoadGoogleCSE(envFiles ...string) (GoogleCSEConfig, error) {
	if len(envFiles) == 0 {
		envFiles = []string{".env"}
	}
	if err := godotenv.Load(envFiles...); err != nil && !errors.Is(err, os.ErrNotExist) {
		return GoogleCSEConfig{}, fmt.Errorf("failed to load env files %v: %w", envFiles, err)
	}

	cfg := DefaultGoogleCSEConfig()
	cfg.APIKey = os.Getenv("GOOGLE_CSE_API_KEY")
	cfg.CX = os.Getenv("GOOGLE_CSE_CX")

	if v := os.Getenv("GOOGLE_CSE_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return GoogleCSEConfig{}, fmt.Errorf("parse GOOGLE_CSE_TIMEOUT %q: %w", v, err)
		}
		if d <= 0 {
			return GoogleCSEConfig{}, fmt.Errorf("GOOGLE_CSE_TIMEOUT must be positive, got %s", d)
		}
		cfg.Timeout = d
	}

	return cfg, nil
}

// IsConfigured 는 APIKey + CX 가 모두 set 되었는지 여부를 반환합니다.
//
// false 면 cmd 가 search handler 를 wire 하지 않음 — search 기능 optional 정책.
func (c GoogleCSEConfig) IsConfigured() bool {
	return c.APIKey != "" && c.CX != ""
}
