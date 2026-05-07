package validator_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/processor/parser/rule/validator"
	"issuetracker/internal/storage"
	"issuetracker/pkg/llm"
	"issuetracker/pkg/llm/prompt"
	"issuetracker/pkg/logger"
)

// validatorLoader 는 LLMValidator 가 요구하는 prompt asset 을 in-memory 로 제공합니다.
// 운영의 scripts/prompts/validator/{system,page.user,list.user}.txt 와 동일한 placeholder 사용.
var validatorLoader = prompt.MapLoader{
	"validator/system":    "You are a CSS selector validator. Respond with JSON.",
	"validator/page.user": "Article page\ntitle: {{TITLE}}\nbody: {{BODY}}\n{{PUBLISHED_AT_LINE}}criteria\n{{PUBLISHED_AT_CRITERIA}}",
	"validator/list.user": "List page\ncontainer: {{ITEM_CONTAINER}}\n{{ITEM_LINKS_LINE}}criteria",
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
	response string
	err      error
	calls    int
}

func (f *fakeProvider) Name() string { return "fake" }
func (f *fakeProvider) Generate(_ context.Context, _ llm.Request) (*llm.Response, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return &llm.Response{Content: f.response, Model: "fake-model"}, nil
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

var pageSelectors = storage.SelectorMap{
	Title:       &storage.FieldSelector{CSS: "h1.article-title"},
	MainContent: &storage.FieldSelector{CSS: "article p"},
	PublishedAt: &storage.FieldSelector{CSS: "span.date"},
}

var listSelectors = storage.SelectorMap{
	ItemContainer: &storage.FieldSelector{CSS: "li.news-item"},
	ItemLink:      &storage.FieldSelector{CSS: "a", Attribute: "href"},
}

// ─────────────────────────────────────────────────────────────────────────────
// LLMValidator 테스트
// ─────────────────────────────────────────────────────────────────────────────

func TestLLMValidator_PageValid(t *testing.T) {
	provider := &fakeProvider{response: `{"valid": true, "reason": "title and body look like a news article"}`}
	v := newTestLLMValidator(t, provider)

	res, err := v.Validate(context.Background(), samplePageHTML, pageSelectors, storage.TargetTypePage)
	require.NoError(t, err)
	assert.True(t, res.Valid)
	assert.NotEmpty(t, res.Reason)
	assert.Equal(t, 1, provider.calls, "LLM 1회 호출")
}

func TestLLMValidator_PageInvalid(t *testing.T) {
	provider := &fakeProvider{response: `{"valid": false, "reason": "title selector extracts navigation menu, not a headline"}`}
	v := newTestLLMValidator(t, provider)

	res, err := v.Validate(context.Background(), samplePageHTML, pageSelectors, storage.TargetTypePage)
	require.NoError(t, err)
	assert.False(t, res.Valid)
	assert.Contains(t, res.Reason, "navigation")
}

func TestLLMValidator_ListValid(t *testing.T) {
	provider := &fakeProvider{response: `{"valid": true, "reason": "item_links are article URLs"}`}
	v := newTestLLMValidator(t, provider)

	res, err := v.Validate(context.Background(), sampleListHTML, listSelectors, storage.TargetTypeList)
	require.NoError(t, err)
	assert.True(t, res.Valid)
}

func TestLLMValidator_APIError_ReturnsError(t *testing.T) {
	provider := &fakeProvider{err: errors.New("rate limit")}
	v := newTestLLMValidator(t, provider)

	_, err := v.Validate(context.Background(), samplePageHTML, pageSelectors, storage.TargetTypePage)
	assert.Error(t, err)
}

func TestLLMValidator_MalformedResponse_ReturnsError(t *testing.T) {
	provider := &fakeProvider{response: "not json at all"}
	v := newTestLLMValidator(t, provider)

	_, err := v.Validate(context.Background(), samplePageHTML, pageSelectors, storage.TargetTypePage)
	assert.Error(t, err)
}

func TestLLMValidator_ResponseWrappedInMarkdown(t *testing.T) {
	// LLM 이 ```json ... ``` 으로 감싼 응답도 파싱 가능해야 함
	provider := &fakeProvider{response: "```json\n{\"valid\": true, \"reason\": \"ok\"}\n```"}
	v := newTestLLMValidator(t, provider)

	res, err := v.Validate(context.Background(), samplePageHTML, pageSelectors, storage.TargetTypePage)
	require.NoError(t, err)
	assert.True(t, res.Valid)
}

// ─────────────────────────────────────────────────────────────────────────────
// Pool 테스트
// ─────────────────────────────────────────────────────────────────────────────

func TestPool_Empty_AlwaysValid(t *testing.T) {
	log := logger.New(logger.DefaultConfig())
	pool := validator.NewPool(log)

	res, err := pool.Validate(context.Background(), samplePageHTML, pageSelectors, storage.TargetTypePage)
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

	res, err := pool.Validate(context.Background(), samplePageHTML, pageSelectors, storage.TargetTypePage)
	require.NoError(t, err)
	assert.True(t, res.Valid, "첫 번째 validator 결과 사용")
	assert.Equal(t, 1, p1.calls, "첫 validator 1회 호출")
	assert.Equal(t, 0, p2.calls, "두 번째 validator 는 호출 안 됨 (short-circuit)")
}

func TestPool_FirstValidatorRejects_ReturnsInvalid(t *testing.T) {
	log := logger.New(logger.DefaultConfig())
	p1 := &fakeProvider{response: `{"valid": false, "reason": "selector extracts ads"}`}
	pool := validator.NewPool(log, newTestLLMValidator(t, p1))

	res, err := pool.Validate(context.Background(), samplePageHTML, pageSelectors, storage.TargetTypePage)
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

	res, err := pool.Validate(context.Background(), samplePageHTML, pageSelectors, storage.TargetTypePage)
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

	res, err := pool.Validate(context.Background(), samplePageHTML, pageSelectors, storage.TargetTypePage)
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

	res, err := adapter.Validate(context.Background(), samplePageHTML, pageSelectors, storage.TargetTypePage)
	require.NoError(t, err)
	assert.True(t, res.Valid)
	assert.Equal(t, "looks good", res.Reason)
}
