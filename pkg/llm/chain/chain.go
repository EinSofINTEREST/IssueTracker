// Package chain 은 여러 llm.Provider 를 Chain-of-Responsibility 패턴으로 합성합니다 (이슈 #142).
//
// Package chain composes multiple llm.Provider instances into a single fallback chain
// where the next handler is invoked when the current one fails with a delegatable error.
//
// 사용 예시:
//
//	primary,   _ := llm.New(llm.Config{Provider: "claude", APIKey: ...})
//	secondary, _ := llm.New(llm.Config{Provider: "openai", APIKey: ...})
//	fallback,  _ := llm.New(llm.Config{Provider: "gemini", APIKey: ...})
//
//	p := chain.New(primary, secondary, fallback)   // 그 자체가 llm.Provider
//	resp, err := p.Generate(ctx, req)
//
// 호출자 코드는 단일 provider 사용과 동일합니다 — chain 은 투명한 wrapper.
package chain

import (
	"context"
	"errors"

	"issuetracker/pkg/llm"
	"issuetracker/pkg/logger"
)

// providerName 은 Name() 반환값 / 로그 식별자.
const providerName = "chain"

// Provider 는 여러 llm.Provider 를 순차 시도하는 fallback chain 입니다.
//
// 각 handler 가 ctx · 동일 Request 로 호출되며, 위임 가능한 에러 (shouldDelegate==true) 시
// 다음 handler 로 전파합니다. 모든 handler 가 실패하면 마지막 에러를 반환합니다.
//
// goroutine-safe — handlers 슬라이스는 New 시점 이후 변하지 않으며 각 handler 호출은
// stateless 라 가정 (provider 패키지의 표준 보장).
type Provider struct {
	handlers []llm.Provider
	log      *logger.Logger // 위임 발생 시 INFO 로그 (nil 허용 — 미주입 시 로그 skip)
}

// Option 은 Provider 생성 옵션입니다.
type Option func(*Provider)

// WithLogger 는 위임 발생 / 모든 handler 실패 시 INFO 로그를 출력할 logger 를 주입합니다.
// 미주입 (nil) 이면 로그가 출력되지 않습니다 — 기존 동작 보존.
func WithLogger(log *logger.Logger) Option {
	return func(p *Provider) { p.log = log }
}

// New 는 handlers 를 순서대로 시도하는 ChainProvider 를 반환합니다.
//
// handlers 가 비어있어도 panic 없이 생성됩니다 — 호출 시 ErrCodeBadRequest 반환.
// (호출 시점에 명시적 에러가 더 디버깅 친화적이라 생성 시점은 관대하게 처리.)
func New(handlers ...llm.Provider) *Provider {
	return NewWithOptions(handlers, nil)
}

// NewWithOptions 는 handlers + 옵션으로 ChainProvider 를 생성합니다.
func NewWithOptions(handlers []llm.Provider, opts []Option) *Provider {
	p := &Provider{handlers: handlers}
	for _, o := range opts {
		o(p)
	}
	return p
}

// Name 은 chain 식별자를 반환합니다 (llm.Provider 구현).
func (p *Provider) Name() string { return providerName }

// Generate 는 handlers 를 순서대로 시도하여 첫 성공 응답을 반환합니다 (llm.Provider 구현).
//
// 흐름:
//  1. handlers 가 비어있음 → ErrCodeBadRequest 즉시 반환
//  2. ctx 가 이미 cancel → ErrCodeNetwork (transport-like) 즉시 반환
//  3. handler 별 순회:
//     - 성공     → 즉시 반환
//     - !delegatable (BadRequest) → 즉시 반환 (다른 handler 도 동일 실패)
//     - delegatable → 다음 handler 시도 전 ctx 취소 검사 →
//     취소면 명시적 ErrCodeNetwork 반환 (이전 handler 의 lastErr 로 가리지 않음),
//     아니면 위임 로그 후 다음 handler 시도 (lastErr 보존)
//  4. 모든 handler 실패 → 마지막 에러 반환
func (p *Provider) Generate(ctx context.Context, req llm.Request) (*llm.Response, error) {
	if len(p.handlers) == 0 {
		return nil, &llm.Error{
			Code:     llm.ErrCodeBadRequest,
			Provider: providerName,
			Message:  "chain has no handlers",
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, &llm.Error{
			Code:     llm.ErrCodeNetwork,
			Provider: providerName,
			Message:  "context already canceled before first handler",
			Err:      err,
		}
	}

	var lastErr error
	for i, h := range p.handlers {
		resp, err := h.Generate(ctx, req)
		if err == nil {
			return resp, nil
		}
		if !shouldDelegate(err) {
			// 입력 자체 문제 — 다른 handler 도 동일 실패. 즉시 종결.
			p.logTermination(h.Name(), i, err, "non-delegatable")
			return nil, err
		}
		// ctx 취소가 handler 내부에서 일어났을 수 있음 — 다음 handler 호출 전에 즉시 검사.
		// 명시적 ErrCodeNetwork 로 반환하여 호출자가 "취소" 를 즉답 가능 (Gemini code review #2).
		// 위임 로그 호출 전에 검사하여 취소된 상황에서 불필요한 위임 로그 회피.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, &llm.Error{
				Code:     llm.ErrCodeNetwork,
				Provider: providerName,
				Message:  "context canceled during chain execution",
				Err:      ctxErr,
			}
		}
		p.logDelegation(h.Name(), i, err)
		lastErr = err
	}

	// 모든 handler 실패 — 마지막 에러를 그대로 반환 (래핑 없이 ErrorCode 정보 보존).
	p.logExhausted(lastErr)
	return nil, lastErr
}

// shouldDelegate 는 에러를 다음 handler 에 위임할 가치가 있는지 판단합니다.
//
//   - *llm.Error 가 아닌 경우 → 다음 handler 시도 (보수적 — 정상 가능성 남김)
//   - BadRequest → 즉시 종결 (입력 형식/파라미터 자체 문제, 다른 provider 도 동일 reject)
//   - 그 외 (Auth/RateLimit/Server/Network/Unknown/ContextLimit) → 다음 handler 시도
//
// 정책 근거:
//   - Auth: 잘못된 API key — 다른 provider 의 key 는 유효할 수 있음
//   - RateLimit: 이 provider 의 한도 초과 — 다른 provider 는 별도 한도
//   - Server: 이 provider 측 일시 장애 — 다른 provider 는 정상 가능성
//   - Network: 일반적인 transport 문제. 다음 handler 시도가 합리적.
//   - ContextLimit: provider/모델 마다 context window 가 크게 다름 — 예:
//     gpt-4o-mini ≈128k, claude-opus-4-7 ≈200k, gemini-1.5-pro ≈2M.
//     한 모델에서 ContextLimit 이라도 더 큰 window 를 가진 다른 모델은 처리 가능.
//     Gemini code review #1 피드백 — 위임 대상으로 포함.
//   - BadRequest: 형식 / 파라미터 자체 문제 — 다른 provider 도 동일 reject 가 합리적 가정.
func shouldDelegate(err error) bool {
	var lerr *llm.Error
	if !errors.As(err, &lerr) {
		return true
	}
	switch lerr.Code {
	case llm.ErrCodeBadRequest:
		return false
	default:
		return true
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 관찰성 헬퍼 (logger nil 허용)
// ─────────────────────────────────────────────────────────────────────────────

// logDelegation 은 handler 가 위임 가능한 에러로 실패해 다음으로 넘어갈 때 호출됩니다.
func (p *Provider) logDelegation(handlerName string, index int, err error) {
	if p.log == nil {
		return
	}
	p.log.WithFields(map[string]interface{}{
		"handler":       handlerName,
		"handler_index": index,
		"total":         len(p.handlers),
	}).WithError(err).Info("llm chain delegating to next handler")
}

// logTermination 은 non-delegatable 에러로 chain 이 즉시 종결될 때 호출됩니다.
func (p *Provider) logTermination(handlerName string, index int, err error, reason string) {
	if p.log == nil {
		return
	}
	p.log.WithFields(map[string]interface{}{
		"handler":       handlerName,
		"handler_index": index,
		"reason":        reason,
	}).WithError(err).Info("llm chain terminated without delegation")
}

// logExhausted 는 모든 handler 가 위임 가능한 에러로 실패했을 때 호출됩니다.
func (p *Provider) logExhausted(lastErr error) {
	if p.log == nil {
		return
	}
	p.log.WithFields(map[string]interface{}{
		"handlers": len(p.handlers),
	}).WithError(lastErr).Warn("llm chain exhausted — all handlers failed")
}
