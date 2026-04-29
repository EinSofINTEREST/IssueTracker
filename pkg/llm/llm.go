// Package llm 은 외부 LLM API (Gemini / OpenAI ChatGPT / Anthropic Claude) 를
// 동일한 추상 인터페이스로 호출할 수 있게 하는 generic client 패키지입니다 (이슈 #140).
//
// Package llm provides a unified interface for invoking external LLM APIs
// (Google Gemini, OpenAI ChatGPT, Anthropic Claude). Provider-specific request /
// response shapes are normalized into common types so that callers depend only on
// the Provider interface.
//
// 호출자는 인터페이스 (llm.Provider) 와 공통 모델 (Message / Request / Response) 만
// 알면 되며, provider 교체는 factory (llm.New) 의 Config 한 줄 변경만으로 가능합니다.
//
// 사용 예시:
//
//	cfg := llm.Config{Provider: "claude", APIKey: os.Getenv("ANTHROPIC_API_KEY"), Model: "claude-opus-4-7"}
//	p, err := llm.New(cfg)
//	if err != nil {
//	    log.Fatal(err)
//	}
//	resp, err := p.Generate(ctx, llm.Request{
//	    Messages: []llm.Message{{Role: llm.RoleUser, Content: "Hello"}},
//	})
package llm

import "context"

// Role 은 대화 메시지의 발화자 역할을 나타냅니다.
//
// Role represents the speaker role for a message in a chat-style request.
// Provider 별 명칭이 다르므로 (예: Anthropic 은 system 을 별도 파라미터로 받음)
// 어댑터에서 변환합니다.
type Role string

const (
	// RoleSystem 은 모델 동작을 지시하는 system instruction 입니다.
	// Anthropic 은 별도 system 파라미터로, OpenAI 는 system role 메시지로 매핑됩니다.
	RoleSystem Role = "system"

	// RoleUser 는 사용자 입력 메시지입니다.
	RoleUser Role = "user"

	// RoleAssistant 는 모델 응답 메시지입니다 (multi-turn 시 이전 응답을 다시 전달).
	RoleAssistant Role = "assistant"
)

// Message 는 대화의 단일 메시지입니다 (chat-style API 의 공통 단위).
//
// Message represents a single message in a chat-style conversation.
type Message struct {
	Role    Role   `json:"role"`
	Content string `json:"content"`
}

// Request 는 LLM 호출 입력입니다.
//
// Request is the input for an LLM call. Empty fields fall back to provider defaults
// configured at construction time (e.g. Model, Temperature).
type Request struct {
	// Messages 는 대화 메시지 목록입니다. RoleSystem 메시지는 일반적으로 첫 항목으로 둡니다.
	Messages []Message

	// Model 이 비어있으면 Provider 생성 시 설정된 기본 모델을 사용합니다.
	Model string

	// Temperature 는 0.0 ~ 2.0 범위 (provider 별로 정규화). 0 이면 provider default.
	Temperature float32

	// MaxTokens 는 응답 생성 시 최대 토큰 수. 0 이면 provider default.
	MaxTokens int
}

// Response 는 LLM 응답입니다.
//
// Response is the normalized LLM response. Token counts may be 0 if the provider
// did not report them.
type Response struct {
	// Content 는 assistant 응답 본문 (단일 string 으로 합친 결과).
	Content string

	// Model 은 실제 응답을 생성한 모델 이름 (provider 가 보고한 값).
	Model string

	// InputTokens / OutputTokens 는 사용량 metric (없으면 0).
	InputTokens  int
	OutputTokens int

	// StopReason 은 응답 종료 사유 (provider 별 raw 값을 그대로 보존).
	// 예: "end_turn" (Anthropic), "stop" (OpenAI), "STOP" (Gemini).
	StopReason string
}

// Provider 는 LLM 호출의 추상 인터페이스입니다.
// 모든 구현체는 goroutine-safe 해야 합니다 (단일 인스턴스를 여러 worker 가 공유).
//
// Provider abstracts an LLM backend. Implementations must be safe for concurrent use.
type Provider interface {
	// Name 은 provider 식별자 ("gemini" / "openai" / "anthropic") 를 반환합니다.
	Name() string

	// Generate 는 단일 chat completion 을 수행합니다. 에러는 *llm.Error 로 정규화됩니다.
	Generate(ctx context.Context, req Request) (*Response, error)
}
