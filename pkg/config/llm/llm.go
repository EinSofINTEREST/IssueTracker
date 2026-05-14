package llmcfg

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/joho/godotenv"

	"issuetracker/pkg/config/internal/parse"
)

// LLMConfig 는 LLM rule generator wiring 설정입니다.
//
// LLMConfig drives the LLM provider used for auto-generating parsing rules when
// a host has no rule registered (rule.ErrNoRule fallback).
//
// Gemini 만 사용 (1000회/일 무료 한도) + FixedOrder("gemini") 정책.
// 후속 PR (이슈 TBD) 에서 chain (gemini → openai → anthropic) 으로 확장.
type LLMConfig struct {
	// Enabled: false 면 rule generator wiring 자체 skip (ErrNoRule 잔존 동작 유지).
	// 환경변수 LLM_ENABLED (default true).
	Enabled bool

	// Provider: 사용할 backend 식별자 ("gemini" / "openai" / "anthropic"). default "gemini".
	Provider string

	// APIKey: provider API key. Provider="gemini" 면 GEMINI_API_KEY, openai 면 OPENAI_API_KEY,
	// anthropic 이면 ANTHROPIC_API_KEY 에서 자동 조회. 없으면 LLM_API_KEY fallback.
	APIKey string

	// Model: 호출 기본 모델 (provider default override). default "gemini-2.5-flash".
	Model string

	// Timeout: 단일 LLM 호출 timeout (default 60s).
	Timeout time.Duration
}

// DefaultLLMConfig 는 로컬 개발 환경용 기본 LLMConfig 를 반환합니다.
func DefaultLLMConfig() LLMConfig {
	return LLMConfig{
		Enabled:  true,
		Provider: "gemini",
		Model:    "gemini-2.5-flash",
		Timeout:  60 * time.Second,
	}
}

// LoadLLM 은 .env 를 로드한 후 OS 환경변수로 LLMConfig 를 구성합니다.
//
// 지원 환경변수:
//   - LLM_ENABLED: true | false (default true)
//   - LLM_PROVIDER: gemini | openai | anthropic (default "gemini")
//   - LLM_MODEL: provider-specific model 이름 (default "gemini-2.5-flash")
//   - LLM_TIMEOUT: Go duration (default "60s")
//   - GEMINI_API_KEY / OPENAI_API_KEY / ANTHROPIC_API_KEY: provider 별 key
//   - LLM_API_KEY: 위 provider 별 key 부재 시 fallback (테스트/통합 용)
func LoadLLM(envFiles ...string) (LLMConfig, error) {
	if len(envFiles) == 0 {
		envFiles = []string{".env"}
	}
	if err := godotenv.Load(envFiles...); err != nil && !errors.Is(err, os.ErrNotExist) {
		return LLMConfig{}, fmt.Errorf("failed to load env files %v: %w", envFiles, err)
	}

	cfg := DefaultLLMConfig()

	// 검증 강화 (이슈 #439): provider enum, 양수 timeout.
	// LLM_PROVIDER 미설정 시 default "gemini" 유지 — 빈 문자열만 skip.
	for _, op := range []error{
		parse.String("LLM_MODEL", &cfg.Model),
		parse.Bool("LLM_ENABLED", &cfg.Enabled),
		parse.Enum("LLM_PROVIDER", []string{"gemini", "openai", "anthropic", "claude"}, &cfg.Provider),
		parse.PositiveDuration("LLM_TIMEOUT", &cfg.Timeout),
	} {
		if op != nil {
			return LLMConfig{}, op
		}
	}

	cfg.APIKey = lookupLLMAPIKey(cfg.Provider)

	return cfg, nil
}

// lookupLLMAPIKey 는 provider 별 표준 환경변수에서 API key 를 조회하고,
// 부재 시 LLM_API_KEY fallback 을 사용합니다.
func lookupLLMAPIKey(provider string) string {
	switch provider {
	case "gemini":
		if v := os.Getenv("GEMINI_API_KEY"); v != "" {
			return v
		}
	case "openai":
		if v := os.Getenv("OPENAI_API_KEY"); v != "" {
			return v
		}
	case "anthropic", "claude":
		if v := os.Getenv("ANTHROPIC_API_KEY"); v != "" {
			return v
		}
	}
	return os.Getenv("LLM_API_KEY")
}
