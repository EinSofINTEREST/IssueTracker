package llmcfg

import (
	"errors"
	"fmt"
	"os"

	"github.com/joho/godotenv"

	"issuetracker/pkg/config/internal/parse"
)

// CallBudgetConfig 는 LLM rule generator 의 호출 상한입니다 (이슈 #169).
//
// rule generator 는 룰이 없는 host 를 만날 때마다 LLM 을 호출합니다. 신규 사이트가 몰리거나
// 룰 생성이 반복 실패하면 호출이 급증해 무료 한도를 넘길 수 있습니다.
//
// 두 창을 함께 둡니다 — 일일 한도만 있으면 하루치를 한 시간에 소진해 나머지 23시간이
// 마비될 수 있습니다.
type CallBudgetConfig struct {
	// DailyCap: 직전 24시간 최대 LLM 호출 수. 0 이면 무제한.
	// 환경변수 LLM_DAILY_CAP (default 1000 — Gemini 무료 한도 기준).
	DailyCap int

	// HourlyCap: 직전 1시간 최대 LLM 호출 수. 0 이면 무제한.
	// 환경변수 LLM_HOURLY_CAP (default 100).
	HourlyCap int
}

// DefaultCallBudgetConfig 는 기본 상한을 반환합니다.
func DefaultCallBudgetConfig() CallBudgetConfig {
	return CallBudgetConfig{DailyCap: 1000, HourlyCap: 100}
}

// LoadCallBudget 는 .env 로드 후 환경변수로 CallBudgetConfig 를 구성합니다.
func LoadCallBudget(envFiles ...string) (CallBudgetConfig, error) {
	if len(envFiles) == 0 {
		envFiles = []string{".env"}
	}
	if err := godotenv.Load(envFiles...); err != nil && !errors.Is(err, os.ErrNotExist) {
		return CallBudgetConfig{}, fmt.Errorf("failed to load env files %v: %w", envFiles, err)
	}

	cfg := DefaultCallBudgetConfig()

	for _, op := range []error{
		parse.NonNegativeInt("LLM_DAILY_CAP", &cfg.DailyCap),
		parse.NonNegativeInt("LLM_HOURLY_CAP", &cfg.HourlyCap),
	} {
		if op != nil {
			return CallBudgetConfig{}, op
		}
	}

	// 시간당 한도가 일일 한도보다 크면 사실상 무의미 — 운영자의 오타일 가능성이 높아 거부한다.
	if cfg.DailyCap > 0 && cfg.HourlyCap > cfg.DailyCap {
		return CallBudgetConfig{}, fmt.Errorf(
			"LLM_HOURLY_CAP (%d) must not exceed LLM_DAILY_CAP (%d)", cfg.HourlyCap, cfg.DailyCap)
	}

	return cfg, nil
}
