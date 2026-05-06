// Package wiring 은 환경변수 / config 기반 LLM provider 조립 헬퍼를 제공합니다 (이슈 #276).
//
// cmd/* 바이너리가 환경변수만으로 chain provider 를 구성할 수 있도록, config 로딩 ↔ provider 생성 ↔
// 정책 wrapping 을 한 곳에 모아 재사용. llmgen / refiner 등 LLM 소비자가 동일 provider 를 공유합니다.
package wiring

import (
	"issuetracker/pkg/config"
	"issuetracker/pkg/llm"
	"issuetracker/pkg/llm/chain"
	"issuetracker/pkg/llm/policy"
	_ "issuetracker/pkg/llm/providers"
	"issuetracker/pkg/logger"
)

// BuildProvider 는 LLMConfig (환경변수) 에 따라 chain provider 를 구성합니다 (이슈 #173 단계 4-2 — 공유용).
//
// 반환값 nil 은 LLM 비활성을 의미 — 호출자가 nil 허용 분기:
//   - LLM_ENABLED=false / API key 부재 / provider 생성 실패 → nil + warn 로그
//
// llmgen 과 refiner 가 동일 provider 를 공유 — 환경변수 1세트 (LLM_*) 로 두 컴포넌트 동시 제어.
//
// 본 함수는 FixedOrder(cfg.Provider) 정책으로 단일 provider 를 사용. 후속 PR 에서 chain 확장 가능.
func BuildProvider(log *logger.Logger) llm.Provider {
	cfg, err := config.LoadLLM()
	if err != nil {
		log.WithError(err).Warn("failed to load LLM config, llm provider disabled")
		return nil
	}
	if !cfg.Enabled {
		log.Info("LLM provider disabled (LLM_ENABLED=false)")
		return nil
	}
	if cfg.APIKey == "" {
		log.WithField("provider", cfg.Provider).Warn("LLM API key missing, llm provider disabled (set GEMINI_API_KEY / OPENAI_API_KEY / ANTHROPIC_API_KEY)")
		return nil
	}

	provider, err := llm.New(llm.Config{
		Provider: cfg.Provider,
		APIKey:   cfg.APIKey,
		Model:    cfg.Model,
		Timeout:  cfg.Timeout,
	})
	if err != nil {
		log.WithError(err).WithField("provider", cfg.Provider).Warn("failed to construct LLM provider")
		return nil
	}

	pol := policy.NewFixedOrder(cfg.Provider)
	composed := chain.NewWithPolicy(pol, []llm.Provider{provider}, chain.WithPolicyLogger(log))

	log.WithFields(map[string]interface{}{
		"provider": cfg.Provider,
		"model":    cfg.Model,
		"timeout":  cfg.Timeout.String(),
	}).Info("LLM provider enabled (FixedOrder policy — see issue #149 follow-up for chain expansion)")

	return composed
}
