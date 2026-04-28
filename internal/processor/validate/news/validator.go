// Package news는 뉴스 소스 전용 Content 검증 로직을 구현합니다.
//
// Package news implements content validation for news source types.
// It enforces stricter rules than community: title length, body length,
// required PublishedAt, quality scoring, and spam detection.
package news

import (
	"context"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"issuetracker/internal/crawler/core"
	"issuetracker/internal/processor"
	"issuetracker/pkg/config"
)

// Validator는 뉴스 컨텐츠 전용 검증기입니다.
//
// Validator is the news-specific content validator.
// It enforces news domain rules and computes a quality score.
type Validator struct {
	cfg config.ValidateConfig
}

// NewValidator는 새로운 뉴스 Validator를 반환합니다.
func NewValidator(cfg config.ValidateConfig) *Validator {
	return &Validator{cfg: cfg}
}

// Validate는 뉴스 컨텐츠의 필수 필드, 품질 점수, 스팸 여부를 검증합니다.
func (v *Validator) Validate(_ context.Context, content *core.Content) processor.ValidationResult {
	var errs []processor.ValidationError

	errs = append(errs, v.validateTitle(content)...)
	errs = append(errs, v.validateBody(content)...)
	errs = append(errs, v.validatePublishedAt(content)...)
	errs = append(errs, v.detectSpam(content)...)

	score := v.qualityScore(content)
	threshold := float32(v.cfg.NewsQualityThreshold)

	// 이슈 #135 — 품질 점수 임계 미달이고 다른 reject 사유 없는 경우, quality_low 에러를
	// breakdown 과 함께 명시적으로 추가. 이전에는 errs=[] 인 채로 reject 되어 사후 추적 불가.
	if score < threshold && len(errs) == 0 {
		wordScore := v.scoreWordCount(content.WordCount)
		metaScore := scoreMetadata(content)
		structScore := scoreStructure(content)
		errs = append(errs, processor.ValidationError{
			Field: "QualityScore",
			Rule:  "quality_low",
			Message: fmt.Sprintf(
				"quality score %.3f below threshold %.3f (word=%.3f, meta=%.3f, struct=%.3f)",
				score, threshold, wordScore, metaScore, structScore,
			),
		})
	}

	isValid := len(errs) == 0 && score >= threshold

	return processor.ValidationResult{
		IsValid:      isValid,
		QualityScore: score,
		Errors:       errs,
	}
}

func (v *Validator) validateTitle(c *core.Content) []processor.ValidationError {
	length := utf8.RuneCountInString(c.Title)
	if length < v.cfg.NewsTitleMinLen {
		return []processor.ValidationError{{
			Field:   "Title",
			Rule:    "min_length",
			Message: fmt.Sprintf("title must be at least %d characters, got %d", v.cfg.NewsTitleMinLen, length),
		}}
	}
	if length > v.cfg.NewsTitleMaxLen {
		return []processor.ValidationError{{
			Field:   "Title",
			Rule:    "max_length",
			Message: fmt.Sprintf("title must be at most %d characters, got %d", v.cfg.NewsTitleMaxLen, length),
		}}
	}
	return nil
}

func (v *Validator) validateBody(c *core.Content) []processor.ValidationError {
	length := utf8.RuneCountInString(c.Body)
	if length < v.cfg.NewsBodyMinLen {
		return []processor.ValidationError{{
			Field:   "Body",
			Rule:    "min_length",
			Message: fmt.Sprintf("body must be at least %d characters, got %d", v.cfg.NewsBodyMinLen, length),
		}}
	}
	if length > v.cfg.NewsBodyMaxLen {
		return []processor.ValidationError{{
			Field:   "Body",
			Rule:    "max_length",
			Message: fmt.Sprintf("body must be at most %d characters, got %d", v.cfg.NewsBodyMaxLen, length),
		}}
	}
	return nil
}

func (v *Validator) validatePublishedAt(c *core.Content) []processor.ValidationError {
	if c.PublishedAt.IsZero() {
		return []processor.ValidationError{{
			Field:   "PublishedAt",
			Rule:    "required",
			Message: "published_at is required for news content",
		}}
	}
	return nil
}

// detectSpam은 과도한 대문자 및 구두점 비율로 스팸을 탐지합니다.
func (v *Validator) detectSpam(c *core.Content) []processor.ValidationError {
	text := c.Title + " " + c.Body
	runes := []rune(text)
	total := len(runes)
	if total == 0 {
		return nil
	}

	var capCount, punctCount int
	for _, r := range runes {
		if unicode.IsLetter(r) && unicode.IsUpper(r) {
			capCount++
		}
		if unicode.IsPunct(r) || unicode.IsSymbol(r) {
			punctCount++
		}
	}

	var errs []processor.ValidationError

	if float64(capCount)/float64(total) > v.cfg.NewsMaxCapRatio {
		errs = append(errs, processor.ValidationError{
			Field:   "Body",
			Rule:    "spam_caps",
			Message: fmt.Sprintf("excessive capitalization detected (%.0f%%)", float64(capCount)/float64(total)*100),
		})
	}

	if float64(punctCount)/float64(total) > v.cfg.NewsMaxPunctRatio {
		errs = append(errs, processor.ValidationError{
			Field:   "Body",
			Rule:    "spam_punct",
			Message: fmt.Sprintf("excessive punctuation detected (%.0f%%)", float64(punctCount)/float64(total)*100),
		})
	}

	return errs
}

// qualityScore는 word count, 메타데이터 존재 여부, 구조적 완성도를 기반으로 품질 점수를 산출합니다.
// 반환값 범위: 0.0 ~ 1.0
func (v *Validator) qualityScore(c *core.Content) float32 {
	wordScore := v.scoreWordCount(c.WordCount)
	metaScore := scoreMetadata(c)
	structScore := scoreStructure(c)

	return float32(wordScore*v.cfg.NewsWeightWordCount + metaScore*v.cfg.NewsWeightMeta + structScore*v.cfg.NewsWeightStructure)
}

func (v *Validator) scoreWordCount(wc int) float64 {
	minWC := v.cfg.NewsMinWordCount
	if wc <= 0 {
		// WordCount가 미설정이면 Body 길이로 추정
		return 0.5
	}
	if wc >= minWC*4 {
		return 1.0
	}
	if wc < minWC {
		return float64(wc) / float64(minWC) * 0.5
	}
	return 0.5 + float64(wc-minWC)/float64(minWC*3)*0.5
}

func scoreMetadata(c *core.Content) float64 {
	score := 0.0
	if c.Author != "" {
		score += 0.4
	}
	if c.Category != "" {
		score += 0.3
	}
	if len(c.Tags) > 0 {
		score += 0.3
	}
	return score
}

func scoreStructure(c *core.Content) float64 {
	score := 0.0
	if c.URL != "" {
		score += 0.3
	}
	if c.Summary != "" {
		score += 0.3
	}
	if strings.Contains(c.Body, "\n") {
		// 단락 구조 존재
		score += 0.4
	}
	return score
}
