package validator

import (
	"context"
	"errors"
	"fmt"

	"issuetracker/internal/storage"
	"issuetracker/pkg/logger"
)

// Pool 은 여러 Validator 를 순서대로 시도하는 복합 검증기입니다 (이슈 #257).
//
// 정책: 첫 번째로 응답한 validator 의 결과(valid/invalid)를 반환합니다.
// API 오류가 발생한 validator 는 건너뛰고 다음을 시도합니다.
// 모든 validator 가 API 오류를 반환하면 best-effort 통과 (룰 INSERT 차단 안 함) — 검증 인프라 장애가
// rule 생성 자체를 막지 않도록 설계 (이슈 #257 요구사항: 운영 안정성 우선).
type Pool struct {
	validators []Validator
	log        *logger.Logger
}

// NewPool 은 Pool 을 생성합니다. validators 가 비어 있으면 noop (항상 valid 반환).
func NewPool(log *logger.Logger, validators ...Validator) *Pool {
	return &Pool{validators: validators, log: log}
}

func (p *Pool) Validate(ctx context.Context, html string, selectors storage.SelectorMap, targetType storage.TargetType) (Result, error) {
	if len(p.validators) == 0 {
		return Result{Valid: true, Reason: "no validators configured"}, nil
	}

	var apiErrs []error
	for i, v := range p.validators {
		res, err := v.Validate(ctx, html, selectors, targetType)
		if err != nil {
			p.log.WithFields(map[string]interface{}{
				"validator_index": i,
				"target_type":     string(targetType),
			}).WithError(err).Warn("semantic validator API error, trying next")
			apiErrs = append(apiErrs, err)
			continue
		}
		// 첫 응답 결과 즉시 반환 (성공이든 실패든)
		return res, nil
	}

	// 모든 validator API 오류 — best-effort 통과
	p.log.WithFields(map[string]interface{}{
		"target_type":     string(targetType),
		"validator_count": len(p.validators),
	}).Warn("all semantic validators failed with API errors, allowing rule insertion (best-effort)")
	return Result{Valid: true, Reason: fmt.Sprintf("all validators unavailable (%d errors)", len(apiErrs))},
		errors.Join(apiErrs...)
}
