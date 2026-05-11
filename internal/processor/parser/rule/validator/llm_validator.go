package validator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"issuetracker/internal/storage"
	"issuetracker/pkg/llm"
	"issuetracker/pkg/llm/prompt"
)

// validatorMaxTokens 는 LLM 검증 응답의 최대 토큰 수입니다 (이슈 #320).
//
// 256 → 512 상향: 256 은 마크다운 코드 펜스 + JSON 키 + 한국어 reason 한 문장이 빠르게 도달.
// 라이브 (2026-05-08) 8h log 에서 best-effort fallback 10건 모두 `"valid": false,` 또는
// reason 중간에서 truncate. 512 면 reason 1~2 문장 + 펜스 여유 확보.
const validatorMaxTokens = 512

// truncatedStopReasons 는 provider 가 max-tokens 도달로 응답을 truncate 한 경우의 StopReason 값입니다.
//
//   - openai     : "length"
//   - gemini     : "MAX_TOKENS"
//   - anthropic  : "max_tokens"
//
// 검증 응답에서 본 값을 만나면 명시 에러로 분류 — best-effort fallback 진입 전에 운영 로그로
// truncate 발생을 가시화 + 후속 MaxTokens 조정 신호로 활용.
var truncatedStopReasons = map[string]struct{}{
	"length":     {},
	"max_tokens": {},
	"MAX_TOKENS": {},
}

// llmValidationResponse 는 LLM 의 검증 응답 JSON 구조입니다.
type llmValidationResponse struct {
	Valid  bool   `json:"valid"`
	Reason string `json:"reason"`
}

// validRegex 는 unmarshal 실패 시 verdict 만 salvage 하기 위한 regex fallback 입니다 (이슈 #320).
//
// reason 이 truncate 되어 unmarshal 이 실패해도 "valid": true|false 만 추출하면 검증 결과 자체는
// 유효 — best-effort 통과 (validation skip) 보다 정확. reason 은 빈 문자열로 보고.
var validRegex = regexp.MustCompile(`"valid"\s*:\s*(true|false)`)

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
		MaxTokens: validatorMaxTokens,
	})
	if err != nil {
		return Result{}, fmt.Errorf("llm validation call: %w", err)
	}
	if resp == nil || resp.Content == "" {
		return Result{}, fmt.Errorf("llm returned empty validation response")
	}

	var vr llmValidationResponse
	if perr := parseValidationResponse(resp.Content, &vr); perr != nil {
		// truncate 감지: provider StopReason 이 max-tokens 신호이면 별도 에러로 가시화 (이슈 #320).
		// 운영자가 MaxTokens 증대 / prompt 단축 결정에 활용.
		if _, truncated := truncatedStopReasons[resp.StopReason]; truncated {
			return Result{}, fmt.Errorf("llm validation response truncated (stop_reason=%s, max_tokens=%d): %w",
				resp.StopReason, validatorMaxTokens, perr)
		}
		return Result{}, fmt.Errorf("parse validation response: %w", perr)
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
//
// 우선 첫 `{` ~ 마지막 `}` 추출 후 unmarshal 을 시도하고, 실패 시 (응답 truncate 등) regex
// fallback 으로 `valid` verdict 만 salvage 합니다 (이슈 #320). reason 누락은 운영 영향 없음 —
// 잘못된 selector 가 통과하는 best-effort silent skip 보다 정확.
func parseValidationResponse(content string, out *llmValidationResponse) error {
	s := content
	if i := strings.Index(s, "{"); i >= 0 {
		s = s[i:]
	}
	if i := strings.LastIndex(s, "}"); i >= 0 {
		s = s[:i+1]
	}
	if err := json.Unmarshal([]byte(s), out); err == nil {
		return nil
	} else if salvageErr := salvageValid(s, out); salvageErr == nil {
		return nil
	} else {
		return fmt.Errorf("unmarshal: %w (raw: %q)", err, content)
	}
}

// salvageValid 는 unmarshal 실패한 LLM 응답에서 regex 로 valid verdict 만 추출합니다 (이슈 #320).
//
// 인자 s 는 parseValidationResponse 가 첫 `{` ~ 마지막 `}` 로 trim 한 결과 — LLM 이 응답 서두에
// prompt 를 echo 하거나 prose 를 출력해도 그 영역의 \"valid\": true 같은 문자열을 verdict 로 오인
// 하지 않도록 JSON 블록 내부만 검사 (gemini Medium 반영).
//
// 매칭 실패 시 error 반환 — 호출자가 원본 unmarshal 에러 메시지를 그대로 노출하여 운영 진단성 보존.
// 매칭 성공 시 out.Valid 만 채움 (Reason 은 빈 문자열).
func salvageValid(s string, out *llmValidationResponse) error {
	m := validRegex.FindStringSubmatch(s)
	if len(m) < 2 {
		return errors.New("salvage: no valid key found in truncated response")
	}
	out.Valid = m[1] == "true"
	out.Reason = ""
	return nil
}
