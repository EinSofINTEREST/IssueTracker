package validate_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"issuetracker/internal/crawler/core"
	"issuetracker/internal/processor/validate/community"
	"issuetracker/pkg/config"
)

func newCommunityContent() *core.Content {
	return &core.Content{
		ID:          "comm-001",
		SourceID:    "reddit",
		SourceType:  core.SourceTypeCommunity,
		Country:     "US",
		Language:    "en",
		Title:       "Interesting discussion thread",
		Body:        strings.Repeat("This is a community post with some discussion. ", 5),
		URL:         "https://reddit.com/r/news/comments/abc",
		PublishedAt: time.Now(),
	}
}

func TestCommunityValidator_Validate_ValidContent_ReturnsValid(t *testing.T) {
	v := community.NewValidator(config.DefaultValidateConfig())
	content := newCommunityContent()

	result := v.Validate(context.Background(), content)

	assert.True(t, result.IsValid)
	assert.Empty(t, result.Errors)
	assert.GreaterOrEqual(t, result.QualityScore, float32(0.4))
}

func TestCommunityValidator_Validate_BodyTooShort_ReturnsError(t *testing.T) {
	v := community.NewValidator(config.DefaultValidateConfig())
	content := newCommunityContent()
	content.Body = "짧음"

	result := v.Validate(context.Background(), content)

	assert.False(t, result.IsValid)
	assert.Equal(t, "Body", result.Errors[0].Field)
	assert.Equal(t, "min_length", result.Errors[0].Rule)
}

func TestCommunityValidator_Validate_MissingPublishedAt_StillValid(t *testing.T) {
	v := community.NewValidator(config.DefaultValidateConfig())
	content := newCommunityContent()
	content.PublishedAt = time.Time{} // 커뮤니티는 PublishedAt 선택

	result := v.Validate(context.Background(), content)

	// PublishedAt 없어도 검증 실패가 아님 (단, 점수는 낮아짐)
	assert.Empty(t, result.Errors)
}

func TestCommunityValidator_Validate_FloodPattern_ReturnsError(t *testing.T) {
	v := community.NewValidator(config.DefaultValidateConfig())
	content := newCommunityContent()
	// 30% 이상이 반복 문자
	content.Body = strings.Repeat("ㅋ", 50) + " " + strings.Repeat("ㅋ", 50)

	result := v.Validate(context.Background(), content)

	assert.False(t, result.IsValid)
	found := false
	for _, e := range result.Errors {
		if e.Rule == "spam_flood" {
			found = true
			break
		}
	}
	assert.True(t, found, "spam_flood error expected")
}

func TestCommunityValidator_Validate_ShortRepeatNotFlood_ReturnsValid(t *testing.T) {
	v := community.NewValidator(config.DefaultValidateConfig())
	content := newCommunityContent()
	// 반복이 짧아서(3회 이하) 도배로 감지되지 않음
	content.Body = strings.Repeat("ㅋㅋㅋ content is fine here. ", 5)

	result := v.Validate(context.Background(), content)

	// spam_flood 에러 없어야 함
	for _, e := range result.Errors {
		assert.NotEqual(t, "spam_flood", e.Rule)
	}
}

func TestCommunityValidator_Validate_QualityScoreRange(t *testing.T) {
	v := community.NewValidator(config.DefaultValidateConfig())

	tests := []struct {
		name    string
		content *core.Content
	}{
		{"full metadata", newCommunityContent()},
		{"no title", func() *core.Content { c := newCommunityContent(); c.Title = ""; return c }()},
		{"no url", func() *core.Content { c := newCommunityContent(); c.URL = ""; return c }()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := v.Validate(context.Background(), tt.content)
			assert.GreaterOrEqual(t, result.QualityScore, float32(0.0))
			assert.LessOrEqual(t, result.QualityScore, float32(1.0))
		})
	}
}
