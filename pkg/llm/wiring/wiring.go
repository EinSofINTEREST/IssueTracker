// Package wiring 은 환경변수 / config 기반 LLM provider 조립 헬퍼를 제공합니다.
//
// cmd/* 바이너리가 환경변수만으로 chain provider 를 구성할 수 있도록, config 로딩 ↔ provider 생성 ↔
// 정책 wrapping 을 한 곳에 모아 재사용. llmgen / refiner 등 LLM 소비자가 동일 provider 를 공유합니다.
package wiring

import (
	"os"

	"issuetracker/pkg/config"
	"issuetracker/pkg/llm"
	"issuetracker/pkg/llm/chain"
	"issuetracker/pkg/llm/policy"
	_ "issuetracker/pkg/llm/providers"
	"issuetracker/pkg/logger"
)

// lookupProviderAPIKey 는 provider 별 표준 환경변수의 *값* 을 반환합니다.
//
// config 의 lookupLLMAPIKey 는 미발견 시 LLM_API_KEY 로 cascade 하여 모든 provider 에 동일 key 가
// 적용되는 부작용이 있으므로, chain 구성 시에는 본 helper 로 provider 별 key 만 직접 조회합니다.
// LLM_API_KEY fallback 은 primary (cfg.Provider) 1건에만 적용 — backward compat 보존.
func lookupProviderAPIKey(name string) string {
	switch name {
	case "gemini":
		return os.Getenv("GEMINI_API_KEY")
	case "openai":
		return os.Getenv("OPENAI_API_KEY")
	case "anthropic", "claude":
		return os.Getenv("ANTHROPIC_API_KEY")
	}
	return ""
}

// normalizePrimary 는 LLM_PROVIDER 의 alias 를 fallbackOrder 의 정식 이름으로 정규화합니다.
//
// 예: cfg.Provider="claude" → "anthropic" 으로 매핑하여 chain 의 anthropic 항목에 LLM_API_KEY
// fallback / LLM_MODEL override 가 정확히 적용되도록 보장.
func normalizePrimary(name string) string {
	if name == "claude" {
		return "anthropic"
	}
	return name
}

// fallbackOrder 는 fixed-order fallback chain 의 시도 순서입니다.
//
// 현재는 gemini → openai → anthropic 으로 hardcoded — 비용 / 한도 / 가용성 우선순위 기준.
// 향후 capability 기반 metric 정책 (cost / latency / 성공률) 도입 시 본 슬라이스 대신
// pkg/llm/policy 의 dynamic policy (CheapestFirst / Latency / Hybrid 등) 로 NewFixedOrder 만 교체.
var fallbackOrder = []string{"gemini", "openai", "anthropic"}

// BuildProvider 는 LLMConfig (환경변수) 에 따라 fixed-order fallback chain 을 구성합니다.
//
// fallback 순서: gemini → openai → anthropic (hardcoded — 향후 metric 기반 정책으로 대체).
// 각 provider 는 자신의 API key 가 환경변수에 설정된 경우에만 chain 후보에 포함:
//   - GEMINI_API_KEY / OPENAI_API_KEY / ANTHROPIC_API_KEY 직접 lookup
//
// LLM_API_KEY 의 역할 (backward compat — primary 1건 한정):
//   - primary (LLM_PROVIDER 가 가리키는 provider) 가 자신의 specific key 부재인 경우에만 LLM_API_KEY 적용.
//   - 다른 provider 에는 cascade 되지 않음 — 잘못된 key 가 chain 에 채워져 auth 실패로 시간 낭비하는 부작용 회피.
//
// 반환값 nil 은 LLM 비활성을 의미 — 호출자가 nil 허용 분기:
//   - LLM_ENABLED=false → nil
//   - 모든 provider 가 API key 부재 / 생성 실패 → nil + warn
//
// 정책 적용:
//   - policy.NewFixedOrder(fallbackOrder...) 로 후보를 정해진 순서로 정렬
//   - 향후 metric 기반 정책 도입 시 본 함수의 policy 객체만 교체하면 됨
//
// LLM_PROVIDER / LLM_MODEL 의 역할 (backward compat):
//   - LLM_PROVIDER 와 일치하는 provider 에는 LLM_MODEL 이 적용됨 (claude alias 포함)
//   - 그 외 provider 는 자체 default model 사용 (gemini-2.5-flash / gpt-4o-mini / claude-opus-4-7)
//
// llmgen 과 refiner 가 동일 provider 를 공유 — 환경변수 1세트 (LLM_*) 로 두 컴포넌트 동시 제어.
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

	// LLM_PROVIDER alias (예: "claude" → "anthropic") 정규화 — fallbackOrder 의 정식 이름과 매칭하여
	// LLM_API_KEY fallback / LLM_MODEL override 가 올바른 chain 항목에 적용되도록 보장.
	primaryName := normalizePrimary(cfg.Provider)

	candidates := make([]llm.Provider, 0, len(fallbackOrder))
	activeNames := make([]string, 0, len(fallbackOrder))
	for _, name := range fallbackOrder {
		apiKey := lookupProviderAPIKey(name)
		// LLM_API_KEY fallback 은 primary (LLM_PROVIDER) 에만 적용 — backward compat.
		// 다른 provider 에 같은 key 를 cascade 하면 chain 이 잘못 채워져 auth 실패 후 다음 시도로 낭비.
		if apiKey == "" && name == primaryName {
			apiKey = cfg.APIKey
		}
		if apiKey == "" {
			log.WithField("provider", name).Debug("skipping provider — no API key configured")
			continue
		}
		// 사용자가 LLM_PROVIDER 로 지정한 primary 에는 LLM_MODEL override 적용. 나머지는 provider default.
		model := ""
		if name == primaryName {
			model = cfg.Model
		}
		p, perr := llm.New(llm.Config{
			Provider: name,
			APIKey:   apiKey,
			Model:    model,
			Timeout:  cfg.Timeout,
		})
		if perr != nil {
			log.WithError(perr).WithField("provider", name).Warn("failed to construct LLM provider, skipping")
			continue
		}
		// 각 provider 를 RetryProvider 로 감싸 — rate_limit / network 발생 시 chain fallback 전에
		// 같은 provider 에서 backoff 재시도 (이슈 #215). chain 은 RetryProvider 가 모든 시도 소진
		// 후에야 다음 provider 로 fallthrough — backoff 정책은 default (RateLimit 5회/10s + Network 3회/1s).
		p = llm.NewRetryProvider(p, llm.RetryProviderOptions{})
		candidates = append(candidates, p)
		activeNames = append(activeNames, name)
	}

	if len(candidates) == 0 {
		log.Warn("LLM provider disabled — no API keys configured (set GEMINI_API_KEY / OPENAI_API_KEY / ANTHROPIC_API_KEY)")
		return nil
	}

	// FixedOrder 정책 — fallbackOrder 의 순서를 후보에 강제. 향후 metric 정책으로 교체 시 본 줄만 변경.
	pol := policy.NewFixedOrder(fallbackOrder...)
	composed := chain.NewWithPolicy(pol, candidates, chain.WithPolicyLogger(log))

	// 로그 필드 의미:
	//   - chain          : 실제 활성화되어 시도되는 provider 시퀀스 (정책 적용 전 후보 목록)
	//   - first_in_chain : chain[0] — 매 호출의 첫 시도 대상 (디버깅용)
	//   - configured_primary : LLM_PROVIDER 가 가리키는 사용자 의도 (alias 정규화 후)
	//                          — first_in_chain 과 다를 수 있음 (key 부재 등으로 chain 에서 제외된 경우)
	log.WithFields(map[string]interface{}{
		"chain":              activeNames,
		"first_in_chain":     activeNames[0],
		"configured_primary": primaryName,
		"policy":             "FixedOrder",
		"timeout":            cfg.Timeout.String(),
	}).Info("LLM provider chain enabled (fixed order — gemini → openai → anthropic; metric policy 후속 PR)")

	return composed
}
