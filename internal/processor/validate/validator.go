// Package validate 는 Content 검증 처리 단계를 구현합니다.
//
// Package validate implements the content validation stage of the processing pipeline.
// It dispatches to source-type-specific validators (news, community) via NewValidator.
// Worker 가 Validator 결과를 직접 사용 — 별도 ContentProcessor 어댑터 없음 (이슈 #206).
package validate

import (
	"context"
	"fmt"

	"issuetracker/internal/processor/fetcher/core"
	"issuetracker/internal/processor/validate/community"
	"issuetracker/internal/processor/validate/news"
	"issuetracker/internal/processor/validate/types"
	"issuetracker/pkg/config"
)

// NewValidator 는 SourceType 에 맞는 Validator 를 반환합니다.
// 알 수 없는 타입은 뉴스 검증기를 기본으로 사용합니다.
//
// NewValidator returns the appropriate Validator for the given SourceType.
// Defaults to the news validator for unknown or social types.
func NewValidator(sourceType core.SourceType, cfg config.ValidateConfig) types.Validator {
	switch sourceType {
	case core.SourceTypeCommunity:
		return community.NewValidator(cfg)
	default:
		return news.NewValidator(cfg)
	}
}

// RunValidation 은 content 를 검증하고 결과에 따라 Reliability 설정 + 에러 변환을 수행합니다.
// 이전의 ContentProcessor 어댑터를 대체 (이슈 #206) — 단순 함수로 충분.
//
// 검증 통과 시: content.Reliability = QualityScore 설정, (content, nil) 반환.
// 검증 실패 시: core.CrawlerError(Validation 카테고리) 반환 — 첫 번째 룰 기준 코드 부여.
func RunValidation(ctx context.Context, v types.Validator, content *core.Content) (*core.Content, error) {
	result := v.Validate(ctx, content)
	content.Reliability = result.QualityScore

	if !result.IsValid {
		code := codeForValidationResult(result)
		message := fmt.Sprintf("content %s failed validation: %v", content.ID, result.Errors)
		return nil, core.NewValidationError(code, message, nil)
	}

	return content, nil
}

// codeForValidationResult 는 ValidationResult.Errors 에서 첫 번째 룰을 참조하여
// 가장 적절한 에러 코드를 결정합니다. errors 가 비어있으면 임계 미달로 간주합니다 (legacy 안전망 —
// 이슈 #135 이후 validator 들은 임계 미달 시 명시적으로 quality_low 룰을 errors 에 추가합니다).
func codeForValidationResult(result types.ValidationResult) string {
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
	case "quality_low":
		return core.CodeValQualityLow
	default:
		return core.CodeValInvalidFormat
	}
}
