package refiner

import (
	"github.com/prometheus/client_golang/prometheus"

	"issuetracker/internal/processor/parser/rule"
	"issuetracker/internal/storage"
	"issuetracker/pkg/config"
	"issuetracker/pkg/llm"
	"issuetracker/pkg/logger"
)

// Build 는 RefinementConfig + LLM provider 로 Refiner 를 구성합니다 (이슈 #173 단계 4-2).
//
// 반환값 nil 은 정밀화 비활성을 의미 — REFINEMENT_ENABLED=false 또는 config load 실패 시.
// LLM provider 는 nil 허용 — algorithm-only 모드로 동작.
// metricsRegistry 는 nil 허용 — METRICS_ADDR 빈 값으로 endpoint 비활성인 환경에서 noop (PR #191 피드백).
//
// 본 함수는 cmd/* 바이너리의 wiring 헬퍼 — main.go 에서 도메인 logic 분리 (이슈 #276).
func Build(
	provider llm.Provider,
	rules storage.ParsingRuleRepository,
	samples storage.SampleURLRepository,
	resolver *rule.Resolver,
	metricsRegistry *prometheus.Registry,
	log *logger.Logger,
) *Refiner {
	cfg, err := config.LoadRefinement()
	if err != nil {
		log.WithError(err).Warn("failed to load refinement config, refiner disabled")
		return nil
	}
	if !cfg.Enabled {
		log.Info("refiner disabled (REFINEMENT_ENABLED=false)")
		return nil
	}

	opts := []Option{
		WithInterval(cfg.Interval),
		WithMinSamples(cfg.MinSamples),
		WithMetrics(NewMetrics(metricsRegistry)),
	}
	if provider != nil {
		adapter, err := NewLLMAdapter(provider)
		if err != nil {
			log.WithError(err).Fatal("failed to construct refiner LLM adapter")
		}
		opts = append(opts, WithLLMClient(adapter))
	}
	r, err := New(rules, samples, resolver, log, opts...)
	if err != nil {
		log.WithError(err).Fatal("failed to construct refiner")
	}
	return r
}
