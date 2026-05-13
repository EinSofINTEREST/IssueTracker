// Package publisher 의 priority resolver 모듈 (이슈 #391, 메타 #385 Sub 6).
//
// 구 internal/processor/fetcher/worker/resolver.go 위치에서 이동 — Kafka I/O 단일 책임
// 원칙에 따라 PublishX 메소드의 priority routing 도 publisher 가 단일 출처.
//
// publisher.go 의 PriorityResolver 인터페이스를 본 파일의 chain/impl 들이 만족.
package publisher

import (
	"issuetracker/internal/processor/fetcher/core"
)

// ─────────────────────────────────────────────────────────────────────────
// ChainablePriorityResolver — CompositeResolver 체인 등록용 인터페이스
// ─────────────────────────────────────────────────────────────────────────

// ChainablePriorityResolver 는 CompositeResolver 체인에 등록 가능한 resolver 인터페이스입니다.
//
// PriorityResolver 를 만족하면서 CanResolve 로 chain 위임 신호 제공.
// CanResolve 가 false 를 반환하면 CompositeResolver 는 체인의 다음 resolver 로 위임합니다.
type ChainablePriorityResolver interface {
	PriorityResolver

	// CanResolve 는 이 resolver 가 job 에 대해 확정적인 우선순위를 결정할 수 있는지 반환합니다.
	// false 반환 시 CompositeResolver 는 체인의 다음 resolver 로 위임합니다.
	CanResolve(job *core.CrawlJob) bool
}

// ─────────────────────────────────────────────────────────────────────────
// ExplicitPriorityResolver
// ─────────────────────────────────────────────────────────────────────────

// ExplicitPriorityResolver 는 job.Priority 에 명시된 값을 그대로 반환합니다.
//
// 발행자 (scheduler / retry / upgrade 등) 가 Priority 를 사전 명시한 경우 chain 의 다른
// resolver 로 위임하지 않고 그대로 통과. 본 resolver 를 chain 의 **1순위** 로 등록하면
// 모든 PublishX 가 resolver 를 통과하더라도 명시 priority 가 보존됩니다 (메타 #385 Sub 6).
//
// 유효하지 않은 Priority 값은 PriorityNormal 로 보정합니다.
type ExplicitPriorityResolver struct{}

// Resolve 는 job.Priority 를 검증하여 반환하고, 유효하지 않은 값은 PriorityNormal 로 대체합니다.
func (r *ExplicitPriorityResolver) Resolve(job *core.CrawlJob) core.Priority {
	switch job.Priority {
	case core.PriorityHigh, core.PriorityNormal, core.PriorityLow:
		return job.Priority
	default:
		return core.PriorityNormal
	}
}

// CanResolve 는 job.Priority 가 유효한 값으로 명시된 경우에만 true 를 반환합니다.
// 제로값 (0) 이면 발행자가 Priority 를 설정하지 않은 것으로 판단하여 false 를 반환.
func (r *ExplicitPriorityResolver) CanResolve(job *core.CrawlJob) bool {
	switch job.Priority {
	case core.PriorityHigh, core.PriorityNormal, core.PriorityLow:
		return true
	default:
		return false
	}
}

// ─────────────────────────────────────────────────────────────────────────
// SourcePriorityResolver
// ─────────────────────────────────────────────────────────────────────────

// SourcePriorityResolver 는 CrawlerName (소스 이름) 을 기준으로 우선순위를 결정합니다.
//
// 특정 소스 (예: breaking-news crawler) 가 항상 고정 우선순위를 가져야 할 때 사용 —
// job.Priority 와 무관하게 매핑된 priority 를 적용. ExplicitPriorityResolver 보다
// 뒤에 등록되면 explicit 가 우선.
type SourcePriorityResolver struct {
	rules    map[string]core.Priority
	fallback core.Priority
}

// NewSourcePriorityResolver 는 미등록 소스에 적용할 기본 우선순위를 지정하여 생성합니다.
func NewSourcePriorityResolver(fallback core.Priority) *SourcePriorityResolver {
	return &SourcePriorityResolver{
		rules:    make(map[string]core.Priority),
		fallback: fallback,
	}
}

// Register 는 크롤러 이름과 우선순위를 매핑합니다.
// 이미 등록된 이름을 재등록하면 덮어씁니다.
func (r *SourcePriorityResolver) Register(crawlerName string, priority core.Priority) {
	r.rules[crawlerName] = priority
}

// Resolve 는 job.CrawlerName 에 매핑된 우선순위를 반환합니다.
// 매핑이 없으면 생성 시 지정한 fallback 우선순위를 반환.
func (r *SourcePriorityResolver) Resolve(job *core.CrawlJob) core.Priority {
	if p, ok := r.rules[job.CrawlerName]; ok {
		return p
	}
	return r.fallback
}

// CanResolve 는 job.CrawlerName 이 등록된 소스인 경우에만 true 를 반환합니다.
// 미등록 소스는 체인의 다음 resolver 로 위임합니다.
func (r *SourcePriorityResolver) CanResolve(job *core.CrawlJob) bool {
	_, ok := r.rules[job.CrawlerName]
	return ok
}

// ─────────────────────────────────────────────────────────────────────────
// RuleBasedPriorityResolver
// ─────────────────────────────────────────────────────────────────────────

// PriorityRule 은 조건 (Match) 과 우선순위 (Priority) 를 갖는 단일 라우팅 규칙입니다.
type PriorityRule struct {
	// Match 가 true 를 반환하면 이 규칙의 Priority 를 적용합니다.
	Match    func(job *core.CrawlJob) bool
	Priority core.Priority
}

// RuleBasedPriorityResolver 는 등록된 규칙을 순서대로 평가하여 첫 번째 매치의 우선순위를 반환합니다.
type RuleBasedPriorityResolver struct {
	rules    []PriorityRule
	fallback core.Priority
}

// NewRuleBasedPriorityResolver 는 기본 우선순위를 지정하여 빈 resolver 를 생성합니다.
func NewRuleBasedPriorityResolver(fallback core.Priority) *RuleBasedPriorityResolver {
	return &RuleBasedPriorityResolver{
		fallback: fallback,
	}
}

// AddRule 은 조건-우선순위 쌍을 규칙 체인 끝에 추가합니다.
// 규칙은 추가된 순서대로 평가되며 먼저 추가된 규칙이 우선합니다.
func (r *RuleBasedPriorityResolver) AddRule(
	match func(job *core.CrawlJob) bool,
	priority core.Priority,
) {
	r.rules = append(r.rules, PriorityRule{Match: match, Priority: priority})
}

// Resolve 는 등록된 규칙을 순서대로 평가합니다.
// 매치되는 규칙이 없으면 fallback 우선순위를 반환합니다.
func (r *RuleBasedPriorityResolver) Resolve(job *core.CrawlJob) core.Priority {
	for _, rule := range r.rules {
		if rule.Match(job) {
			return rule.Priority
		}
	}
	return r.fallback
}

// CanResolve 는 등록된 규칙 중 하나라도 매치되는 경우 true 를 반환합니다.
// 규칙이 없거나 모두 매치되지 않으면 체인의 다음 resolver 로 위임합니다.
func (r *RuleBasedPriorityResolver) CanResolve(job *core.CrawlJob) bool {
	for _, rule := range r.rules {
		if rule.Match(job) {
			return true
		}
	}
	return false
}

// ─────────────────────────────────────────────────────────────────────────
// DefaultPriorityResolver — 종단 resolver
// ─────────────────────────────────────────────────────────────────────────

// DefaultPriorityResolver 는 항상 고정된 우선순위를 반환하는 종단 resolver 입니다.
//
// CompositeResolver 의 마지막 fallback 으로 사용되어 모든 job 이 반드시 우선순위를 갖도록 보장.
type DefaultPriorityResolver struct {
	priority core.Priority
}

// NewDefaultPriorityResolver 는 항상 지정된 우선순위를 반환하는 종단 resolver 를 생성합니다.
func NewDefaultPriorityResolver(priority core.Priority) *DefaultPriorityResolver {
	return &DefaultPriorityResolver{priority: priority}
}

// Resolve 는 항상 생성 시 지정한 우선순위를 반환합니다.
func (r *DefaultPriorityResolver) Resolve(_ *core.CrawlJob) core.Priority {
	return r.priority
}

// CanResolve 는 항상 true 를 반환합니다 — 종단 resolver.
func (r *DefaultPriorityResolver) CanResolve(_ *core.CrawlJob) bool {
	return true
}

// ─────────────────────────────────────────────────────────────────────────
// CompositeResolver — chain resolver
// ─────────────────────────────────────────────────────────────────────────

// CompositeResolver 는 여러 ChainablePriorityResolver 를 순서대로 평가하는 chain resolver 입니다.
//
// 각 등록된 resolver 의 CanResolve 가 true 인 첫 번째 resolver 의 Resolve 결과를 사용.
// 체인의 모든 resolver 가 CanResolve=false 를 반환하면 종단 DefaultPriorityResolver 로 fallback.
//
// 사용 예:
//
//	composite := publisher.NewCompositeResolver(core.PriorityNormal)
//	composite.Add(&publisher.ExplicitPriorityResolver{})  // 1순위: 명시 priority 보존
//	composite.Add(sourceResolver)                          // 2순위: 등록된 소스 매핑
//	composite.Add(ruleResolver)                            // 3순위: 조건 규칙
//	// 4순위 (자동): DefaultPriorityResolver → Normal
type CompositeResolver struct {
	chain    []ChainablePriorityResolver
	terminal PriorityResolver // 체인 끝의 종단 resolver (항상 결정)
}

// NewCompositeResolver 는 지정된 우선순위를 종단으로 갖는 CompositeResolver 를 생성합니다.
// 체인의 모든 resolver 가 CanResolve=false 를 반환하면 defaultPriority 로 라우팅합니다.
func NewCompositeResolver(defaultPriority core.Priority) *CompositeResolver {
	return &CompositeResolver{
		terminal: NewDefaultPriorityResolver(defaultPriority),
	}
}

// Add 는 체인 끝에 resolver 를 추가합니다.
// 먼저 추가된 resolver 가 높은 우선순위로 평가됩니다.
func (r *CompositeResolver) Add(resolver ChainablePriorityResolver) {
	r.chain = append(r.chain, resolver)
}

// Resolve 는 체인을 순서대로 평가하여 CanResolve=true 인 첫 번째 resolver 의 Priority 를 반환합니다.
// 체인이 비어있거나 모두 CanResolve=false 이면 종단 DefaultPriorityResolver 를 사용.
func (r *CompositeResolver) Resolve(job *core.CrawlJob) core.Priority {
	for _, resolver := range r.chain {
		if resolver.CanResolve(job) {
			return resolver.Resolve(job)
		}
	}
	return r.terminal.Resolve(job)
}
