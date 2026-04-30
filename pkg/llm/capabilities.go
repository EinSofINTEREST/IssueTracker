package llm

// Capabilities describes a provider/model's quantitative properties used by routing policies.
//
// Capabilities 는 routing policy 가 provider 선택에 사용하는 정량적 속성입니다 (이슈 #144).
// 비용 / context window / 평균 latency 등을 표준 단위로 표현합니다.
//
// 모든 비용은 USD per 1M tokens, latency 는 milliseconds 입니다.
type Capabilities struct {
	// CostInputPer1M: 입력 토큰 1M 당 USD. 0 이면 unknown / 무료.
	CostInputPer1M float64

	// CostOutputPer1M: 출력 토큰 1M 당 USD. 0 이면 unknown / 무료.
	CostOutputPer1M float64

	// ContextWindow: 모델의 최대 context window (tokens). 0 이면 unknown.
	ContextWindow int

	// AvgLatencyMs: 평균 응답 latency 추정치 (milliseconds). 초기 hardcode estimate.
	// MeasuredProvider 가 동적 EMA 를 노출하지만 Capabilities 는 정적 baseline.
	AvgLatencyMs int
}

// CapabilitiesProvider returns the Capabilities for a given (provider, model) pair.
//
// 구현체:
//   - StaticCapabilitiesProvider: 컴파일 시점 hardcode (초기 구현, 본 PR)
//   - 향후 RefreshableCapabilitiesProvider: 주기 background goroutine 으로 외부 source
//     (config 파일 / DB / pricing API) 에서 fetch 후 cache 갱신 (이슈 #144 후속)
//
// Get 은 lookup 결과가 없으면 (Capabilities{}, false) 반환합니다.
type CapabilitiesProvider interface {
	Get(provider, model string) (Capabilities, bool)
}

// StaticCapabilitiesProvider returns Capabilities from a compile-time hardcoded table.
//
// 본 구현은 본 PR 에서만 동기 lookup. RefreshableCapabilitiesProvider (후속) 는 동일 인터페이스를
// 구현하면서 background refresh 로직만 추가하므로 호출자 코드 변경 없이 교체 가능합니다.
type StaticCapabilitiesProvider struct {
	table map[capKey]Capabilities
}

type capKey struct {
	Provider string
	Model    string
}

// NewStaticCapabilitiesProvider returns a StaticCapabilitiesProvider pre-populated with
// the well-known model pricing as of 2026-04 (이슈 #144 본문 참고). 운영 단가 변동 시 hardcode
// 갱신 또는 RefreshableCapabilitiesProvider 로 교체.
func NewStaticCapabilitiesProvider() *StaticCapabilitiesProvider {
	return &StaticCapabilitiesProvider{
		table: defaultCapabilitiesTable(),
	}
}

// NewStaticCapabilitiesProviderFrom builds a provider from an arbitrary table — 테스트 / 운영자
// 커스텀 단가에 사용.
func NewStaticCapabilitiesProviderFrom(table map[string]map[string]Capabilities) *StaticCapabilitiesProvider {
	flat := make(map[capKey]Capabilities, len(table))
	for provider, models := range table {
		for model, caps := range models {
			flat[capKey{Provider: provider, Model: model}] = caps
		}
	}
	return &StaticCapabilitiesProvider{table: flat}
}

// Get implements CapabilitiesProvider — provider/model 조합으로 hardcode 테이블 lookup.
func (s *StaticCapabilitiesProvider) Get(provider, model string) (Capabilities, bool) {
	caps, ok := s.table[capKey{Provider: provider, Model: model}]
	return caps, ok
}

// defaultCapabilitiesTable returns the hardcoded pricing baseline (USD per 1M tokens).
// 단가 출처는 이슈 #144 본문 — 작업 시점 (2026-04) 기준이며 운영자가 주기 검증 필요.
func defaultCapabilitiesTable() map[capKey]Capabilities {
	return map[capKey]Capabilities{
		// OpenAI
		{Provider: "openai", Model: "gpt-4o-mini"}: {
			CostInputPer1M: 0.15, CostOutputPer1M: 0.60,
			ContextWindow: 128_000, AvgLatencyMs: 800,
		},
		{Provider: "openai", Model: "gpt-4o"}: {
			CostInputPer1M: 2.50, CostOutputPer1M: 10.00,
			ContextWindow: 128_000, AvgLatencyMs: 1500,
		},

		// Anthropic
		{Provider: "anthropic", Model: "claude-opus-4-7"}: {
			CostInputPer1M: 15.00, CostOutputPer1M: 75.00,
			ContextWindow: 200_000, AvgLatencyMs: 3500,
		},
		{Provider: "anthropic", Model: "claude-sonnet-4-6"}: {
			CostInputPer1M: 3.00, CostOutputPer1M: 15.00,
			ContextWindow: 200_000, AvgLatencyMs: 1800,
		},
		{Provider: "anthropic", Model: "claude-haiku-4-5"}: {
			CostInputPer1M: 0.80, CostOutputPer1M: 4.00,
			ContextWindow: 200_000, AvgLatencyMs: 700,
		},

		// Google Gemini
		{Provider: "gemini", Model: "gemini-1.5-pro"}: {
			CostInputPer1M: 1.25, CostOutputPer1M: 5.00,
			ContextWindow: 2_000_000, AvgLatencyMs: 2200,
		},
		{Provider: "gemini", Model: "gemini-2.5-flash"}: {
			CostInputPer1M: 0.075, CostOutputPer1M: 0.30,
			ContextWindow: 1_000_000, AvgLatencyMs: 600,
		},
	}
}
