package validator

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"issuetracker/internal/storage"
	"issuetracker/pkg/llm"
)

// llmValidationResponse 는 LLM 의 검증 응답 JSON 구조입니다.
type llmValidationResponse struct {
	Valid  bool   `json:"valid"`
	Reason string `json:"reason"`
}

// LLMValidator 는 pkg/llm.Provider 기반 의미 검증 구현체입니다.
type LLMValidator struct {
	provider llm.Provider
}

// NewLLMValidator 는 LLMValidator 를 생성합니다. provider 는 비-nil 이어야 합니다.
func NewLLMValidator(provider llm.Provider) *LLMValidator {
	return &LLMValidator{provider: provider}
}

func (v *LLMValidator) Validate(ctx context.Context, html string, selectors storage.SelectorMap, targetType storage.TargetType) (Result, error) {
	ec, err := extractContent(html, selectors)
	if err != nil {
		return Result{}, fmt.Errorf("extract content for validation: %w", err)
	}

	prompt := buildValidationPrompt(ec, targetType)
	resp, err := v.provider.Generate(ctx, llm.Request{
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: validationSystemPrompt},
			{Role: llm.RoleUser, Content: prompt},
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

const validationSystemPrompt = `You are a CSS selector quality validator for a news crawler.
Given text extracted from a news website using CSS selectors, determine if the extraction is semantically correct.
Respond ONLY with JSON: {"valid": true or false, "reason": "one sentence explanation"}`

func buildValidationPrompt(ec extractedContent, targetType storage.TargetType) string {
	var sb strings.Builder

	switch targetType {
	case storage.TargetTypeList:
		sb.WriteString("Target type: list page (category/index page)\n\n")
		sb.WriteString("Extracted content:\n")
		fmt.Fprintf(&sb, "- item_container (first element text): %q\n", ec.ItemContainer)
		if len(ec.ItemLinks) > 0 {
			fmt.Fprintf(&sb, "- item_links (first %d): %v\n", len(ec.ItemLinks), ec.ItemLinks)
		}
		sb.WriteString("\nValidation criteria:\n")
		sb.WriteString("- item_links should look like URLs or article titles (not empty, not boilerplate navigation)\n")
		sb.WriteString("- item_container should contain multiple article entries\n")

	default: // TargetTypePage
		sb.WriteString("Target type: article page\n\n")
		sb.WriteString("Extracted content:\n")
		fmt.Fprintf(&sb, "- title: %q\n", ec.Title)
		fmt.Fprintf(&sb, "- body (first 500 chars): %q\n", ec.Body)
		if ec.PublishedAt != "" {
			fmt.Fprintf(&sb, "- published_at: %q\n", ec.PublishedAt)
		}
		sb.WriteString("\nValidation criteria:\n")
		sb.WriteString("- title: non-empty, looks like a news headline (not a menu item, ad, or boilerplate)\n")
		sb.WriteString("- body: at least 100 characters, looks like article content (not navigation, ads, or repeated short phrases)\n")
		if ec.PublishedAt != "" {
			sb.WriteString("- published_at: looks like a date/time string\n")
		}
	}

	sb.WriteString("\nIs this extraction semantically valid for a news article scraper?")
	return sb.String()
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
