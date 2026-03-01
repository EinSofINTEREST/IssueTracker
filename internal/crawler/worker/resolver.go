// Package worker provides Kafka-based crawler worker pool and routing logic.
package worker

import (
  "strings"

  "issuetracker/internal/crawler/core"
  "issuetracker/pkg/queue"
)

// ─────────────────────────────────────────────────────────────────────────
// PriorityResolver — 단독 사용 인터페이스
// ─────────────────────────────────────────────────────────────────────────

// PriorityResolver는 CrawlJob을 어떤 우선순위 Kafka 큐로 라우팅할지 결정하는 인터페이스입니다.
//
// PriorityResolver determines which priority queue (crawl.high / crawl.normal / crawl.low)
// a CrawlJob should be routed to. Implementations must be safe for concurrent use.
type PriorityResolver interface {
  Resolve(job *core.CrawlJob) core.Priority
}

// ─────────────────────────────────────────────────────────────────────────
// ChainablePriorityResolver — CompositeResolver 체인 등록용 인터페이스
// ─────────────────────────────────────────────────────────────────────────

// ChainablePriorityResolver는 CompositeResolver 체인에 등록 가능한 resolver 인터페이스입니다.
//
// ChainablePriorityResolver extends PriorityResolver with CanResolve,
// which signals whether this resolver has a definitive answer for the given job.
// CanResolve가 false를 반환하면 CompositeResolver는 체인의 다음 resolver로 넘어갑니다.
type ChainablePriorityResolver interface {
  PriorityResolver

  // CanResolve는 이 resolver가 job에 대해 확정적인 우선순위를 결정할 수 있는지 반환합니다.
  // false 반환 시 CompositeResolver는 체인의 다음 resolver로 위임합니다.
  CanResolve(job *core.CrawlJob) bool
}

// ─────────────────────────────────────────────────────────────────────────
// ExplicitPriorityResolver
// ─────────────────────────────────────────────────────────────────────────

// ExplicitPriorityResolver는 job.Priority에 명시된 값을 그대로 반환합니다.
//
// ExplicitPriorityResolver passes through job.Priority unchanged.
// Use when callers always set the priority field explicitly before publishing.
// 유효하지 않은 Priority 값은 PriorityNormal로 보정합니다.
type ExplicitPriorityResolver struct{}

// Resolve는 job.Priority를 검증하여 반환하고, 유효하지 않은 값은 PriorityNormal로 대체합니다.
func (r *ExplicitPriorityResolver) Resolve(job *core.CrawlJob) core.Priority {
  switch job.Priority {
  case core.PriorityHigh, core.PriorityNormal, core.PriorityLow:
    return job.Priority
  default:
    return core.PriorityNormal
  }
}

// CanResolve는 job.Priority가 유효한 값으로 명시된 경우에만 true를 반환합니다.
// 제로값(0)이면 발행자가 Priority를 설정하지 않은 것으로 판단하여 false를 반환합니다.
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

// SourcePriorityResolver는 CrawlerName(소스 이름)을 기준으로 우선순위를 결정합니다.
//
// SourcePriorityResolver routes jobs to priority queues based on the crawler's
// source name. Useful when specific news sources (e.g. "breaking-news-crawler")
// always require a fixed priority regardless of the job's Priority field.
type SourcePriorityResolver struct {
  rules    map[string]core.Priority
  fallback core.Priority
}

// NewSourcePriorityResolver는 미등록 소스에 적용할 기본 우선순위를 지정하여 생성합니다.
func NewSourcePriorityResolver(fallback core.Priority) *SourcePriorityResolver {
  return &SourcePriorityResolver{
    rules:    make(map[string]core.Priority),
    fallback: fallback,
  }
}

// Register는 크롤러 이름과 우선순위를 매핑합니다.
// 이미 등록된 이름을 재등록하면 덮어씁니다.
func (r *SourcePriorityResolver) Register(crawlerName string, priority core.Priority) {
  r.rules[crawlerName] = priority
}

// Resolve는 job.CrawlerName에 매핑된 우선순위를 반환합니다.
// 매핑이 없으면 생성 시 지정한 fallback 우선순위를 반환합니다.
func (r *SourcePriorityResolver) Resolve(job *core.CrawlJob) core.Priority {
  if p, ok := r.rules[job.CrawlerName]; ok {
    return p
  }
  return r.fallback
}

// CanResolve는 job.CrawlerName이 등록된 소스인 경우에만 true를 반환합니다.
// 미등록 소스는 체인의 다음 resolver로 위임합니다.
func (r *SourcePriorityResolver) CanResolve(job *core.CrawlJob) bool {
  _, ok := r.rules[job.CrawlerName]
  return ok
}

// ─────────────────────────────────────────────────────────────────────────
// RuleBasedPriorityResolver
// ─────────────────────────────────────────────────────────────────────────

// PriorityRule은 조건(Match)과 우선순위(Priority)를 갖는 단일 라우팅 규칙입니다.
type PriorityRule struct {
  // Match가 true를 반환하면 이 규칙의 Priority를 적용합니다.
  Match    func(job *core.CrawlJob) bool
  Priority core.Priority
}

// RuleBasedPriorityResolver는 등록된 규칙을 순서대로 평가하여 첫 번째 매치의 우선순위를 반환합니다.
//
// RuleBasedPriorityResolver evaluates PriorityRule entries in insertion order
// and returns the Priority of the first matching rule. If no rule matches,
// the fallback priority is returned.
type RuleBasedPriorityResolver struct {
  rules    []PriorityRule
  fallback core.Priority
}

// NewRuleBasedPriorityResolver는 기본 우선순위를 지정하여 빈 resolver를 생성합니다.
func NewRuleBasedPriorityResolver(fallback core.Priority) *RuleBasedPriorityResolver {
  return &RuleBasedPriorityResolver{
    fallback: fallback,
  }
}

// AddRule은 조건-우선순위 쌍을 규칙 체인 끝에 추가합니다.
// 규칙은 추가된 순서대로 평가되며 먼저 추가된 규칙이 우선합니다.
func (r *RuleBasedPriorityResolver) AddRule(
  match func(job *core.CrawlJob) bool,
  priority core.Priority,
) {
  r.rules = append(r.rules, PriorityRule{Match: match, Priority: priority})
}

// Resolve는 등록된 규칙을 순서대로 평가합니다.
// 매치되는 규칙이 없으면 fallback 우선순위를 반환합니다.
func (r *RuleBasedPriorityResolver) Resolve(job *core.CrawlJob) core.Priority {
  for _, rule := range r.rules {
    if rule.Match(job) {
      return rule.Priority
    }
  }
  return r.fallback
}

// CanResolve는 등록된 규칙 중 하나라도 매치되는 경우 true를 반환합니다.
// 규칙이 없거나 모두 매치되지 않으면 체인의 다음 resolver로 위임합니다.
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

// DefaultPriorityResolver는 항상 고정된 우선순위를 반환하는 종단 resolver입니다.
//
// DefaultPriorityResolver always resolves to the configured priority.
// CompositeResolver의 마지막 fallback으로 사용되어 모든 job이 반드시 우선순위를 갖도록 보장합니다.
type DefaultPriorityResolver struct {
  priority core.Priority
}

// NewDefaultPriorityResolver는 항상 지정된 우선순위를 반환하는 종단 resolver를 생성합니다.
func NewDefaultPriorityResolver(priority core.Priority) *DefaultPriorityResolver {
  return &DefaultPriorityResolver{priority: priority}
}

// Resolve는 항상 생성 시 지정한 우선순위를 반환합니다.
func (r *DefaultPriorityResolver) Resolve(_ *core.CrawlJob) core.Priority {
  return r.priority
}

// CanResolve는 항상 true를 반환합니다.
// 종단 resolver이므로 모든 job에 대해 결정을 내립니다.
func (r *DefaultPriorityResolver) CanResolve(_ *core.CrawlJob) bool {
  return true
}

// ─────────────────────────────────────────────────────────────────────────
// CompositeResolver — chain resolver
// ─────────────────────────────────────────────────────────────────────────

// CompositeResolver는 여러 ChainablePriorityResolver를 순서대로 평가하는 chain resolver입니다.
//
// CompositeResolver evaluates each registered resolver in order.
// CanResolve가 true인 첫 번째 resolver의 Resolve 결과를 사용합니다.
// 체인의 모든 resolver가 CanResolve=false를 반환하면 종단 DefaultPriorityResolver로 fallback합니다.
//
// 사용 예:
//
//	composite := worker.NewCompositeResolver(core.PriorityNormal)
//	composite.Add(sourceResolver)   // 1순위: 등록된 소스에 한해 결정
//	composite.Add(ruleResolver)     // 2순위: 조건 규칙에 한해 결정
//	// 3순위 (자동): DefaultPriorityResolver → Normal
type CompositeResolver struct {
  chain    []ChainablePriorityResolver
  terminal PriorityResolver // 체인 끝의 종단 resolver (항상 결정)
}

// NewCompositeResolver는 지정된 우선순위를 종단으로 갖는 CompositeResolver를 생성합니다.
// 체인의 모든 resolver가 CanResolve=false를 반환하면 defaultPriority로 라우팅합니다.
func NewCompositeResolver(defaultPriority core.Priority) *CompositeResolver {
  return &CompositeResolver{
    terminal: NewDefaultPriorityResolver(defaultPriority),
  }
}

// Add는 체인 끝에 resolver를 추가합니다.
// 먼저 추가된 resolver가 높은 우선순위로 평가됩니다.
func (r *CompositeResolver) Add(resolver ChainablePriorityResolver) {
  r.chain = append(r.chain, resolver)
}

// Resolve는 체인을 순서대로 평가하여 CanResolve=true인 첫 번째 resolver의 Priority를 반환합니다.
// 체인이 비어있거나 모두 CanResolve=false이면 종단 DefaultPriorityResolver를 사용합니다.
func (r *CompositeResolver) Resolve(job *core.CrawlJob) core.Priority {
  for _, resolver := range r.chain {
    if resolver.CanResolve(job) {
      return resolver.Resolve(job)
    }
  }
  return r.terminal.Resolve(job)
}

// ─────────────────────────────────────────────────────────────────────────
// Raw 토픽 라우팅
// ─────────────────────────────────────────────────────────────────────────

// RawTopicFunc는 국가 코드(ISO 3166-1 alpha-2)를 raw Kafka 토픽 이름으로 변환하는 함수 타입입니다.
//
// RawTopicFunc maps a country code to the corresponding raw Kafka topic name.
// Used by KafkaConsumerPool to route crawled RawContent to the correct topic.
type RawTopicFunc func(country string) string

// DefaultRawTopicFunc는 국가 코드를 raw 토픽으로 변환하는 기본 구현입니다.
//
// DefaultRawTopicFunc maps country codes to raw topics:
//
//	KR → issuetracker.raw.kr
//	* (그 외) → issuetracker.raw.us
func DefaultRawTopicFunc(country string) string {
  switch strings.ToUpper(country) {
  case "KR":
    return queue.TopicRawKR
  default:
    return queue.TopicRawUS
  }
}
