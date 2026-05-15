// 본 파일은 enrich 단계의 외부 맥락 수집기 (Contextualizer) 를 정의합니다 (이슈 #449).
//
// Contextualizer 는 추출된 entities / claims 를 입력으로 받아 page 외부의 배경·타임라인·
// 함의 정보를 PageContext 로 반환합니다. Claude WebFetch 도구를 적극 활용하도록 prompt 가
// 유도 — 위키피디아 / 공식 페이지 / 정책 문서 등에서 직접 페치.

package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"issuetracker/pkg/llm/prompt"
)

// ContextInput 은 Contextualizer.Provide 의 입력입니다.
type ContextInput struct {
	URL      string
	Host     string
	Title    string
	Entities []Entity
	Claims   []Claim
}

// Contextualizer 는 page 외부의 맥락 정보를 PageContext 로 반환합니다.
//
// 실패 시 worker 가 nil context 로 fallback — forward 보장 (forward-first 정책).
type Contextualizer interface {
	Provide(ctx context.Context, in ContextInput) (*PageContext, error)
}

// NoopContextualizer 는 항상 nil context 를 반환합니다 (claudegen 미configured fallback).
type NoopContextualizer struct{}

// NewNoopContextualizer 는 NoopContextualizer 인스턴스를 반환합니다.
func NewNoopContextualizer() *NoopContextualizer { return &NoopContextualizer{} }

// Provide 는 항상 nil 반환 — context 미첨부.
func (c *NoopContextualizer) Provide(_ context.Context, _ ContextInput) (*PageContext, error) {
	return nil, nil
}

// 컴파일 타임 인터페이스 만족 검증.
var _ Contextualizer = (*NoopContextualizer)(nil)

// contextPromptName 은 외부 맥락 수집 prompt asset 경로입니다.
const contextPromptName = "claudegen/enricher_context"

// ClaudegenContextualizer 는 claude.Pool 로 context session 을 실행합니다.
type ClaudegenContextualizer struct {
	runner SessionRunner
	loader prompt.Loader
}

// NewClaudegenContextualizer 는 claudegen-backed Contextualizer 를 생성합니다.
func NewClaudegenContextualizer(runner SessionRunner, loader prompt.Loader) (*ClaudegenContextualizer, error) {
	if runner == nil {
		return nil, errors.New("enrich/core: agent runner must not be nil")
	}
	if loader == nil {
		return nil, errors.New("enrich/core: prompt loader must not be nil")
	}
	return &ClaudegenContextualizer{runner: runner, loader: loader}, nil
}

// Provide 는 claudegen 세션을 실행하고 stdout 을 PageContext 로 파싱합니다.
//
// entities + claims 가 모두 비어있으면 호출 skip — context 산출 근거가 없음.
func (c *ClaudegenContextualizer) Provide(ctx context.Context, in ContextInput) (*PageContext, error) {
	if len(in.Entities) == 0 && len(in.Claims) == 0 {
		return nil, nil
	}

	tpl, err := c.loader.Load(contextPromptName)
	if err != nil {
		return nil, fmt.Errorf("load context prompt %q: %w", contextPromptName, err)
	}

	entitiesJSON, err := marshalSliceForPrompt(in.Entities)
	if err != nil {
		return nil, fmt.Errorf("marshal entities: %w", err)
	}
	// context prompt 는 idx 가 불필요 — 단순 직렬화 사용 (verifier 와 다른 점).
	claimsJSON, err := marshalSliceForPrompt(in.Claims)
	if err != nil {
		return nil, fmt.Errorf("marshal claims: %w", err)
	}

	promptText := prompt.Render(tpl,
		"{{URL}}", in.URL,
		"{{HOST}}", in.Host,
		"{{TITLE}}", in.Title,
		"{{ENTITIES_JSON}}", entitiesJSON,
		"{{CLAIMS_JSON}}", claimsJSON,
	)

	stdout, err := c.runner.RunSession(ctx, "enrich-context", nil, promptText)
	if err != nil {
		return nil, fmt.Errorf("claudegen context session: %w", err)
	}

	pageCtx, err := parseContextOutput(stdout)
	if err != nil {
		return nil, fmt.Errorf("parse context output: %w", err)
	}
	return pageCtx, nil
}

// 컴파일 타임 인터페이스 만족 검증.
var _ Contextualizer = (*ClaudegenContextualizer)(nil)

// parseContextOutput 는 claudegen stdout JSON 을 PageContext 로 파싱합니다.
//
// 응답이 비어있거나 모든 sub-field 가 비어있어도 *PageContext 자체는 반환 — 호출자가
// 명시적으로 nil/non-nil 을 통해 "수집 시도 완료" 신호로 활용 가능.
// 다만 모든 sub-field 가 empty 면 호출자 (worker) 가 attach skip 결정 가능 (helper IsEmpty).
func parseContextOutput(output string) (*PageContext, error) {
	body := stripFences(output)
	if body == "" {
		return nil, errors.New("empty output")
	}
	var pc PageContext
	if err := json.Unmarshal([]byte(body), &pc); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	// nil slice → empty slice 정규화 (downstream 일관성).
	if pc.Background == nil {
		pc.Background = []BackgroundItem{}
	}
	if pc.Timeline == nil {
		pc.Timeline = []TimelineEvent{}
	}
	// Implications 가 모두 빈 문자열이면 객체 자체를 nil 로 — JSON output 의 noise 제거.
	if pc.Implications != nil &&
		pc.Implications.Political == "" &&
		pc.Implications.Social == "" &&
		pc.Implications.Technical == "" {
		pc.Implications = nil
	}
	return &pc, nil
}

// IsEmpty 는 PageContext 의 모든 필드가 empty 인지 검사합니다.
// worker 가 빈 context 첨부를 skip 할 때 사용.
func (c *PageContext) IsEmpty() bool {
	if c == nil {
		return true
	}
	return len(c.Background) == 0 && len(c.Timeline) == 0 && c.Implications == nil
}
