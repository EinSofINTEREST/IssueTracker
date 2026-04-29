// Package gemini 는 Google Gemini API 의 llm.Provider 구현입니다 (이슈 #140).
//
// Package gemini implements the llm.Provider interface against Google's
// Generative Language REST API.
//
// API 문서: https://ai.google.dev/api/rest/v1beta/models/generateContent
package gemini

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"time"

	"issuetracker/pkg/llm"
)

const (
	// providerName 은 llm.Error / Provider.Name 에서 사용되는 식별자.
	providerName = "gemini"

	// defaultBaseURL 은 Gemini REST API root.
	defaultBaseURL = "https://generativelanguage.googleapis.com/v1beta"

	// defaultModel 은 Model 필드가 비어있을 때 사용되는 기본 모델.
	defaultModel = "gemini-2.5-flash"
)

// init 은 factory (llm.New) 에서 본 provider 를 사용할 수 있게 등록합니다 (이슈 #140).
//
// 사용자는 llm.Config{Provider: "gemini", APIKey: "...", Model: "..."} 으로 생성 가능.
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

// Provider 는 llm.Provider 의 Gemini 구현입니다.
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

// WithTimeout 은 HTTP timeout 을 override 합니다 (default llm.DefaultTimeout).
func WithTimeout(d time.Duration) Option {
	return func(p *Provider) { p.http = llm.NewHTTPClient(d) }
}

// New 는 Gemini Provider 를 생성합니다. apiKey 가 비어있으면 panic 대신
// 호출 시 ErrCodeAuth 를 반환합니다 (테스트 / dry-run 용).
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

// Name 은 provider 식별자를 반환합니다 (llm.Provider 구현).
func (p *Provider) Name() string { return providerName }

// Generate 는 단일 chat completion 을 수행합니다 (llm.Provider 구현).
//
// Gemini API 형식 변환:
//   - llm.Role(system)      → systemInstruction (별도 필드)
//   - llm.Role(user)        → contents[].role="user"
//   - llm.Role(assistant)   → contents[].role="model"  (Gemini 명칭 차이)
//
// API key 는 query parameter 로 전달 (Gemini 표준).
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

	body, err := buildGeminiRequest(req)
	if err != nil {
		return nil, err
	}

	endpoint := fmt.Sprintf("%s/models/%s:generateContent?key=%s",
		p.baseURL, url.PathEscape(model), url.QueryEscape(p.apiKey))

	respBytes, err := p.http.PostJSON(ctx, providerName, endpoint, nil, body)
	if err != nil {
		return nil, err
	}

	return parseGeminiResponse(respBytes, model)
}

// ─────────────────────────────────────────────────────────────────────────────
// Request / Response 스키마 (Gemini REST 전용)
// ─────────────────────────────────────────────────────────────────────────────

type geminiPart struct {
	Text string `json:"text"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"` // "user" | "model"
	Parts []geminiPart `json:"parts"`
}

type geminiSystemInstruction struct {
	Parts []geminiPart `json:"parts"`
}

type geminiGenerationConfig struct {
	Temperature     *float32 `json:"temperature,omitempty"`
	MaxOutputTokens *int     `json:"maxOutputTokens,omitempty"`
}

type geminiRequest struct {
	Contents          []geminiContent          `json:"contents"`
	SystemInstruction *geminiSystemInstruction `json:"systemInstruction,omitempty"`
	GenerationConfig  *geminiGenerationConfig  `json:"generationConfig,omitempty"`
}

type geminiCandidate struct {
	Content      geminiContent `json:"content"`
	FinishReason string        `json:"finishReason"`
}

type geminiUsageMetadata struct {
	PromptTokenCount     int `json:"promptTokenCount"`
	CandidatesTokenCount int `json:"candidatesTokenCount"`
}

type geminiResponse struct {
	Candidates    []geminiCandidate   `json:"candidates"`
	UsageMetadata geminiUsageMetadata `json:"usageMetadata"`
	ModelVersion  string              `json:"modelVersion"`
}

// buildGeminiRequest 는 llm.Request 를 Gemini API 요청 body 로 변환합니다.
func buildGeminiRequest(req llm.Request) (*geminiRequest, error) {
	if len(req.Messages) == 0 {
		return nil, &llm.Error{
			Code:     llm.ErrCodeBadRequest,
			Provider: providerName,
			Message:  "request has no messages",
		}
	}
	out := &geminiRequest{}

	var systemTexts []string
	for _, m := range req.Messages {
		switch m.Role {
		case llm.RoleSystem:
			systemTexts = append(systemTexts, m.Content)
		case llm.RoleUser:
			out.Contents = append(out.Contents, geminiContent{
				Role:  "user",
				Parts: []geminiPart{{Text: m.Content}},
			})
		case llm.RoleAssistant:
			out.Contents = append(out.Contents, geminiContent{
				Role:  "model",
				Parts: []geminiPart{{Text: m.Content}},
			})
		default:
			return nil, &llm.Error{
				Code:     llm.ErrCodeBadRequest,
				Provider: providerName,
				Message:  fmt.Sprintf("unsupported role %q", m.Role),
			}
		}
	}
	if len(systemTexts) > 0 {
		// 여러 system 메시지는 줄바꿈으로 합쳐 하나의 systemInstruction 으로 전송
		// (Gemini 가 multi-system 을 직접 지원하지 않음).
		joined := systemTexts[0]
		for i := 1; i < len(systemTexts); i++ {
			joined += "\n\n" + systemTexts[i]
		}
		out.SystemInstruction = &geminiSystemInstruction{
			Parts: []geminiPart{{Text: joined}},
		}
	}

	if req.Temperature > 0 || req.MaxTokens > 0 {
		out.GenerationConfig = &geminiGenerationConfig{}
		if req.Temperature > 0 {
			t := req.Temperature
			out.GenerationConfig.Temperature = &t
		}
		if req.MaxTokens > 0 {
			n := req.MaxTokens
			out.GenerationConfig.MaxOutputTokens = &n
		}
	}

	return out, nil
}

// parseGeminiResponse 는 Gemini API 응답을 llm.Response 로 정규화합니다.
func parseGeminiResponse(body []byte, requestedModel string) (*llm.Response, error) {
	var raw geminiResponse
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, &llm.Error{
			Code:     llm.ErrCodeUnknown,
			Provider: providerName,
			Message:  "decode response",
			Err:      err,
		}
	}
	if len(raw.Candidates) == 0 || len(raw.Candidates[0].Content.Parts) == 0 {
		return nil, &llm.Error{
			Code:     llm.ErrCodeUnknown,
			Provider: providerName,
			Message:  "empty response (no candidates)",
		}
	}
	// Gemini 는 단일 candidate 안의 parts 가 여러 text chunk 일 수 있어 합칩니다.
	cand := raw.Candidates[0]
	content := ""
	for _, part := range cand.Content.Parts {
		content += part.Text
	}
	model := raw.ModelVersion
	if model == "" {
		model = requestedModel
	}
	return &llm.Response{
		Content:      content,
		Model:        model,
		InputTokens:  raw.UsageMetadata.PromptTokenCount,
		OutputTokens: raw.UsageMetadata.CandidatesTokenCount,
		StopReason:   cand.FinishReason,
	}, nil
}
