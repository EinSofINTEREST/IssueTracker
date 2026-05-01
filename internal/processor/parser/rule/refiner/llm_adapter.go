package refiner

import (
	"context"
	"errors"

	"issuetracker/internal/processor/parser/rule/pathinfer"
	"issuetracker/pkg/llm"
)

// providerAdapter 는 pkg/llm.Provider 를 pathinfer.LLMClient 로 wrapping 하는 어댑터입니다 (이슈 #173 단계 4-2).
//
// pathinfer 패키지의 LLMClient 인터페이스는 system + user 두 string 만 받음 — 호출자가 pkg/llm 의
// Request / Response 타입을 직접 다루지 않도록 분리됨. 본 adapter 가 두 string 을 llm.Request 로
// 변환 + Response.Content 추출.
type providerAdapter struct {
	provider llm.Provider
}

// NewLLMAdapter 는 pkg/llm.Provider 를 pathinfer.LLMClient 로 변환하는 어댑터를 반환합니다.
//
// provider 가 nil 이면 panic — wire 누락 즉시 가시화.
func NewLLMAdapter(provider llm.Provider) pathinfer.LLMClient {
	if provider == nil {
		panic("refiner: NewLLMAdapter requires non-nil provider")
	}
	return &providerAdapter{provider: provider}
}

// Generate 는 llm.Provider.Generate 를 호출하고 응답 텍스트만 반환합니다.
//
//   - system 이 비어있지 않으면 RoleSystem 메시지로 첫 번째 추가
//   - user 는 RoleUser 메시지로 항상 추가
//   - TaskHint 는 비워둠 (path_pattern 추론은 단순 응답 — 특정 hint 미적용)
//   - resp.Content 가 빈 문자열이면 에러
func (a *providerAdapter) Generate(ctx context.Context, system, user string) (string, error) {
	messages := make([]llm.Message, 0, 2)
	if system != "" {
		messages = append(messages, llm.Message{Role: llm.RoleSystem, Content: system})
	}
	messages = append(messages, llm.Message{Role: llm.RoleUser, Content: user})

	resp, err := a.provider.Generate(ctx, llm.Request{Messages: messages})
	if err != nil {
		return "", err
	}
	if resp == nil || resp.Content == "" {
		return "", errors.New("llm provider returned empty response")
	}
	return resp.Content, nil
}

// 컴파일 타임 인터페이스 만족 검증.
var _ pathinfer.LLMClient = (*providerAdapter)(nil)
