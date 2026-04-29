// Package anthropic 는 Anthropic Claude API 의 llm.Provider 구현입니다 (이슈 #140).
//
// Package anthropic implements the llm.Provider interface against Anthropic's
// Messages REST API.
//
// API 문서: https://docs.anthropic.com/en/api/messages
package anthropic

import (
	"context"
	"encoding/json"
	"time"

	"issuetracker/pkg/llm"
)

const (
	providerName     = "anthropic"
	defaultBaseURL   = "https://api.anthropic.com/v1"
	defaultModel     = "claude-opus-4-7"
	defaultMaxTokens = 4096 // Anthropic 은 max_tokens 가 필수 — 기본값 보장
	apiVersion       = "2023-06-01"
)

// Provider 는 llm.Provider 의 Anthropic 구현입니다.
type Provider struct {
	apiKey  string
	model   string
	baseURL string
	http    *llm.HTTPClient
}

// Option 은 Provider 생성 옵션입니다.
type Option func(*Provider)

// WithBaseURL 은 API base URL 을 override 합니다 (테스트의 mock server 용).
func WithBaseURL(u string) Option { return func(p *Provider) { p.baseURL = u } }

// WithModel 은 기본 모델을 override 합니다.
func WithModel(m string) Option { return func(p *Provider) { p.model = m } }

// WithTimeout 은 HTTP timeout 을 override 합니다.
func WithTimeout(d time.Duration) Option {
	return func(p *Provider) { p.http = llm.NewHTTPClient(d) }
}

// New 는 Anthropic Provider 를 생성합니다.
func New(apiKey string, opts ...Option) *Provider {
	p := &Provider{
		apiKey:  apiKey,
		model:   defaultModel,
		baseURL: defaultBaseURL,
		http:    llm.NewHTTPClient(0),
	}
	for _, o := range opts {
		o(p)
	}
	return p
}

// Name 은 provider 식별자를 반환합니다.
func (p *Provider) Name() string { return providerName }

// Generate 는 단일 chat completion 을 수행합니다.
//
// Anthropic API 스키마 차이:
//   - llm.Role(system) → 별도 top-level "system" 필드 (messages 배열에 안 들어감)
//   - llm.Role(user) / RoleAssistant → messages 배열
//   - max_tokens 는 필수 — 0 이면 defaultMaxTokens 사용
//   - 인증 헤더: x-api-key + anthropic-version
func (p *Provider) Generate(ctx context.Context, req llm.Request) (*llm.Response, error) {
	if p.apiKey == "" {
		return nil, &llm.Error{
			Code:     llm.ErrCodeAuth,
			Provider: providerName,
			Message:  "missing API key",
		}
	}

	model := req.Model
	if model == "" {
		model = p.model
	}

	body, err := buildAnthropicRequest(req, model)
	if err != nil {
		return nil, err
	}

	endpoint := p.baseURL + "/messages"
	headers := map[string]string{
		"x-api-key":         p.apiKey,
		"anthropic-version": apiVersion,
	}

	respBytes, err := p.http.PostJSON(ctx, providerName, endpoint, headers, body)
	if err != nil {
		return nil, err
	}

	return parseAnthropicResponse(respBytes, model)
}

// ─────────────────────────────────────────────────────────────────────────────
// Request / Response 스키마
// ─────────────────────────────────────────────────────────────────────────────

type anthropicMessage struct {
	Role    string `json:"role"` // "user" | "assistant"
	Content string `json:"content"`
}

type anthropicRequest struct {
	Model       string             `json:"model"`
	System      string             `json:"system,omitempty"`
	Messages    []anthropicMessage `json:"messages"`
	MaxTokens   int                `json:"max_tokens"`
	Temperature *float32           `json:"temperature,omitempty"`
}

type anthropicContentBlock struct {
	Type string `json:"type"` // "text" 등
	Text string `json:"text"`
}

type anthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type anthropicResponse struct {
	Model      string                  `json:"model"`
	Content    []anthropicContentBlock `json:"content"`
	StopReason string                  `json:"stop_reason"`
	Usage      anthropicUsage          `json:"usage"`
}

// buildAnthropicRequest 는 llm.Request 를 Anthropic 요청 body 로 변환합니다.
//
// 다중 system 메시지는 줄바꿈으로 합쳐 단일 system 필드로 전송 (Anthropic 표준).
func buildAnthropicRequest(req llm.Request, model string) (*anthropicRequest, error) {
	if len(req.Messages) == 0 {
		return nil, &llm.Error{
			Code:     llm.ErrCodeBadRequest,
			Provider: providerName,
			Message:  "request has no messages",
		}
	}

	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = defaultMaxTokens
	}
	out := &anthropicRequest{
		Model:     model,
		MaxTokens: maxTokens,
		Messages:  make([]anthropicMessage, 0, len(req.Messages)),
	}

	var systemTexts []string
	for _, m := range req.Messages {
		switch m.Role {
		case llm.RoleSystem:
			systemTexts = append(systemTexts, m.Content)
		case llm.RoleUser:
			out.Messages = append(out.Messages, anthropicMessage{Role: "user", Content: m.Content})
		case llm.RoleAssistant:
			out.Messages = append(out.Messages, anthropicMessage{Role: "assistant", Content: m.Content})
		default:
			return nil, &llm.Error{
				Code:     llm.ErrCodeBadRequest,
				Provider: providerName,
				Message:  "unsupported role " + string(m.Role),
			}
		}
	}
	if len(systemTexts) > 0 {
		joined := systemTexts[0]
		for i := 1; i < len(systemTexts); i++ {
			joined += "\n\n" + systemTexts[i]
		}
		out.System = joined
	}

	if req.Temperature > 0 {
		t := req.Temperature
		out.Temperature = &t
	}

	return out, nil
}

// parseAnthropicResponse 는 Anthropic 응답을 llm.Response 로 정규화합니다.
//
// Anthropic 의 content 는 block 배열 — 모든 type="text" 블록의 text 를 합칩니다.
// (다른 type 예: tool_use 는 본 모듈에서 다루지 않음)
func parseAnthropicResponse(body []byte, requestedModel string) (*llm.Response, error) {
	var raw anthropicResponse
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, &llm.Error{
			Code:     llm.ErrCodeUnknown,
			Provider: providerName,
			Message:  "decode response",
			Err:      err,
		}
	}
	if len(raw.Content) == 0 {
		return nil, &llm.Error{
			Code:     llm.ErrCodeUnknown,
			Provider: providerName,
			Message:  "empty response (no content blocks)",
		}
	}
	content := ""
	for _, block := range raw.Content {
		if block.Type == "text" {
			content += block.Text
		}
	}
	model := raw.Model
	if model == "" {
		model = requestedModel
	}
	return &llm.Response{
		Content:      content,
		Model:        model,
		InputTokens:  raw.Usage.InputTokens,
		OutputTokens: raw.Usage.OutputTokens,
		StopReason:   raw.StopReason,
	}, nil
}
