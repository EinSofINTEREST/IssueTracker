package bus

import (
	"sync/atomic"
	"time"

	"issuetracker/internal/processor/fetcher/core"
)

// HostOverride 는 한 host 의 명시적 priority override 입니다 (이슈 #383).
//
// bus 패키지가 storage 에 의존하지 않도록 model.PriorityConfig 를 그대로 받지 않고
// 필요한 값만 옮겨 담습니다 (PriorityRulesRefresher 의 loader 콜백과 같은 layering 원칙).
type HostOverride struct {
	Priority core.Priority

	// Until 이 zero 면 무기한입니다.
	Until time.Time
}

// active 는 now 기준으로 override 가 유효한지 반환합니다.
func (o HostOverride) active(now time.Time) bool {
	return o.Until.IsZero() || now.Before(o.Until)
}

// OverridePriorityResolver 는 운영자가 명시한 host priority 를 적용합니다 (이슈 #383).
//
// # 체인 위치
//
//	Explicit → Override → Source → RuleBased → Scoring → default Normal
//
// `ExplicitPriorityResolver` (발행자가 job 에 직접 명시) 보다는 뒤다 — 발행 시점의 명시가
// 더 구체적인 의도다. `RuleBased` (DB crawl_priority) 보다는 앞인데, 둘 다 운영자 설정이지만
// override 는 `Until` 을 가진 **시한부 의도** 이고, 상시 정책을 일시적으로 덮는 것이 이
// 기능의 목적이기 때문이다.
//
// # 만료 판정을 조회 시점에 하는 이유
//
// 스냅샷 적재 시점에 만료를 거르면, refresh interval 동안 **이미 만료된 override 가 계속
// 적용** 된다 (interval 이 5분이면 최대 5분). 매 조회에서 비교하면 즉시 반영되고, 비용은
// time.Now() 한 번이라 hot path 에서도 무시 가능하다.
type OverridePriorityResolver struct {
	overrides atomic.Pointer[map[string]HostOverride]

	// now 는 테스트에서 만료 경계를 제어하기 위한 시계 주입점입니다. nil 이면 time.Now.
	now func() time.Time
}

// NewOverridePriorityResolver 는 빈 resolver 를 생성합니다.
//
// 스냅샷이 주입되기 전에는 CanResolve 가 항상 false — 부팅 직후 refresher 가 첫 로드를
// 마치기 전까지는 기존 chain 동작이 그대로 유지됩니다.
func NewOverridePriorityResolver() *OverridePriorityResolver {
	return &OverridePriorityResolver{}
}

// SetClock 은 시계를 주입합니다 (테스트 전용). nil 이면 time.Now 로 되돌립니다.
func (r *OverridePriorityResolver) SetClock(now func() time.Time) { r.now = now }

func (r *OverridePriorityResolver) clock() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now()
}

// SetOverrides 는 host → override 스냅샷을 atomic 으로 교체합니다.
//
// 빈 맵 / nil 은 resolver 비활성화 — chain 이 다음 resolver 로 위임합니다.
func (r *OverridePriorityResolver) SetOverrides(overrides map[string]HostOverride) {
	if len(overrides) == 0 {
		r.overrides.Store(nil)
		return
	}
	cp := make(map[string]HostOverride, len(overrides))
	for k, v := range overrides {
		cp[k] = v
	}
	r.overrides.Store(&cp)
}

// Resolve 는 override priority 를 반환합니다.
//
// CanResolve 가 false 인 상태에서 호출되면 (chain 이 보장하지만 방어적으로) Normal.
func (r *OverridePriorityResolver) Resolve(job *core.CrawlJob) core.Priority {
	o, ok := r.lookup(job)
	if !ok {
		return core.PriorityNormal
	}
	return o.Priority
}

// CanResolve 는 job 의 host 에 **유효한** override 가 있는 경우에만 true 를 반환합니다.
// 만료된 override 는 없는 것과 같이 취급해 chain 에 위임합니다.
func (r *OverridePriorityResolver) CanResolve(job *core.CrawlJob) bool {
	_, ok := r.lookup(job)
	return ok
}

func (r *OverridePriorityResolver) lookup(job *core.CrawlJob) (HostOverride, bool) {
	if job == nil {
		return HostOverride{}, false
	}
	m := r.overrides.Load()
	if m == nil {
		return HostOverride{}, false
	}
	host := hostOf(job.Target.URL)
	if host == "" {
		return HostOverride{}, false
	}
	o, ok := (*m)[host]
	if !ok || !o.active(r.clock()) {
		return HostOverride{}, false
	}
	return o, true
}
