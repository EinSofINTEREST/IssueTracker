package bus

import (
	"context"
	"time"

	"issuetracker/pkg/logger"
)

// PriorityRulesLoader 는 host/path priority 룰을 외부 store (DB 등) 에서 로드하는 콜백입니다.
//
// main.go 가 ParserRuleRepository 에 의존하여 본 콜백을 주입합니다 — bus 패키지가 storage
// 패키지에 직접 의존하지 않도록 layering 보존 (이슈 #521).
type PriorityRulesLoader func(ctx context.Context) ([]HostPathPriorityRule, error)

// PriorityRulesRefresher 는 RuleBasedPriorityResolver 의 host/path 룰을 주기적으로 hydrate 합니다.
//
// 부팅 직후 1회 즉시 로드 + interval 마다 ticker 로 refresh. ctx 취소 시 graceful 종료.
//
// 운영자가 parser_rules.crawl_priority 를 변경하면 다음 refresh tick 에 자동 반영 — TTL 5분
// default 이므로 운영 반영 지연 최대 5분.
type PriorityRulesRefresher struct {
	resolver *RuleBasedPriorityResolver
	loader   PriorityRulesLoader
	interval time.Duration
	log      *logger.Logger
}

// NewPriorityRulesRefresher 는 refresher 인스턴스를 생성합니다.
// interval <= 0 이면 5분 default.
func NewPriorityRulesRefresher(
	resolver *RuleBasedPriorityResolver,
	loader PriorityRulesLoader,
	interval time.Duration,
	log *logger.Logger,
) *PriorityRulesRefresher {
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	return &PriorityRulesRefresher{
		resolver: resolver,
		loader:   loader,
		interval: interval,
		log:      log,
	}
}

// Start 는 부팅 직후 1회 즉시 hydrate + ticker goroutine 을 기동합니다.
//
// 부팅 시점 hydrate 실패는 WARN 로그 + 기동 진행 — refresher 가 없는 상태로 시스템이 흘러
// fallback 우선순위 (Normal) 로 동작. 다음 tick 에서 자연 복구.
//
// goroutine 은 ctx.Done() 시 즉시 종료. graceful shutdown 신호 그대로 흡수.
func (r *PriorityRulesRefresher) Start(ctx context.Context) {
	r.refreshOnce(ctx)

	go func() {
		ticker := time.NewTicker(r.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				r.refreshOnce(ctx)
			}
		}
	}()
}

// refreshOnce 는 loader 호출 + resolver 의 host/path 룰 atomic 교체.
//
// loader 에러 시 WARN 로그 + 기존 룰 유지 (silent skip) — 다음 tick 에서 자연 복구.
func (r *PriorityRulesRefresher) refreshOnce(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	rules, err := r.loader(ctx)
	if err != nil {
		if r.log != nil {
			r.log.WithError(err).Warn("priority rules refresh failed, retaining previous rules")
		}
		return
	}
	r.resolver.SetHostPathRules(rules)
	if r.log != nil {
		r.log.WithField("rule_count", len(rules)).Debug("priority rules refreshed")
	}
}
