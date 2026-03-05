package validate_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"issuetracker/internal/crawler/core"
	"issuetracker/internal/processor/validate/news"
	"issuetracker/pkg/config"
)

func newNewsContent() *core.Content {
	return &core.Content{
		ID:          "content-001",
		SourceID:    "cnn",
		SourceType:  core.SourceTypeNews,
		Country:     "US",
		Language:    "en",
		Title:       "Breaking News: Major Event Occurs in Capital City",
		Body:        strings.Repeat("This is a test article body sentence. ", 10),
		Summary:     "Short summary of the article.",
		Author:      "Jane Doe",
		PublishedAt: time.Now(),
		Category:    "Politics",
		Tags:        []string{"news", "politics"},
		URL:         "https://cnn.com/article/123",
		WordCount:   80,
	}
}

func TestNewsValidator_Validate_ValidContent_ReturnsValid(t *testing.T) {
	v := news.NewValidator(config.DefaultValidateConfig())
	content := newNewsContent()

	result := v.Validate(context.Background(), content)

	assert.True(t, result.IsValid)
	assert.Empty(t, result.Errors)
	assert.GreaterOrEqual(t, result.QualityScore, float32(0.5))
}

func TestNewsValidator_Validate_TitleTooShort_ReturnsError(t *testing.T) {
	v := news.NewValidator(config.DefaultValidateConfig())
	content := newNewsContent()
	content.Title = "Short"

	result := v.Validate(context.Background(), content)

	assert.False(t, result.IsValid)
	assert.NotEmpty(t, result.Errors)
	assert.Equal(t, "Title", result.Errors[0].Field)
	assert.Equal(t, "min_length", result.Errors[0].Rule)
}

func TestNewsValidator_Validate_TitleTooLong_ReturnsError(t *testing.T) {
	v := news.NewValidator(config.DefaultValidateConfig())
	content := newNewsContent()
	content.Title = strings.Repeat("a", 501)

	result := v.Validate(context.Background(), content)

	assert.False(t, result.IsValid)
	assert.Equal(t, "max_length", result.Errors[0].Rule)
}

func TestNewsValidator_Validate_BodyTooShort_ReturnsError(t *testing.T) {
	v := news.NewValidator(config.DefaultValidateConfig())
	content := newNewsContent()
	content.Body = "Too short."

	result := v.Validate(context.Background(), content)

	assert.False(t, result.IsValid)
	assert.Equal(t, "Body", result.Errors[0].Field)
	assert.Equal(t, "min_length", result.Errors[0].Rule)
}

func TestNewsValidator_Validate_BodyTooLong_ReturnsError(t *testing.T) {
	v := news.NewValidator(config.DefaultValidateConfig())
	content := newNewsContent()
	content.Body = strings.Repeat("a", 50001)

	result := v.Validate(context.Background(), content)

	assert.False(t, result.IsValid)
	assert.Equal(t, "max_length", result.Errors[0].Rule)
}

func TestNewsValidator_Validate_MissingPublishedAt_ReturnsError(t *testing.T) {
	v := news.NewValidator(config.DefaultValidateConfig())
	content := newNewsContent()
	content.PublishedAt = time.Time{}

	result := v.Validate(context.Background(), content)

	assert.False(t, result.IsValid)
	assert.Equal(t, "PublishedAt", result.Errors[0].Field)
	assert.Equal(t, "required", result.Errors[0].Rule)
}

func TestNewsValidator_Validate_ExcessiveCaps_ReturnsSpamError(t *testing.T) {
	v := news.NewValidator(config.DefaultValidateConfig())
	content := newNewsContent()
	// 30% 이상 대문자
	content.Title = "THIS IS A VERY LONG TITLE THAT IS ENTIRELY IN UPPERCASE LETTERS"
	content.Body = strings.Repeat("THIS IS AN UPPERCASE BODY SENTENCE. ", 10)

	result := v.Validate(context.Background(), content)

	assert.False(t, result.IsValid)
	found := false
	for _, e := range result.Errors {
		if e.Rule == "spam_caps" {
			found = true
			break
		}
	}
	assert.True(t, found, "spam_caps error expected")
}

func TestNewsValidator_Validate_QualityScoreRange(t *testing.T) {
	v := news.NewValidator(config.DefaultValidateConfig())

	tests := []struct {
		name    string
		content *core.Content
	}{
		{"full metadata", newNewsContent()},
		{"no author", func() *core.Content { c := newNewsContent(); c.Author = ""; return c }()},
		{"no category", func() *core.Content { c := newNewsContent(); c.Category = ""; return c }()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := v.Validate(context.Background(), tt.content)
			assert.GreaterOrEqual(t, result.QualityScore, float32(0.0))
			assert.LessOrEqual(t, result.QualityScore, float32(1.0))
		})
	}
}

func TestNewsValidator_Validate_MultipleErrors_AllReported(t *testing.T) {
	v := news.NewValidator(config.DefaultValidateConfig())
	content := newNewsContent()
	content.Title = "Short"
	content.Body = "Too short."
	content.PublishedAt = time.Time{}

	result := v.Validate(context.Background(), content)

	assert.False(t, result.IsValid)
	assert.GreaterOrEqual(t, len(result.Errors), 3)
}
