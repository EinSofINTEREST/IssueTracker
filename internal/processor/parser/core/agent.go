// Package core 는 parser stage 가 agent backend 를 사용하는 단일 진입점입니다 (이슈 #460).
//
// 디자인 목표 (객체 지향 설계):
//   - parser stage 가 concrete agent backend (claude / codex) 를 모르도록 추상화
//   - 본 패키지가 정의한 RuleAgent 인터페이스를 main.go 에서 DI
//   - llmgen / refiner / pathinfer / validator 등 parser 내부 서브패키지는
//     본 인터페이스에 의존 (현재는 점진적 — 본 PR 은 추상 정의 + claude 어댑터만 도입)
//
// 향후 codex backend 추가 시 main.go 가 RuleAgent 의 codex 구현체를 주입 — parser 내부
// 코드는 변경 없음.
package core

import (
	"context"

	"issuetracker/internal/processor/parser/rule/llmgen"
	"issuetracker/internal/storage/model"
	"issuetracker/pkg/agent"
)

// RuleAgent 는 parser stage 가 사용하는 agent port 입니다.
//
// 두 가지 use-case 를 정의:
//
//  1. 셀렉터 추출 (SelectorExtractor 호환) — parser_rules 의 selectors JSONB 생성용 단일-step 추출.
//     legacy LLM provider (Gemini / OpenAI API 등) 가 본 호출만 지원.
//
//  2. 강화된 추출 (EnrichedExtractor 호환) — validity / page_type / selectors / self_check
//     를 함께 반환하는 multi-step 추출. claude.Pool 등 container-based agent 가 지원.
//
// 두 메소드 모두 llmgen.SelectorExtractor / EnrichedExtractor 인터페이스와 시그니처가
// 1:1 일치 — backend (claude.Pool 등) 가 그대로 만족.
//
// 본 인터페이스를 통해 parser stage 는 concrete agent backend 를 모르고 사용 가능 —
// 후속 codex agent backend 도 동일 인터페이스 만족 시 swap.
type RuleAgent interface {
	llmgen.SelectorExtractor
	llmgen.EnrichedExtractor

	// ModelName 은 backend 가 사용하는 모델 식별자를 반환합니다 — DB 기록 / 로깅용.
	ModelName() string
}

// RawAgent 는 stage-agnostic 한 generic agent primitive 의 alias 입니다.
//
// agent.Agent (= RunSession) 의 alias — parser 내부에서 vendor 추상 단일 의존성으로
// 호출 가능. RuleAgent 가 더 도메인-구체적 인터페이스이므로 일반적 호출은 RuleAgent 우선.
// RawAgent 는 selector / enriched 가 아닌 임의 prompt 호출이 필요할 때 사용.
type RawAgent = agent.Agent

// 컴파일 타임 검증: model.TargetType 이 정상 import 되었음을 표시 (외부 패키지가
// 본 패키지를 import 했을 때 model.TargetType 을 RuleAgent 메소드 시그니처 일치 위해
// 같은 패키지에서 접근 가능하도록).
var _ = model.TargetTypePage

// RuleAgentClient 는 RuleAgent 의 default wiring 구조체입니다.
//
// 인스턴스 자체는 RuleAgent 인터페이스를 만족하는 backend (claude.Pool 등) 의 단순 wrapper
// 가 아니라, 향후 stage-level 부가 기능 (호출 metric 측정 / cost cap / retry policy 등) 을
// 흡수할 위치 — 현재는 wrapper 만 제공.
type RuleAgentClient struct {
	backend RuleAgent
}

// NewRuleAgentClient 는 backend agent 를 wrap 하여 RuleAgentClient 를 생성합니다.
//
// backend 가 nil 이면 panic — fail-fast (silent agent loss 회피).
func NewRuleAgentClient(backend RuleAgent) *RuleAgentClient {
	if backend == nil {
		panic("parser/core: RuleAgent backend must not be nil")
	}
	return &RuleAgentClient{backend: backend}
}

// Extract 는 selector 만 반환하는 단일-step 추출 — backend 에 위임.
func (c *RuleAgentClient) Extract(ctx context.Context, host string, targetType model.TargetType, html string) (model.SelectorMap, error) {
	return c.backend.Extract(ctx, host, targetType, html)
}

// ExtractEnriched 는 multi-step 추출 — backend 에 위임.
func (c *RuleAgentClient) ExtractEnriched(ctx context.Context, host string, targetType model.TargetType, html string) (*llmgen.ExtractResult, error) {
	return c.backend.ExtractEnriched(ctx, host, targetType, html)
}

// ModelName 은 backend 모델 ID 위임.
func (c *RuleAgentClient) ModelName() string {
	return c.backend.ModelName()
}

// 컴파일 타임 검증.
var (
	_ RuleAgent                = (*RuleAgentClient)(nil)
	_ llmgen.SelectorExtractor = (*RuleAgentClient)(nil)
	_ llmgen.EnrichedExtractor = (*RuleAgentClient)(nil)
)
