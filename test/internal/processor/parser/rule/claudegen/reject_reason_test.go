package claudegen_test

// reject_reason_test.go — Sub B (#365) — claudegen prompt 에 reason context 주입 검증.
//
// 검증 항목 (black-box via ExtractEnriched → exec args 캡쳐):
//  1. ctx 에 reason 없으면 prompt 에 placeholder 가 빈 문자열로 치환 → 기존 동작 유지
//  2. ctx 에 reason 있으면 prompt 의 마지막에 "Validation feedback" 블록 삽입
//  3. ctx 에 빈 reason 으로 WithRejectReason 호출 시 placeholder 영향 없음

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/processor/parser/rule/claudegen"
	"issuetracker/internal/processor/parser/rule/llmgen"
	"issuetracker/internal/storage/model"
	"issuetracker/pkg/llm/prompt"
	"issuetracker/pkg/logger"
)

// extractPromptArg 는 ExecSession 호출의 args slice 에서 "-p" 다음 prompt 문자열을 추출합니다.
func extractPromptArg(args []string) string {
	for i, a := range args {
		if a == "-p" && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func TestExtractEnriched_NoReason_PromptHasNoFeedbackBlock(t *testing.T) {
	runner := &mockContainerRunner{
		execStdout: `{"validity":"ok","page_type":"news","selectors":{"title":{"css":"h1"},"main_content":{"css":"article","multi":true}},"self_check":{"title_sample":"x","body_word_count_estimate":200}}`,
	}
	w := newTestWorker(t, runner)
	require.NoError(t, w.Start(t.Context()))
	t.Cleanup(func() { _ = w.Stop(context.Background()) })

	_, err := w.ExtractEnriched(t.Context(), "news.example.com", model.TargetTypePage, "<html><h1>title</h1><article>body</article></html>")
	require.NoError(t, err)

	prompt := extractPromptArg(runner.execedWith.args)
	require.NotEmpty(t, prompt)
	assert.NotContains(t, prompt, "Validation feedback from previous attempt",
		"reason 부재 시 feedback 블록 삽입 X")
	// {{TARGET_TYPE}} 등 다른 placeholder 는 치환됨 (claudegenLoader 의 mock template 안)
	assert.Contains(t, prompt, "(page)")
}

func TestExtractEnriched_WithReason_PromptHasFeedbackBlock(t *testing.T) {
	runner := &mockContainerRunner{
		execStdout: `{"validity":"ok","page_type":"news","selectors":{"title":{"css":"h1"},"main_content":{"css":"article","multi":true}},"self_check":{"title_sample":"x","body_word_count_estimate":200}}`,
	}
	w := newTestWorker(t, runner)
	require.NoError(t, w.Start(t.Context()))
	t.Cleanup(func() { _ = w.Stop(context.Background()) })

	const reason = "PublishedAt required, Title min_length"
	ctx := llmgen.WithRejectReason(t.Context(), reason)
	_, err := w.ExtractEnriched(ctx, "news.example.com", model.TargetTypePage, "<html><h1>title</h1><article>body</article></html>")
	require.NoError(t, err)

	prompt := extractPromptArg(runner.execedWith.args)
	require.NotEmpty(t, prompt)
	assert.Contains(t, prompt, "Validation feedback from previous attempt",
		"reason 존재 시 feedback 블록 삽입됨")
	assert.Contains(t, prompt, reason, "reason 텍스트 자체가 prompt 에 포함")
	assert.Contains(t, prompt, "validity=\"blacklist\"",
		"blacklist 분기 안내가 prompt 에 포함")
}

func TestExtractEnriched_EmptyReason_NoFeedbackBlock(t *testing.T) {
	runner := &mockContainerRunner{
		execStdout: `{"validity":"ok","page_type":"news","selectors":{"title":{"css":"h1"},"main_content":{"css":"article","multi":true}},"self_check":{"title_sample":"x","body_word_count_estimate":200}}`,
	}
	w := newTestWorker(t, runner)
	require.NoError(t, w.Start(t.Context()))
	t.Cleanup(func() { _ = w.Stop(context.Background()) })

	// WithRejectReason 에 빈 문자열 — None Object 패턴으로 ctx 그대로 반환
	ctx := llmgen.WithRejectReason(t.Context(), "")
	_, err := w.ExtractEnriched(ctx, "news.example.com", model.TargetTypePage, "<html><h1>title</h1><article>body</article></html>")
	require.NoError(t, err)

	prompt := extractPromptArg(runner.execedWith.args)
	assert.NotContains(t, prompt, "Validation feedback from previous attempt")
}

// TestExtractEnriched_TemplateMissingPlaceholder_FailsFast 는 LLM_PROMPT_DIR override 등으로
// 외부 템플릿이 placeholder 를 가지지 않을 때 reparse 경로 (reason 존재) 가 silent drop 되지
// 않고 fail-fast 하는지 검증합니다 (Copilot 반영 PR #368).
func TestExtractEnriched_TemplateMissingPlaceholder_FailsFast(t *testing.T) {
	// 외부 override 시뮬레이션 — placeholder 토큰 없는 prompt loader 주입.
	brokenLoader := prompt.MapLoader{
		"claudegen/page.user": "Read {{SESSION_PATH}}/page.html from {{HOST}} ({{TARGET_TYPE}}). Return JSON.",
		"claudegen/list.user": "Read {{SESSION_PATH}}/page.html from {{HOST}} ({{TARGET_TYPE}}). Return list JSON.",
	}
	log := logger.New(logger.DefaultConfig())
	authDir := makeAuthDir(t)
	w, err := claudegen.NewWithRunner(
		"ghcr.io/anthropics/claude-code:latest",
		"claude-sonnet-4-6",
		authDir,
		"/root/.claude",
		10*time.Second,
		&mockContainerRunner{},
		brokenLoader,
		log,
	)
	require.NoError(t, err)
	require.NoError(t, w.Start(t.Context()))
	t.Cleanup(func() { _ = w.Stop(context.Background()) })

	// reason 존재 + placeholder 부재 → 에러 반환
	ctx := llmgen.WithRejectReason(t.Context(), "PublishedAt required")
	_, extractErr := w.ExtractEnriched(ctx, "news.example.com", model.TargetTypePage, "<html></html>")
	require.Error(t, extractErr, "placeholder 부재 시 reparse 경로는 fail-fast")
	assert.Contains(t, extractErr.Error(), "VALIDATION_REJECT_REASON_CONTEXT",
		"에러 메시지에 누락된 placeholder 이름 포함")
}

// TestExtractEnriched_TemplateMissingPlaceholder_NoReason_OK 는 placeholder 가 없어도
// reason 부재 시에는 (정상 경로) 통과하는지 검증 — 기존 외부 템플릿 호환성.
func TestExtractEnriched_TemplateMissingPlaceholder_NoReason_OK(t *testing.T) {
	brokenLoader := prompt.MapLoader{
		"claudegen/page.user": "Read {{SESSION_PATH}}/page.html from {{HOST}} ({{TARGET_TYPE}}). Return JSON.",
		"claudegen/list.user": "Read {{SESSION_PATH}}/page.html from {{HOST}} ({{TARGET_TYPE}}). Return list JSON.",
	}
	log := logger.New(logger.DefaultConfig())
	authDir := makeAuthDir(t)
	runner := &mockContainerRunner{
		execStdout: `{"validity":"ok","page_type":"news","selectors":{"title":{"css":"h1"},"main_content":{"css":"article","multi":true}},"self_check":{"title_sample":"x","body_word_count_estimate":200}}`,
	}
	w, err := claudegen.NewWithRunner(
		"ghcr.io/anthropics/claude-code:latest",
		"claude-sonnet-4-6",
		authDir,
		"/root/.claude",
		10*time.Second,
		runner,
		brokenLoader,
		log,
	)
	require.NoError(t, err)
	require.NoError(t, w.Start(t.Context()))
	t.Cleanup(func() { _ = w.Stop(context.Background()) })

	// reason 부재 → placeholder 부재해도 정상 동작 (기존 호환성)
	_, extractErr := w.ExtractEnriched(t.Context(), "news.example.com", model.TargetTypePage, "<html><h1>x</h1><article>y</article></html>")
	assert.NoError(t, extractErr, "reason 부재 시 placeholder 부재 OK (기존 외부 템플릿 호환)")
}

// TestExtractEnriched_ListTarget_WithReason_PromptHasFeedbackBlock 은 list (category)
// target 도 동일하게 reason 블록을 받는지 검증합니다.
func TestExtractEnriched_ListTarget_WithReason_PromptHasFeedbackBlock(t *testing.T) {
	runner := &mockContainerRunner{
		execStdout: `{"validity":"ok","page_type":"news","selectors":{"item_container":{"css":"li"},"item_link":{"css":"a","attribute":"href"}},"self_check":{"item_count_estimate":10,"first_item_title_sample":"x"}}`,
	}
	w := newTestWorker(t, runner)
	require.NoError(t, w.Start(t.Context()))
	t.Cleanup(func() { _ = w.Stop(context.Background()) })

	ctx := llmgen.WithRejectReason(t.Context(), "ItemContainer matched 0 elements")
	_, err := w.ExtractEnriched(ctx, "news.example.com", model.TargetTypeList, "<html><ul><li><a href='/x'>x</a></li></ul></html>")
	require.NoError(t, err)

	prompt := extractPromptArg(runner.execedWith.args)
	require.True(t, strings.Contains(prompt, "Validation feedback from previous attempt"),
		"list target 도 reason 블록 적용")
}
