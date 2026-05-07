package validator

import (
	"context"

	"issuetracker/internal/processor/parser/rule/llmgen"
	"issuetracker/internal/storage"
)

// LLMGenAdapter 는 validator.Pool 을 llmgen.SelectorValidator 인터페이스로 감쌉니다.
// 두 패키지 간 import cycle 없이 연결합니다.
type LLMGenAdapter struct {
	pool *Pool
}

// NewLLMGenAdapter 는 LLMGenAdapter 를 생성합니다.
func NewLLMGenAdapter(pool *Pool) *LLMGenAdapter {
	return &LLMGenAdapter{pool: pool}
}

func (a *LLMGenAdapter) Validate(ctx context.Context, html string, selectors storage.SelectorMap, targetType storage.TargetType) (llmgen.SelectorValidatorResult, error) {
	res, err := a.pool.Validate(ctx, html, selectors, targetType)
	return llmgen.SelectorValidatorResult{Valid: res.Valid, Reason: res.Reason}, err
}
