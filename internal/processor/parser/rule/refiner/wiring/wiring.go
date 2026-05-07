// Package wiring 은 cmd/* 바이너리가 환경 의존성 (config 등) 을 주입하여 refiner.Refiner 를
// 구성하는 헬퍼를 제공합니다.
//
// refiner 도메인 패키지는 pkg/config 에 의존하지 않으며, 본 wiring 서브패키지가 그 결합을 흡수 —
// 도메인 로직과 인프라 설정의 분리.
package wiring

import (
	"fmt"

	"github.com/prometheus/client_golang/prometheus"

	"issuetracker/internal/processor/parser/rule"
	"issuetracker/internal/processor/parser/rule/refiner"
	"issuetracker/internal/storage"
	"issuetracker/pkg/config"
	"issuetracker/pkg/llm"
	"issuetracker/pkg/logger"
)

// Build 는 RefinementConfig + LLM provider 로 refiner.Refiner 를 구성합니다.
//
// 반환값 (nil, nil) 은 정밀화 비활성을 의미 — REFINEMENT_ENABLED=false 인 경우에 한정.
// config load 실패는 (nil, error) — malformed env 가 silent disable 되지 않도록 명시적 에러.
// LLM provider 는 nil 허용 — algorithm-only 모드로 동작.
// metricsRegistry 는 nil 허용 — METRICS_ADDR 빈 값으로 endpoint 비활성인 환경에서 noop.
//
// 에러 반환 정책: 도메인 wiring 실패는 main 이 Fatal 결정 — 본 함수는 wrap 후 return.
func Build(
	provider llm.Provider,
	rules storage.ParsingRuleRepository,
	samples storage.SampleURLRepository,
	resolver *rule.Resolver,
	metricsRegistry *prometheus.Registry,
	log *logger.Logger,
) (*refiner.Refiner, error) {
	cfg, err := config.LoadRefinement()
	if err != nil {
		// malformed env 가 silent 로 refiner 를 끄지 않도록 명시적 에러 반환.
		// (nil, nil) 은 explicit !cfg.Enabled 경로에 한정 — 호출자가 Fatal 결정.
		return nil, fmt.Errorf("load refinement config: %w", err)
	}
	if !cfg.Enabled {
		log.Info("refiner disabled (REFINEMENT_ENABLED=false)")
		return nil, nil
	}

	opts := []refiner.Option{
		refiner.WithInterval(cfg.Interval),
		refiner.WithMinSamples(cfg.MinSamples),
		refiner.WithMetrics(refiner.NewMetrics(metricsRegistry)),
	}
	if provider != nil {
		adapter, err := refiner.NewLLMAdapter(provider)
		if err != nil {
			return nil, fmt.Errorf("construct refiner LLM adapter: %w", err)
		}
		opts = append(opts, refiner.WithLLMClient(adapter))
	}
	r, err := refiner.New(rules, samples, resolver, log, opts...)
	if err != nil {
		return nil, fmt.Errorf("construct refiner: %w", err)
	}
	return r, nil
}
