package validate_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	core "issuetracker/internal/crawler/core"
	"issuetracker/internal/processor"
	"issuetracker/internal/processor/validate"
)

// stubValidator는 입력에 관계없이 고정된 ValidationResult 를 반환합니다.
type stubValidator struct {
	result processor.ValidationResult
}

func (s *stubValidator) Validate(_ context.Context, _ *core.Content) processor.ValidationResult {
	return s.result
}

// 검증 실패 시 ContentProcessor 가 첫 번째 Rule 기준으로 에러 코드를 부여하는지 검증.
// DLQ 라우팅이 코드에 의존하므로 매핑이 회귀하지 않도록 고정합니다.
func TestContentProcessor_Process_AssignsCodeFromFirstRule(t *testing.T) {
	tests := []struct {
		name     string
		errors   []processor.ValidationError
		wantCode string
	}{
		{
			name:     "min_length → VAL_003",
			errors:   []processor.ValidationError{{Field: "Title", Rule: "min_length"}},
			wantCode: core.CodeValContentShort,
		},
		{
			name:     "max_length → VAL_004",
			errors:   []processor.ValidationError{{Field: "Body", Rule: "max_length"}},
			wantCode: core.CodeValContentLong,
		},
		{
			name:     "required → VAL_001",
			errors:   []processor.ValidationError{{Field: "PublishedAt", Rule: "required"}},
			wantCode: core.CodeValMissingField,
		},
		{
			name:     "spam_caps → VAL_006",
			errors:   []processor.ValidationError{{Field: "Body", Rule: "spam_caps"}},
			wantCode: core.CodeValSpam,
		},
		{
			name:     "spam_punct → VAL_006",
			errors:   []processor.ValidationError{{Field: "Body", Rule: "spam_punct"}},
			wantCode: core.CodeValSpam,
		},
		{
			name:     "spam_flood → VAL_006",
			errors:   []processor.ValidationError{{Field: "Body", Rule: "spam_flood"}},
			wantCode: core.CodeValSpam,
		},
		{
			name:     "unknown rule → VAL_002 (default fallback)",
			errors:   []processor.ValidationError{{Field: "Body", Rule: "weird_rule_xyz"}},
			wantCode: core.CodeValInvalidFormat,
		},
		{
			name:     "no errors but invalid (threshold below) → VAL_005",
			errors:   nil,
			wantCode: core.CodeValQualityLow,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := &stubValidator{
				result: processor.ValidationResult{
					IsValid:      false,
					QualityScore: 0.1,
					Errors:       tt.errors,
				},
			}
			cp := validate.NewContentProcessor(v)

			content := &core.Content{ID: "test-id"}
			out, err := cp.Process(context.Background(), content)

			assert.Nil(t, out)
			require.Error(t, err)

			var ce *core.CrawlerError
			require.True(t, errors.As(err, &ce), "검증 에러는 *core.CrawlerError 여야 함")
			assert.Equal(t, core.ErrCategoryValidation, ce.Category)
			assert.Equal(t, tt.wantCode, ce.Code)
			assert.False(t, ce.Retryable, "validation 에러는 non-retryable")
		})
	}
}

// 검증 통과 시 Reliability 가 QualityScore 로 설정되고 에러 없이 content 가 반환되는지 검증.
func TestContentProcessor_Process_SuccessSetsReliability(t *testing.T) {
	v := &stubValidator{
		result: processor.ValidationResult{
			IsValid:      true,
			QualityScore: 0.85,
			Errors:       nil,
		},
	}
	cp := validate.NewContentProcessor(v)

	content := &core.Content{ID: "ok-id"}
	out, err := cp.Process(context.Background(), content)

	require.NoError(t, err)
	assert.Equal(t, content, out)
	assert.InDelta(t, 0.85, content.Reliability, 1e-6)
}
