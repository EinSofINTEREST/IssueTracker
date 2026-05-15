package validator_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/processor/parser/rule/validator"
	"issuetracker/internal/storage/model"
	"issuetracker/pkg/llm"
	"issuetracker/pkg/llm/prompt"
	"issuetracker/pkg/logger"
)

// validatorLoader 는 LLMValidator 가 요구하는 prompt asset 을 in-memory 로 제공합니다.
// 운영의 pkg/llm/prompt/assets/validator/{system,page.user,list.user}.txt 와 동일한 placeholder 사용.
var validatorLoader = prompt.MapLoader{
	"parser/validator/system":    "You are a CSS selector validator. Respond with JSON.",
	"parser/validator/page.user": "Article page\ntitle: {{TITLE}}\nbody: {{BODY}}\n{{PUBLISHED_AT_LINE}}criteria\n{{PUBLISHED_AT_CRITERIA}}",
	"parser/validator/list.user": "List page\ncontainer: {{ITEM_CONTAINER}}\n{{ITEM_LINKS_LINE}}criteria",
}

func newTestLLMValidator(t *testing.T, provider llm.Provider) *validator.LLMValidator {
	t.Helper()
	v, err := validator.NewLLMValidator(provider, validatorLoader)
	require.NoError(t, err)
	return v
}

// ─────────────────────────────────────────────────────────────────────────────
// 테스트용 fakes
// ─────────────────────────────────────────────────────────────────────────────

type fakeProvider struct {
	response      string
	stopReason    string // 빈 문자열이면 응답에서 미설정
	err           error
	calls         int
	lastMaxTokens int // Validator 가 전달한 MaxTokens 검증용
}

func (f *fakeProvider) Name() string { return "fake" }
func (f *fakeProvider) Generate(_ context.Context, req llm.Request) (*llm.Response, error) {
	f.calls++
	f.lastMaxTokens = req.MaxTokens
	if f.err != nil {
		return nil, f.err
	}
	return &llm.Response{Content: f.response, StopReason: f.stopReason, Model: "fake-model"}, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// 샘플 HTML
// ─────────────────────────────────────────────────────────────────────────────

const samplePageHTML = `<!DOCTYPE html><html><body>
<h1 class="article-title">뉴스 헤드라인 — 기후 변화 대응 국제 협약 체결</h1>
<article><p>세계 각국 정상들이 모여 기후 변화 대응을 위한 국제 협약을 체결했다.
이번 협약은 탄소 배출을 2030년까지 50% 감축하는 것을 목표로 하며,
선진국들이 개발도상국 지원 기금을 조성하기로 합의했다.</p></article>
<span class="date">2026-05-05</span>
</body></html>`

const sampleListHTML = `<!DOCTYPE html><html><body>
<ul class="news-list">
  <li class="news-item"><a href="/news/1">기후 협약 체결</a></li>
  <li class="news-item"><a href="/news/2">경제 지표 발표</a></li>
  <li class="news-item"><a href="/news/3">스포츠 결과</a></li>
</ul>
</body></html>`

var pageSelectors = model.SelectorMap{
	Title:       &model.FieldSelector{CSS: "h1.article-title"},
	MainContent: &model.FieldSelector{CSS: "article p"},
	PublishedAt: &model.FieldSelector{CSS: "span.date"},
}

var listSelectors = model.SelectorMap{
	ItemContainer: &model.FieldSelector{CSS: "li.news-item"},
	ItemLink:      &model.FieldSelector{CSS: "a", Attribute: "href"},
}

// ─────────────────────────────────────────────────────────────────────────────
// LLMValidator 테스트
// ─────────────────────────────────────────────────────────────────────────────

func TestLLMValidator_PageValid(t *testing.T) {
	provider := &fakeProvider{response: `{"valid": true, "reason": "title and body look like a news article"}`}
	v := newTestLLMValidator(t, provider)

	res, err := v.Validate(context.Background(), samplePageHTML, pageSelectors, model.TargetTypePage)
	require.NoError(t, err)
	assert.True(t, res.Valid)
	assert.NotEmpty(t, res.Reason)
	assert.Equal(t, 1, provider.calls, "LLM 1회 호출")
}

func TestLLMValidator_PageInvalid(t *testing.T) {
	provider := &fakeProvider{response: `{"valid": false, "reason": "title selector extracts navigation menu, not a headline"}`}
	v := newTestLLMValidator(t, provider)

	res, err := v.Validate(context.Background(), samplePageHTML, pageSelectors, model.TargetTypePage)
	require.NoError(t, err)
	assert.False(t, res.Valid)
	assert.Contains(t, res.Reason, "navigation")
}

func TestLLMValidator_ListValid(t *testing.T) {
	provider := &fakeProvider{response: `{"valid": true, "reason": "item_links are article URLs"}`}
	v := newTestLLMValidator(t, provider)

	res, err := v.Validate(context.Background(), sampleListHTML, listSelectors, model.TargetTypeList)
	require.NoError(t, err)
	assert.True(t, res.Valid)
}

func TestLLMValidator_APIError_ReturnsError(t *testing.T) {
	provider := &fakeProvider{err: errors.New("rate limit")}
	v := newTestLLMValidator(t, provider)

	_, err := v.Validate(context.Background(), samplePageHTML, pageSelectors, model.TargetTypePage)
	assert.Error(t, err)
}

func TestLLMValidator_MalformedResponse_ReturnsError(t *testing.T) {
	provider := &fakeProvider{response: "not json at all"}
	v := newTestLLMValidator(t, provider)

	_, err := v.Validate(context.Background(), samplePageHTML, pageSelectors, model.TargetTypePage)
	assert.Error(t, err)
}

func TestLLMValidator_ResponseWrappedInMarkdown(t *testing.T) {
	// LLM 이 ```json ... ``` 으로 감싼 응답도 파싱 가능해야 함
	provider := &fakeProvider{response: "```json\n{\"valid\": true, \"reason\": \"ok\"}\n```"}
	v := newTestLLMValidator(t, provider)

	res, err := v.Validate(context.Background(), samplePageHTML, pageSelectors, model.TargetTypePage)
	require.NoError(t, err)
	assert.True(t, res.Valid)
}

// TestLLMValidator_MaxTokensIncreasedTo512 — 이슈 #320 의 #1 수정 검증.
// Validator 가 Generate 호출 시 MaxTokens=512 를 전달하는지 fakeProvider 가 기록한 값으로 확인.
func TestLLMValidator_MaxTokensIncreasedTo512(t *testing.T) {
	provider := &fakeProvider{response: `{"valid": true, "reason": "ok"}`}
	v := newTestLLMValidator(t, provider)

	_, err := v.Validate(context.Background(), samplePageHTML, pageSelectors, model.TargetTypePage)
	require.NoError(t, err)
	assert.Equal(t, 512, provider.lastMaxTokens, "MaxTokens 가 512 로 상향됨")
}

// TestLLMValidator_TruncatedResponseSalvagesValidVerdict — 이슈 #320 의 #3 (regex fallback).
// 응답이 reason 중간에 truncate 되어도 "valid" verdict 만큼은 salvage 되어 결과 반환.
func TestLLMValidator_TruncatedResponseSalvagesValidVerdict(t *testing.T) {
	provider := &fakeProvider{
		response: `{"valid": false, "reason": "The extracted`, // mid-reason 잘림
	}
	v := newTestLLMValidator(t, provider)

	res, err := v.Validate(context.Background(), samplePageHTML, pageSelectors, model.TargetTypePage)
	require.NoError(t, err, "regex salvage 로 verdict 추출 성공")
	assert.False(t, res.Valid, "valid: false 추출 확인")
	assert.Empty(t, res.Reason, "reason 은 truncate 되어 빈 문자열")
}

// TestLLMValidator_TruncatedResponseTrueVerdict — true verdict 도 동일하게 salvage.
func TestLLMValidator_TruncatedResponseTrueVerdict(t *testing.T) {
	provider := &fakeProvider{
		response: "```json\n{\n  \"valid\": true, \"reason\": \"looks like a valid",
	}
	v := newTestLLMValidator(t, provider)

	res, err := v.Validate(context.Background(), samplePageHTML, pageSelectors, model.TargetTypePage)
	require.NoError(t, err)
	assert.True(t, res.Valid)
}

// TestLLMValidator_TruncatedAtValidKey_ReturnsTruncateError — 이슈 #320 의 #2 (truncate 명시 에러).
// 응답이 "valid" 키 도달 전에 truncate 되어 salvage 도 실패 + StopReason 이 max-tokens 신호면
// 명시 truncate 에러로 반환.
func TestLLMValidator_TruncatedAtValidKey_ReturnsTruncateError(t *testing.T) {
	provider := &fakeProvider{
		response:   "```json\n{",
		stopReason: "MAX_TOKENS",
	}
	v := newTestLLMValidator(t, provider)

	_, err := v.Validate(context.Background(), samplePageHTML, pageSelectors, model.TargetTypePage)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "truncated", "truncate 명시 에러 메시지")
	assert.Contains(t, err.Error(), "stop_reason=MAX_TOKENS")
	assert.Contains(t, err.Error(), "max_tokens=512")
}

// TestLLMValidator_TruncateDetection_OpenAI — provider 별 StopReason 모두 인식.
func TestLLMValidator_TruncateDetection_StopReasons(t *testing.T) {
	cases := []string{"length", "max_tokens", "MAX_TOKENS"}
	for _, sr := range cases {
		sr := sr
		t.Run(sr, func(t *testing.T) {
			provider := &fakeProvider{response: "incomplete", stopReason: sr}
			v := newTestLLMValidator(t, provider)
			_, err := v.Validate(context.Background(), samplePageHTML, pageSelectors, model.TargetTypePage)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "truncated")
		})
	}
}

// TestLLMValidator_NonTruncateError_NoTruncateMessage — StopReason 이 일반 종료 (stop / end_turn)
// 인데 unmarshal 실패하면 기존 unmarshal 에러 메시지 그대로 — truncate 메시지 추가 X (오분류 방지).
func TestLLMValidator_NonTruncateError_NoTruncateMessage(t *testing.T) {
	provider := &fakeProvider{
		response:   "not valid json",
		stopReason: "stop",
	}
	v := newTestLLMValidator(t, provider)

	_, err := v.Validate(context.Background(), samplePageHTML, pageSelectors, model.TargetTypePage)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "truncated", "stop 은 정상 종료 — truncate 분류 안 함")
	assert.Contains(t, err.Error(), "parse validation response")
}

// ─────────────────────────────────────────────────────────────────────────────
// Pool 테스트
// ─────────────────────────────────────────────────────────────────────────────

func TestPool_Empty_AlwaysValid(t *testing.T) {
	log := logger.New(logger.DefaultConfig())
	pool := validator.NewPool(log)

	res, err := pool.Validate(context.Background(), samplePageHTML, pageSelectors, model.TargetTypePage)
	require.NoError(t, err)
	assert.True(t, res.Valid)
}

func TestPool_FirstValidatorPasses_ReturnsImmediately(t *testing.T) {
	log := logger.New(logger.DefaultConfig())
	p1 := &fakeProvider{response: `{"valid": true, "reason": "ok"}`}
	p2 := &fakeProvider{response: `{"valid": false, "reason": "bad"}`}
	pool := validator.NewPool(log,
		newTestLLMValidator(t, p1),
		newTestLLMValidator(t, p2),
	)

	res, err := pool.Validate(context.Background(), samplePageHTML, pageSelectors, model.TargetTypePage)
	require.NoError(t, err)
	assert.True(t, res.Valid, "첫 번째 validator 결과 사용")
	assert.Equal(t, 1, p1.calls, "첫 validator 1회 호출")
	assert.Equal(t, 0, p2.calls, "두 번째 validator 는 호출 안 됨 (short-circuit)")
}

func TestPool_FirstValidatorRejects_ReturnsInvalid(t *testing.T) {
	log := logger.New(logger.DefaultConfig())
	p1 := &fakeProvider{response: `{"valid": false, "reason": "selector extracts ads"}`}
	pool := validator.NewPool(log, newTestLLMValidator(t, p1))

	res, err := pool.Validate(context.Background(), samplePageHTML, pageSelectors, model.TargetTypePage)
	require.NoError(t, err)
	assert.False(t, res.Valid)
	assert.Contains(t, res.Reason, "ads")
}

func TestPool_AllAPIErrors_BestEffortPass(t *testing.T) {
	// 모든 validator 가 API 오류 → best-effort 통과 (rule INSERT 차단 안 함)
	log := logger.New(logger.DefaultConfig())
	p1 := &fakeProvider{err: errors.New("timeout")}
	p2 := &fakeProvider{err: errors.New("rate limit")}
	pool := validator.NewPool(log,
		newTestLLMValidator(t, p1),
		newTestLLMValidator(t, p2),
	)

	res, err := pool.Validate(context.Background(), samplePageHTML, pageSelectors, model.TargetTypePage)
	assert.Error(t, err, "API 오류는 error 로 반환")
	assert.True(t, res.Valid, "best-effort: 모든 validator API 오류 시 통과")
}

func TestPool_FirstAPIError_SecondPasses(t *testing.T) {
	// 첫 번째 API 오류 → 두 번째 validator 로 fallback
	log := logger.New(logger.DefaultConfig())
	p1 := &fakeProvider{err: errors.New("timeout")}
	p2 := &fakeProvider{response: `{"valid": true, "reason": "ok"}`}
	pool := validator.NewPool(log,
		newTestLLMValidator(t, p1),
		newTestLLMValidator(t, p2),
	)

	res, err := pool.Validate(context.Background(), samplePageHTML, pageSelectors, model.TargetTypePage)
	require.NoError(t, err)
	assert.True(t, res.Valid)
	assert.Equal(t, 1, p2.calls, "두 번째 validator 사용")
}

// ─────────────────────────────────────────────────────────────────────────────
// LLMGenAdapter 테스트
// ─────────────────────────────────────────────────────────────────────────────

func TestLLMGenAdapter_ConvertsResult(t *testing.T) {
	log := logger.New(logger.DefaultConfig())
	p := &fakeProvider{response: `{"valid": true, "reason": "looks good"}`}
	pool := validator.NewPool(log, newTestLLMValidator(t, p))
	adapter := validator.NewLLMGenAdapter(pool)

	res, err := adapter.Validate(context.Background(), samplePageHTML, pageSelectors, model.TargetTypePage)
	require.NoError(t, err)
	assert.True(t, res.Valid)
	assert.Equal(t, "looks good", res.Reason)
}
