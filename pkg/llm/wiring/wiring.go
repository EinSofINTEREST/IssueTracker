// Package wiring 은 환경변수 / config 기반 LLM provider 조립 헬퍼를 제공합니다.
//
// cmd/* 바이너리가 환경변수만으로 chain provider 를 구성할 수 있도록, config 로딩 ↔ provider 생성 ↔
// 정책 wrapping 을 한 곳에 모아 재사용. llmgen / refiner 등 LLM 소비자가 동일 provider 를 공유합니다.
package wiring

import (
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"

	"github.com/prometheus/client_golang/prometheus"

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
// LLM_POLICY=chain (default) 일 때 적용. 다른 정책 (hybrid / latency / cheapest) 은 metric /
// capability 기반으로 동적 정렬.
var fallbackOrder = []string{"gemini", "openai", "anthropic"}

// metricsLabelPrefix 는 MeasuredFactory 가 collector 이름에 prepend 하는 prefix 입니다.
// /metrics endpoint 에서 "issuetracker_llm_*" 메트릭으로 노출 — 다른 도메인 메트릭과 충돌 회피.
const metricsLabelPrefix = "issuetracker_llm"

// Options 는 BuildProviderWithOptions 의 선택 인자입니다.
//
// 모든 필드 zero value 면 BuildProvider 의 default 동작과 동일.
type Options struct {
	// PrometheusRegistry: nil 이면 Prometheus collector 등록 skip — in-memory EMA / Stats 만 유지
	// (LatencyWeighted / Hybrid 정책은 그대로 동작). cmd/* 가 metrics endpoint 를 노출하는 경우 전달.
	PrometheusRegistry *prometheus.Registry
}

// BuildProvider 는 backward-compatible wrapper — Prometheus registry 없이 default option 으로 wiring 합니다.
//
// 신규 wiring 은 BuildProviderWithOptions 를 직접 호출하여 metrics registry 를 전달.
func BuildProvider(log *logger.Logger) llm.Provider {
	return BuildProviderWithOptions(log, Options{})
}

// BuildProviderWithOptions 는 LLMConfig (환경변수) + opts 에 따라 multi-provider chain 을 구성합니다.
//
// 각 provider 는 inner → outer 순으로 다음 decorator 로 wrap 됩니다:
//  1. RealProvider (gemini / openai / anthropic)
//  2. RetryProvider — rate_limit / network 백오프 재시도 (이슈 #215)
//  3. MeasuredProvider — per-call latency EMA + Prometheus 메트릭 + Stats (이슈 #170)
//
// candidates 가 *MeasuredProvider 이므로 정책 (LatencyWeighted / Hybrid) 의 type assertion 이 hit —
// 동적 metric 기반 정렬 가능.
//
// LLM_API_KEY fallback 은 primary (cfg.Provider) 1건에만 적용 — backward compat.
// LLM_PROVIDER 의 alias (claude → anthropic) 는 normalizePrimary 가 정규화.
//
// 정책 선택 (LLM_POLICY, 이슈 #170):
//   - "chain" (default) : FixedOrder(fallbackOrder...) — gemini → openai → anthropic 정적 순서
//   - "cheapest"        : CheapestFirst(caps) — Capabilities.CostInputPer1M 오름차순
//   - "latency"         : LatencyWeighted(caps) — Stats.LatencyMs() EMA 오름차순 (이력 없으면 Capabilities baseline)
//   - "hybrid"          : Hybrid(caps, weights) — cost + latency + failure_rate 가중 합산
//     가중치는 LLM_HYBRID_WEIGHTS 환경변수로 override (default 1.0,1.0,0.5)
//
// 반환값 nil 은 LLM 비활성을 의미 — 호출자가 nil 허용 분기:
//   - LLM_ENABLED=false → nil
//   - 모든 provider 가 API key 부재 / 생성 실패 → nil + warn
//
// llmgen 과 refiner 가 동일 provider 를 공유 — 환경변수 1세트 (LLM_*) 로 두 컴포넌트 동시 제어.
func BuildProviderWithOptions(log *logger.Logger, opts Options) llm.Provider {
	cfg, err := config.LoadLLM()
	if err != nil {
		log.WithError(err).Warn("failed to load LLM config, llm provider disabled")
		return nil
	}
	if !cfg.Enabled {
		log.Info("LLM provider disabled (LLM_ENABLED=false)")
		return nil
	}

	primaryName := normalizePrimary(cfg.Provider)

	// MeasuredFactory 는 모든 후보가 공유 — 단일 collector set 으로 등록.
	// PrometheusRegistry 가 nil 이면 collector 등록 skip — in-memory EMA / Stats 는 그대로 동작.
	measuredFactory := llm.NewMeasuredFactory(opts.PrometheusRegistry, metricsLabelPrefix)

	candidates := make([]llm.Provider, 0, len(fallbackOrder))
	activeNames := make([]string, 0, len(fallbackOrder))
	for _, name := range fallbackOrder {
		apiKey := lookupProviderAPIKey(name)
		// LLM_API_KEY fallback 은 primary (LLM_PROVIDER) 에만 적용 — backward compat.
		if apiKey == "" && name == primaryName {
			apiKey = cfg.APIKey
		}
		if apiKey == "" {
			log.WithField("provider", name).Debug("skipping provider — no API key configured")
			continue
		}
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
		// RetryProvider — rate_limit / network 발생 시 chain fallback 전에 같은 provider 에서
		// backoff 재시도 (이슈 #215). default 정책 (RateLimit 5회/10s + Network 3회/1s).
		retryWrapped := llm.NewRetryProvider(p, llm.RetryProviderOptions{})
		// MeasuredProvider — per-call latency EMA + Prometheus 메트릭 (이슈 #170).
		// outer wrap 으로 retries 포함한 end-to-end 시간을 기록 — 정책이 보는 latency 는 실 사용자 경험.
		measured := measuredFactory.Wrap(retryWrapped)
		candidates = append(candidates, measured)
		activeNames = append(activeNames, name)
	}

	if len(candidates) == 0 {
		log.Warn("LLM provider disabled — no API keys configured (set GEMINI_API_KEY / OPENAI_API_KEY / ANTHROPIC_API_KEY)")
		return nil
	}

	pol, polName := selectPolicy(log)
	composed := chain.NewWithPolicy(pol, candidates, chain.WithPolicyLogger(log))

	// first_in_candidates 는 정책 적용 전 후보 목록의 첫 항목 — FixedOrder 일 때만 실제 첫 시도 대상과
	// 일치. 동적 정책 (cheapest / latency / hybrid) 은 매 호출마다 policy.Select 가 정렬을 변경하므로
	// 실제 첫 시도는 chain Generate 시점 로그에 의존 (Copilot 피드백 #342).
	log.WithFields(map[string]interface{}{
		"chain":               activeNames,
		"first_in_candidates": activeNames[0],
		"configured_primary":  primaryName,
		"policy":              polName,
		"timeout":             cfg.Timeout.String(),
		"metrics_registered":  opts.PrometheusRegistry != nil,
	}).Info("LLM provider chain enabled")

	return composed
}

// selectPolicy 는 LLM_POLICY 환경변수에 따라 정책을 생성합니다 — invalid 값은 default 로 fallback.
//
// 반환: (Policy, name) — name 은 로그용 식별자.
func selectPolicy(log *logger.Logger) (policy.Policy, string) {
	raw := strings.ToLower(strings.TrimSpace(os.Getenv("LLM_POLICY")))
	switch raw {
	case "", "chain", "fixed":
		// 기본값 — FixedOrder(fallbackOrder...) 로 gemini → openai → anthropic 정적 순서.
		return policy.NewFixedOrder(fallbackOrder...), "FixedOrder"
	case "cheapest":
		return policy.NewCheapestFirst(llm.NewStaticCapabilitiesProvider()), "CheapestFirst"
	case "latency":
		return policy.NewLatencyWeighted(llm.NewStaticCapabilitiesProvider()), "LatencyWeighted"
	case "hybrid":
		weights, werr := loadHybridWeights()
		if werr != nil {
			log.WithError(werr).Warn("invalid LLM_HYBRID_WEIGHTS — falling back to default")
			weights = policy.DefaultHybridWeights()
		}
		return policy.NewHybrid(llm.NewStaticCapabilitiesProvider(), weights), "Hybrid"
	default:
		log.WithField("requested", raw).Warn("unknown LLM_POLICY — falling back to chain (FixedOrder)")
		return policy.NewFixedOrder(fallbackOrder...), "FixedOrder"
	}
}

// loadHybridWeights 는 LLM_HYBRID_WEIGHTS 환경변수 (예: "1.0,1.0,0.5") 를 파싱합니다.
//
// 미설정 / 빈 값 이면 DefaultHybridWeights 반환. 형식 오류면 error + caller 가 default fallback.
func loadHybridWeights() (policy.HybridWeights, error) {
	v := strings.TrimSpace(os.Getenv("LLM_HYBRID_WEIGHTS"))
	if v == "" {
		return policy.DefaultHybridWeights(), nil
	}
	parts := strings.Split(v, ",")
	if len(parts) != 3 {
		return policy.HybridWeights{}, fmt.Errorf("invalid LLM_HYBRID_WEIGHTS: expected 3 comma-separated floats (cost,latency,failure_rate), got %q", v)
	}
	parseFloat := func(s, label string) (float64, error) {
		f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		if err != nil {
			return 0, fmt.Errorf("invalid LLM_HYBRID_WEIGHTS: parse %s weight %q: %w", label, s, err)
		}
		// NaN / ±Inf 는 Hybrid 정책의 normalize 계산을 깨뜨림 (NaN 전파) — 명시 거부 (gemini Medium 반영).
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return 0, fmt.Errorf("invalid LLM_HYBRID_WEIGHTS: %s weight must be a finite number, got %v", label, f)
		}
		if f < 0 {
			return 0, fmt.Errorf("invalid LLM_HYBRID_WEIGHTS: %s weight must be non-negative, got %v", label, f)
		}
		return f, nil
	}
	cost, err := parseFloat(parts[0], "cost")
	if err != nil {
		return policy.HybridWeights{}, err
	}
	latency, err := parseFloat(parts[1], "latency")
	if err != nil {
		return policy.HybridWeights{}, err
	}
	failure, err := parseFloat(parts[2], "failure_rate")
	if err != nil {
		return policy.HybridWeights{}, err
	}
	return policy.HybridWeights{Cost: cost, Latency: latency, FailureRate: failure}, nil
}
