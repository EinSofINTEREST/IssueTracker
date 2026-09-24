package bus

import (
	"context"
	"time"

	"issuetracker/pkg/logger"
)

// HostOverridesLoader 는 host override 스냅샷을 외부 store 에서 로드하는 콜백입니다.
//
// PriorityRulesLoader 와 같은 layering 원칙 — bus 패키지가 storage 에 직접 의존하지 않도록
// main.go 가 콜백을 주입합니다 (이슈 #383).
type HostOverridesLoader func(ctx context.Context) (map[string]HostOverride, error)

// HostOverridesRefresher 는 OverridePriorityResolver 의 스냅샷을 주기적으로 hydrate 합니다.
//
// 부팅 직후 1회 즉시 로드 + interval 마다 refresh. PriorityRulesRefresher 와 동일 구조.
//
// 운영자가 fetcher_rules.priority_config 를 변경하면 다음 refresh 에 반영됩니다.
// **만료는 refresh 를 기다리지 않습니다** — resolver 가 조회 시점에 판정합니다.
type HostOverridesRefresher struct {
	resolver *OverridePriorityResolver
	loader   HostOverridesLoader
	interval time.Duration
	log      *logger.Logger
}

// NewHostOverridesRefresher 는 refresher 를 생성합니다. interval <= 0 이면 5분.
func NewHostOverridesRefresher(
	resolver *OverridePriorityResolver,
	loader HostOverridesLoader,
	interval time.Duration,
	log *logger.Logger,
) *HostOverridesRefresher {
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	return &HostOverridesRefresher{
		resolver: resolver,
		loader:   loader,
		interval: interval,
		log:      log,
	}
}

// Start 는 부팅 직후 1회 hydrate + ticker goroutine 을 기동합니다.
func (r *HostOverridesRefresher) Start(ctx context.Context) {
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

// refreshOnce 는 loader 호출 + 스냅샷 교체.
//
// 실패 시 기존 스냅샷을 유지한다 — 비우면 DB 일시 장애가 곧바로 모든 override 해제로
// 이어져, 운영자가 High 로 고정해 둔 host 가 조용히 Normal 로 떨어진다.
func (r *HostOverridesRefresher) refreshOnce(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	overrides, err := r.loader(ctx)
	if err != nil {
		if r.log != nil {
			r.log.WithError(err).Warn("host overrides refresh failed, retaining previous snapshot")
		}
		return
	}
	r.resolver.SetOverrides(overrides)
	if r.log != nil {
		r.log.WithField("override_count", len(overrides)).Debug("host overrides refreshed")
	}
}
