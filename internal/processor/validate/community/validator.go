// Package community는 커뮤니티 소스 전용 Content 검증 로직을 구현합니다.
//
// Package community implements content validation for community source types.
// Rules are more lenient than news: shorter minimum body, optional PublishedAt,
// and community-specific spam detection (repetitive characters, flood patterns).
package community

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"issuetracker/internal/crawler/core"
	"issuetracker/internal/processor"
	"issuetracker/pkg/config"
)

// Validator는 커뮤니티 컨텐츠 전용 검증기입니다.
//
// Validator is the community-specific content validator.
// It applies lenient rules suited for informal community posts.
type Validator struct {
	cfg config.ValidateConfig
}

// NewValidator는 새로운 커뮤니티 Validator를 반환합니다.
func NewValidator(cfg config.ValidateConfig) *Validator {
	return &Validator{cfg: cfg}
}

// Validate는 커뮤니티 컨텐츠의 필드 검증, 품질 점수, 도배 패턴을 검사합니다.
func (v *Validator) Validate(_ context.Context, content *core.Content) processor.ValidationResult {
	var errs []processor.ValidationError

	errs = append(errs, v.validateBody(content)...)
	errs = append(errs, v.detectFlood(content)...)

	score := v.qualityScore(content)
	isValid := len(errs) == 0 && score >= float32(v.cfg.CommunityQualityThreshold)

	return processor.ValidationResult{
		IsValid:      isValid,
		QualityScore: score,
		Errors:       errs,
	}
}

func (v *Validator) validateBody(c *core.Content) []processor.ValidationError {
	length := utf8.RuneCountInString(c.Body)
	if length < v.cfg.CommunityBodyMinLen {
		return []processor.ValidationError{{
			Field:   "Body",
			Rule:    "min_length",
			Message: fmt.Sprintf("body must be at least %d characters, got %d", v.cfg.CommunityBodyMinLen, length),
		}}
	}
	return nil
}

// detectFlood는 동일 문자 반복(도배) 패턴을 탐지합니다.
// 예: "ㅋㅋㅋㅋㅋ", "aaaaa", "!!!!!"
func (v *Validator) detectFlood(c *core.Content) []processor.ValidationError {
	text := c.Body
	runes := []rune(text)
	total := len(runes)
	if total == 0 {
		return nil
	}

	repeatCount := v.countRepeatRunes(runes)
	if float64(repeatCount)/float64(total) > v.cfg.CommunityMaxRepeatRatio {
		return []processor.ValidationError{{
			Field:   "Body",
			Rule:    "spam_flood",
			Message: fmt.Sprintf("flood pattern detected (%.0f%% repetitive characters)", float64(repeatCount)/float64(total)*100),
		}}
	}

	return nil
}

// countRepeatRunes는 연속 반복 문자(CommunityMinRepeatRun 이상)에 속하는 문자 수를 반환합니다.
func (v *Validator) countRepeatRunes(runes []rune) int {
	count := 0
	i := 0
	for i < len(runes) {
		run := 1
		for i+run < len(runes) && runes[i+run] == runes[i] {
			run++
		}
		if run >= v.cfg.CommunityMinRepeatRun {
			count += run
		}
		i += run
	}
	return count
}

// qualityScore는 본문 길이, 메타데이터, 구조 완성도를 기반으로 품질 점수를 산출합니다.
func (v *Validator) qualityScore(c *core.Content) float32 {
	bodyScore := v.scoreBody(c.Body)
	metaScore := scoreMeta(c)

	return float32(bodyScore*0.6 + metaScore*0.4)
}

func (v *Validator) scoreBody(body string) float64 {
	length := utf8.RuneCountInString(body)
	if length < v.cfg.CommunityBodyMinLen {
		return 0.0
	}
	if length >= 200 {
		return 1.0
	}
	return float64(length) / 200.0
}

func scoreMeta(c *core.Content) float64 {
	score := 0.0
	if c.Title != "" {
		score += 0.4
	}
	if !c.PublishedAt.IsZero() {
		score += 0.3
	}
	if strings.TrimSpace(c.URL) != "" {
		score += 0.3
	}
	return score
}
