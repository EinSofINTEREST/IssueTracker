// Package validate는 Content 검증 처리 단계를 구현합니다.
//
// Package validate implements the content validation stage of the processing pipeline.
// It dispatches to source-type-specific validators (news, community) via NewValidator,
// and adapts them to ContentProcessor via NewContentProcessor.
package validate

import (
	"context"
	"fmt"

	"issuetracker/internal/crawler/core"
	"issuetracker/internal/processor"
	"issuetracker/internal/processor/validate/community"
	"issuetracker/internal/processor/validate/news"
	"issuetracker/pkg/config"
)

// NewValidator는 SourceType에 맞는 Validator를 반환합니다.
// 알 수 없는 타입은 뉴스 검증기를 기본으로 사용합니다.
//
// NewValidator returns the appropriate Validator for the given SourceType.
// Defaults to the news validator for unknown or social types.
func NewValidator(sourceType core.SourceType, cfg config.ValidateConfig) processor.Validator {
	switch sourceType {
	case core.SourceTypeCommunity:
		return community.NewValidator(cfg)
	default:
		return news.NewValidator(cfg)
	}
}

// NewContentProcessor는 Validator를 감싸는 ContentProcessor를 반환합니다.
// 검증 통과 시 content.Reliability를 QualityScore로 설정하고 반환합니다.
// 검증 실패(IsValid == false) 시 에러를 반환하며, 호출자(Worker)가 DLQ로 라우팅합니다.
//
// NewContentProcessor wraps a Validator as a ContentProcessor.
// On success, sets content.Reliability = QualityScore and returns the content.
// On failure, returns an error so the Worker can route to DLQ.
func NewContentProcessor(v processor.Validator) processor.ContentProcessor {
	return &validatorProcessor{validator: v}
}

// validatorProcessor는 Validator를 ContentProcessor로 어댑팅합니다.
type validatorProcessor struct {
	validator processor.Validator
}

// Process는 content를 검증하고 Reliability를 설정합니다.
// 검증 실패 시 core.CrawlerError(Validation 카테고리)로 반환하며,
// 첫 번째 룰에 따라 코드를 부여합니다(임계 미달은 VAL_005, errors 가 비어있는 임계 실패도 VAL_005).
func (p *validatorProcessor) Process(ctx context.Context, content *core.Content) (*core.Content, error) {
	result := p.validator.Validate(ctx, content)
	content.Reliability = result.QualityScore

	if !result.IsValid {
		code := codeForValidationResult(result)
		message := fmt.Sprintf("content %s failed validation: %v", content.ID, result.Errors)
		return nil, core.NewValidationError(code, message, nil)
	}

	return content, nil
}

// codeForValidationResult는 ValidationResult.Errors 에서 첫 번째 룰을 참조하여
// 가장 적절한 에러 코드를 결정합니다. errors 가 비어있으면 임계 미달로 간주합니다.
func codeForValidationResult(result processor.ValidationResult) string {
	if len(result.Errors) == 0 {
		return core.CodeValQualityLow
	}

	switch result.Errors[0].Rule {
	case "min_length":
		return core.CodeValContentShort
	case "max_length":
		return core.CodeValContentLong
	case "required":
		return core.CodeValMissingField
	case "spam_caps", "spam_punct", "spam_flood":
		return core.CodeValSpam
	default:
		return core.CodeValInvalidFormat
	}
}
