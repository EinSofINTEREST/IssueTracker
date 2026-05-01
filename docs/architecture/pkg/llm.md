# pkg/llm — Unified LLM Provider Abstraction

소스: [`pkg/llm/`](../../../pkg/llm/)

다중 LLM provider (Gemini / OpenAI ChatGPT / Anthropic Claude) 를 **단일 인터페이스 `Provider`** 로
추상화합니다. 호출자는 provider 교체를 [`Config`](../../../pkg/llm/factory.go) 한 줄로 처리.

<br>

## 핵심 인터페이스

[llm.go](../../../pkg/llm/llm.go):

```go
type Provider interface {
    Name() string
    Generate(ctx context.Context, req Request) (*Response, error)
}

type Request struct {
    Messages    []Message  // Role + Content
    Model       string
    Temperature float64
    MaxTokens   int
    TaskHint    string     // policy 가 사용 (cheapest/latency 등)
}

type Response struct {
    Text  string
    Usage TokenUsage
}

type Role string
const (
    RoleSystem    Role = "system"
    RoleUser      Role = "user"
    RoleAssistant Role = "assistant"
)
```

<br>

## Factory

[factory.go](../../../pkg/llm/factory.go):
```go
provider, err := llm.New(llm.Config{
    Provider: "gemini" | "openai" | "anthropic",
    APIKey:   "...",
    Model:    "...",
    Timeout:  60 * time.Second,
})
```

provider 빌더는 각 서브패키지 `init()` 에서 [`RegisterProvider`](../../../pkg/llm/factory.go) 로 등록 —
[`pkg/llm/providers`](../../../pkg/llm/providers/) 가 import side-effect 로 모두 등록.

본 패키지를 사용할 때는 보통 `_ "issuetracker/pkg/llm/providers"` 를 함께 import.

<br>

## 서브패키지

| 디렉토리                                                  | 역할                                                  |
|----------------------------------------------------------|-------------------------------------------------------|
| [`pkg/llm/anthropic/`](../../../pkg/llm/anthropic/)       | Claude Messages API 어댑터                            |
| [`pkg/llm/gemini/`](../../../pkg/llm/gemini/)             | Gemini Generative API 어댑터                          |
| [`pkg/llm/openai/`](../../../pkg/llm/openai/)             | OpenAI Chat Completions 어댑터                        |
| [`pkg/llm/providers/`](../../../pkg/llm/providers/)       | side-effect import — 모든 provider 의 init() 트리거    |
| [`pkg/llm/chain/`](../../../pkg/llm/chain/)               | Chain-of-Responsibility 합성 (이슈 #142)              |
| [`pkg/llm/policy/`](../../../pkg/llm/policy/)             | 라우팅 정책 — fixed / cheapest / hybrid / latency     |
| [`pkg/llm/prompt/`](../../../pkg/llm/prompt/)             | 프롬프트 빌더 helper                                   |

<br>

## Chain & Policy

[chain.go](../../../pkg/llm/chain/chain.go):
```go
primary,   _ := llm.New(...)
secondary, _ := llm.New(...)

p := chain.New(primary, secondary, fallback)            // 단순 fallback chain
// 또는 정책 기반:
pol := policy.NewFixedOrder("gemini", "openai")
p := chain.NewWithPolicy(pol, []llm.Provider{...}, chain.WithPolicyLogger(log))
```

정책 종류:
- [`fixed.go`](../../../pkg/llm/policy/fixed.go) — 명시 순서대로 시도
- [`cheapest.go`](../../../pkg/llm/policy/cheapest.go) — 가격 우선
- [`hybrid.go`](../../../pkg/llm/policy/hybrid.go) — TaskHint 기반 분기
- [`latency.go`](../../../pkg/llm/policy/latency.go) — 응답 시간 우선

<br>

## 부수 helper

| 파일                                                   | 역할                                              |
|--------------------------------------------------------|---------------------------------------------------|
| [http.go](../../../pkg/llm/http.go)                    | provider 가 공유하는 HTTP 클라이언트 helper       |
| [errors.go](../../../pkg/llm/errors.go)                | `ErrCode` (Auth / RateLimit / Timeout / …)        |
| [capabilities.go](../../../pkg/llm/capabilities.go)    | provider 별 capability 메타 (정책이 사용)         |
| [measured.go](../../../pkg/llm/measured.go)            | latency / token usage metric 측정 wrapper         |

<br>

## 의존

- [`pkg/logger`](logger.md) (선택)
- 외부: provider 별 SDK 또는 표준 `net/http`

<br>

## 호출 측

- [`cmd/issuetracker.buildLLMProvider`](../cmd/issuetracker.md) — chain 구성
- [`internal/processor/parser/rule/llmgen`](../internal/processor/parser/rule.md) — selector 자동 생성
- [`internal/processor/parser/rule/refiner`](../internal/processor/parser/rule.md) — path_pattern 정밀화

<br>

## 관련 이슈

- 이슈 #140 — generic LLM client 도입
- 이슈 #142 — chain composition
- 이슈 #144 — 정책 (cheapest / latency / hybrid) 확장
- 이슈 #149 — selector 자동 생성에서 사용
- 이슈 #173 단계 4-2 — refiner 가 동일 provider 공유
