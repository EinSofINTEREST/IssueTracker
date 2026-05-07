package validator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"issuetracker/internal/storage"
	"issuetracker/pkg/llm"
	"issuetracker/pkg/llm/prompt"
)

// llmValidationResponse 는 LLM 의 검증 응답 JSON 구조입니다.
type llmValidationResponse struct {
	Valid  bool   `json:"valid"`
	Reason string `json:"reason"`
}

// LLMValidator 는 pkg/llm.Provider 기반 의미 검증 구현체입니다.
type LLMValidator struct {
	provider llm.Provider
	loader   prompt.Loader
}

// NewLLMValidator 는 LLMValidator 를 생성합니다. provider / loader 모두 비-nil 이어야 합니다.
func NewLLMValidator(provider llm.Provider, loader prompt.Loader) (*LLMValidator, error) {
	if provider == nil {
		return nil, errors.New("validator: nil llm provider")
	}
	if loader == nil {
		return nil, errors.New("validator: nil prompt loader")
	}
	return &LLMValidator{provider: provider, loader: loader}, nil
}

func (v *LLMValidator) Validate(ctx context.Context, html string, selectors storage.SelectorMap, targetType storage.TargetType) (Result, error) {
	ec, err := extractContent(html, selectors)
	if err != nil {
		return Result{}, fmt.Errorf("extract content for validation: %w", err)
	}

	system, err := v.loader.Load("validator/system")
	if err != nil {
		return Result{}, fmt.Errorf("load validator system prompt: %w", err)
	}
	user, err := buildValidationPrompt(ec, targetType, v.loader)
	if err != nil {
		return Result{}, err
	}
	resp, err := v.provider.Generate(ctx, llm.Request{
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: system},
			{Role: llm.RoleUser, Content: user},
		},
		TaskHint:  llm.TaskHintJSON,
		MaxTokens: 256,
	})
	if err != nil {
		return Result{}, fmt.Errorf("llm validation call: %w", err)
	}
	if resp == nil || resp.Content == "" {
		return Result{}, fmt.Errorf("llm returned empty validation response")
	}

	var vr llmValidationResponse
	if err := parseValidationResponse(resp.Content, &vr); err != nil {
		return Result{}, fmt.Errorf("parse validation response: %w", err)
	}
	return Result(vr), nil
}

func buildValidationPrompt(ec extractedContent, targetType storage.TargetType, loader prompt.Loader) (string, error) {
	switch targetType {
	case storage.TargetTypeList:
		template, err := loader.Load("validator/list.user")
		if err != nil {
			return "", fmt.Errorf("load validator list user prompt: %w", err)
		}
		linksLine := ""
		if len(ec.ItemLinks) > 0 {
			linksLine = fmt.Sprintf("- item_links (first %d): %v\n", len(ec.ItemLinks), ec.ItemLinks)
		}
		return prompt.Render(template,
			"{{ITEM_CONTAINER}}", fmt.Sprintf("%q", ec.ItemContainer),
			"{{ITEM_LINKS_LINE}}", linksLine,
		), nil

	default: // TargetTypePage
		template, err := loader.Load("validator/page.user")
		if err != nil {
			return "", fmt.Errorf("load validator page user prompt: %w", err)
		}
		publishedLine := ""
		publishedCriteria := ""
		if ec.PublishedAt != "" {
			publishedLine = fmt.Sprintf("- published_at: %q\n", ec.PublishedAt)
			publishedCriteria = "- published_at: looks like a date/time string\n"
		}
		return prompt.Render(template,
			"{{TITLE}}", fmt.Sprintf("%q", ec.Title),
			"{{BODY}}", fmt.Sprintf("%q", ec.Body),
			"{{PUBLISHED_AT_LINE}}", publishedLine,
			"{{PUBLISHED_AT_CRITERIA}}", publishedCriteria,
		), nil
	}
}

// parseValidationResponse 는 LLM 응답에서 JSON 객체를 추출하여 파싱합니다.
func parseValidationResponse(content string, out *llmValidationResponse) error {
	// JSON 블록 추출 — LLM 이 마크다운 코드 블록으로 감쌀 수 있음
	s := content
	if i := strings.Index(s, "{"); i >= 0 {
		s = s[i:]
	}
	if i := strings.LastIndex(s, "}"); i >= 0 {
		s = s[:i+1]
	}
	if err := json.Unmarshal([]byte(s), out); err != nil {
		return fmt.Errorf("unmarshal: %w (raw: %q)", err, content)
	}
	return nil
}
