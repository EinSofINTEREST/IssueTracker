// Package openai 는 OpenAI ChatGPT API 의 llm.Provider 구현입니다 (이슈 #140).
//
// Package openai implements the llm.Provider interface against OpenAI's
// chat completions REST API.
//
// API 문서: https://platform.openai.com/docs/api-reference/chat
package openai

import (
	"context"
	"encoding/json"
	"time"

	"issuetracker/pkg/llm"
)

const (
	providerName   = "openai"
	defaultBaseURL = "https://api.openai.com/v1"
	defaultModel   = "gpt-4o-mini"
)

// init 은 factory (llm.New) 에서 본 provider 를 사용할 수 있게 등록합니다 (이슈 #140).
func init() {
	llm.RegisterProvider(providerName, func(cfg llm.Config) (llm.Provider, error) {
		opts := []Option{}
		if cfg.Model != "" {
			opts = append(opts, WithModel(cfg.Model))
		}
		if cfg.BaseURL != "" {
			opts = append(opts, WithBaseURL(cfg.BaseURL))
		}
		if cfg.Timeout > 0 {
			opts = append(opts, WithTimeout(cfg.Timeout))
		}
		return New(cfg.APIKey, opts...), nil
	})
}

// Provider 는 llm.Provider 의 OpenAI 구현입니다.
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

// New 는 OpenAI Provider 를 생성합니다.
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
// OpenAI API 는 모든 role (system / user / assistant) 을 messages 배열에 그대로
// 전달하므로 변환이 가장 단순합니다. Authorization 헤더 + Bearer 토큰.
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

	body, err := buildOpenAIRequest(req, model)
	if err != nil {
		return nil, err
	}

	endpoint := p.baseURL + "/chat/completions"
	headers := map[string]string{
		"Authorization": "Bearer " + p.apiKey,
	}

	respBytes, err := p.http.PostJSON(ctx, providerName, endpoint, headers, body)
	if err != nil {
		return nil, err
	}

	return parseOpenAIResponse(respBytes, model)
}

// ─────────────────────────────────────────────────────────────────────────────
// Request / Response 스키마
// ─────────────────────────────────────────────────────────────────────────────

type openaiMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openaiRequest struct {
	Model       string          `json:"model"`
	Messages    []openaiMessage `json:"messages"`
	Temperature *float32        `json:"temperature,omitempty"`
	MaxTokens   *int            `json:"max_tokens,omitempty"`
}

type openaiChoice struct {
	Message      openaiMessage `json:"message"`
	FinishReason string        `json:"finish_reason"`
}

type openaiUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

type openaiResponse struct {
	Model   string         `json:"model"`
	Choices []openaiChoice `json:"choices"`
	Usage   openaiUsage    `json:"usage"`
}

// buildOpenAIRequest 는 llm.Request 를 OpenAI 요청 body 로 변환합니다.
func buildOpenAIRequest(req llm.Request, model string) (*openaiRequest, error) {
	if len(req.Messages) == 0 {
		return nil, &llm.Error{
			Code:     llm.ErrCodeBadRequest,
			Provider: providerName,
			Message:  "request has no messages",
		}
	}
	out := &openaiRequest{
		Model:    model,
		Messages: make([]openaiMessage, 0, len(req.Messages)),
	}
	for _, m := range req.Messages {
		out.Messages = append(out.Messages, openaiMessage{
			Role:    string(m.Role), // "system" / "user" / "assistant" 그대로 매핑
			Content: m.Content,
		})
	}
	if req.Temperature > 0 {
		t := req.Temperature
		out.Temperature = &t
	}
	if req.MaxTokens > 0 {
		n := req.MaxTokens
		out.MaxTokens = &n
	}
	return out, nil
}

// parseOpenAIResponse 는 OpenAI 응답을 llm.Response 로 정규화합니다.
func parseOpenAIResponse(body []byte, requestedModel string) (*llm.Response, error) {
	var raw openaiResponse
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, &llm.Error{
			Code:     llm.ErrCodeUnknown,
			Provider: providerName,
			Message:  "decode response",
			Err:      err,
		}
	}
	if len(raw.Choices) == 0 {
		return nil, &llm.Error{
			Code:     llm.ErrCodeUnknown,
			Provider: providerName,
			Message:  "empty response (no choices)",
		}
	}
	choice := raw.Choices[0]
	model := raw.Model
	if model == "" {
		model = requestedModel
	}
	return &llm.Response{
		Content:      choice.Message.Content,
		Model:        model,
		InputTokens:  raw.Usage.PromptTokens,
		OutputTokens: raw.Usage.CompletionTokens,
		StopReason:   choice.FinishReason,
	}, nil
}
