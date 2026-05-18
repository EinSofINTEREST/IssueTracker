package rule_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/processor/fetcher/core"
	"issuetracker/internal/processor/parser/rule"
	"issuetracker/internal/storage/model"
)

const articleHTML = `
<html><body>
  <h1 class="hl">Breaking News Today</h1>
  <div class="byline">Reporter Kim, Reporter Park</div>
  <span class="cat">Politics</span>
  <time datetime="2026-04-29T10:30:00+09:00">2026-04-29</time>
  <article>
    <p>First paragraph of the body.</p>
    <p>Second paragraph with more detail.</p>
  </article>
  <ul class="tags">
    <li><a>politics</a></li>
    <li><a>election</a></li>
  </ul>
  <div class="images">
    <img src="https://cdn.example.com/img1.jpg"/>
    <img src="https://cdn.example.com/img2.jpg"/>
  </div>
</body></html>
`

const listHTML = `
<html><body>
  <ul class="list">
    <li class="item">
      <a class="lnk" href="/article/1">First Item Title</a>
      <p class="sum">Snippet 1</p>
    </li>
    <li class="item">
      <a class="lnk" href="/article/2">Second Item Title</a>
      <p class="sum">Snippet 2</p>
    </li>
    <li class="item">
      <a class="lnk" href="https://news.example.com/article/3">Third (already absolute)</a>
    </li>
  </ul>
</body></html>
`

func pageRule() *model.ParserRuleRecord {
	return &model.ParserRuleRecord{
		ID:          1,
		SourceName:  "test",
		HostPattern: "news.example.com",
		TargetType:  model.TargetTypePage,
		Version:     1,
		Enabled:     true,
		Selectors: model.SelectorMap{
			Title:       &model.FieldSelector{CSS: "h1.hl"},
			Author:      &model.FieldSelector{CSS: "div.byline"},
			Category:    &model.FieldSelector{CSS: "span.cat"},
			PublishedAt: &model.FieldSelector{CSS: "time", Attribute: "datetime"},
			MainContent: &model.FieldSelector{CSS: "article p", Multi: true},
			Tags:        &model.FieldSelector{CSS: "ul.tags li a"},
			Images:      &model.FieldSelector{CSS: "div.images img", Attribute: "src"},
		},
	}
}

func listRule() *model.ParserRuleRecord {
	return &model.ParserRuleRecord{
		ID:          2,
		SourceName:  "test",
		HostPattern: "news.example.com",
		TargetType:  model.TargetTypeList,
		Version:     1,
		Enabled:     true,
		Selectors: model.SelectorMap{
			ItemContainer: &model.FieldSelector{CSS: "ul.list li.item"},
			ItemLink:      &model.FieldSelector{CSS: "a.lnk", Attribute: "href"},
			ItemTitle:     &model.FieldSelector{CSS: "a.lnk"},
			ItemSnippet:   &model.FieldSelector{CSS: "p.sum"},
		},
	}
}

func makeRaw(url, html string) *core.RawContent {
	return &core.RawContent{URL: url, HTML: html}
}

// ─────────────────────────────────────────────────────────────────────────────
// ParsePage
// ─────────────────────────────────────────────────────────────────────────────

func TestParser_ParsePage_Success(t *testing.T) {
	repo := &fakeRepo{rules: []*model.ParserRuleRecord{pageRule()}}
	res, _ := rule.NewResolver(repo)
	p, _ := rule.NewParser(res)

	page, err := p.ParsePage(context.Background(), makeRaw("https://news.example.com/article/1", articleHTML))
	require.NoError(t, err)

	assert.Equal(t, "Breaking News Today", page.Title)
	assert.Equal(t, "Reporter Kim, Reporter Park", page.Author)
	assert.Equal(t, "Politics", page.Category)
	assert.Equal(t, "First paragraph of the body.\nSecond paragraph with more detail.", page.MainContent)
	assert.Equal(t, []string{"politics", "election"}, page.Tags)
	assert.Equal(t, []string{"https://cdn.example.com/img1.jpg", "https://cdn.example.com/img2.jpg"}, page.Images)

	expected, _ := time.Parse(time.RFC3339, "2026-04-29T10:30:00+09:00")
	assert.True(t, page.PublishedAt.Equal(expected), "PublishedAt mismatch: got %v want %v", page.PublishedAt, expected)
}

func TestParser_ParsePage_NoRule_ReturnsErrNoRule(t *testing.T) {
	repo := &fakeRepo{notFound: true}
	res, _ := rule.NewResolver(repo)
	p, _ := rule.NewParser(res)

	_, err := p.ParsePage(context.Background(), makeRaw("https://nope.example.com/x", articleHTML))
	require.Error(t, err)
	assert.True(t, errors.Is(err, &rule.Error{Code: rule.ErrNoRule}))
}

func TestParser_ParsePage_MissingTitleSelector_EmptySelector(t *testing.T) {
	r := pageRule()
	r.Selectors.Title = nil
	repo := &fakeRepo{rules: []*model.ParserRuleRecord{r}}
	res, _ := rule.NewResolver(repo)
	p, _ := rule.NewParser(res)

	_, err := p.ParsePage(context.Background(), makeRaw("https://news.example.com/x", articleHTML))
	require.Error(t, err)
	var rerr *rule.Error
	require.ErrorAs(t, err, &rerr)
	assert.Equal(t, rule.ErrEmptySelector, rerr.Code)
}

func TestParser_ParsePage_MainContentMatchesNothing_ParseFailure(t *testing.T) {
	r := pageRule()
	r.Selectors.MainContent = &model.FieldSelector{CSS: "div.no-such-class", Multi: true}
	repo := &fakeRepo{rules: []*model.ParserRuleRecord{r}}
	res, _ := rule.NewResolver(repo)
	p, _ := rule.NewParser(res)

	_, err := p.ParsePage(context.Background(), makeRaw("https://news.example.com/x", articleHTML))
	require.Error(t, err)
	var rerr *rule.Error
	require.ErrorAs(t, err, &rerr)
	assert.Equal(t, rule.ErrParseFailure, rerr.Code)
}

func TestParser_ParsePage_EmptyRaw_ParseFailure(t *testing.T) {
	repo := &fakeRepo{rules: []*model.ParserRuleRecord{pageRule()}}
	res, _ := rule.NewResolver(repo)
	p, _ := rule.NewParser(res)

	_, err := p.ParsePage(context.Background(), &core.RawContent{URL: "https://news.example.com/x", HTML: ""})
	require.Error(t, err)
	var rerr *rule.Error
	require.ErrorAs(t, err, &rerr)
	assert.Equal(t, rule.ErrParseFailure, rerr.Code)
}

// ─────────────────────────────────────────────────────────────────────────────
// ParseLinks
// ─────────────────────────────────────────────────────────────────────────────

func TestParser_ParseLinks_Success_AbsolutizesURLs(t *testing.T) {
	repo := &fakeRepo{rules: []*model.ParserRuleRecord{listRule()}}
	res, _ := rule.NewResolver(repo)
	p, _ := rule.NewParser(res)

	items, err := p.ParseLinks(context.Background(), makeRaw("https://news.example.com/category/politics", listHTML))
	require.NoError(t, err)
	require.Len(t, items, 3)

	assert.Equal(t, "https://news.example.com/article/1", items[0].URL, "상대 경로는 base 로 절대 URL 화")
	assert.Equal(t, "First Item Title", items[0].Title)
	assert.Equal(t, "Snippet 1", items[0].Snippet)

	assert.Equal(t, "https://news.example.com/article/2", items[1].URL)
	assert.Equal(t, "https://news.example.com/article/3", items[2].URL, "이미 절대 URL 은 그대로")
}

func TestParser_ParseLinks_MissingItemContainer_EmptySelector(t *testing.T) {
	r := listRule()
	r.Selectors.ItemContainer = nil
	repo := &fakeRepo{rules: []*model.ParserRuleRecord{r}}
	res, _ := rule.NewResolver(repo)
	p, _ := rule.NewParser(res)

	_, err := p.ParseLinks(context.Background(), makeRaw("https://news.example.com/x", listHTML))
	require.Error(t, err)
	var rerr *rule.Error
	require.ErrorAs(t, err, &rerr)
	assert.Equal(t, rule.ErrEmptySelector, rerr.Code)
}

func TestParser_ParseLinks_MissingItemLink_EmptySelector(t *testing.T) {
	r := listRule()
	r.Selectors.ItemLink = nil
	repo := &fakeRepo{rules: []*model.ParserRuleRecord{r}}
	res, _ := rule.NewResolver(repo)
	p, _ := rule.NewParser(res)

	_, err := p.ParseLinks(context.Background(), makeRaw("https://news.example.com/x", listHTML))
	require.Error(t, err)
	var rerr *rule.Error
	require.ErrorAs(t, err, &rerr)
	assert.Equal(t, rule.ErrEmptySelector, rerr.Code)
}

func TestParser_ParseLinks_NoRule_ReturnsErrNoRule(t *testing.T) {
	repo := &fakeRepo{notFound: true}
	res, _ := rule.NewResolver(repo)
	p, _ := rule.NewParser(res)

	_, err := p.ParseLinks(context.Background(), makeRaw("https://nope.example.com/list", listHTML))
	require.Error(t, err)
	assert.True(t, errors.Is(err, &rule.Error{Code: rule.ErrNoRule}))
}

func TestNewParser_NilResolver_ReturnsError(t *testing.T) {
	p, err := rule.NewParser(nil)
	assert.Nil(t, p)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-nil resolver")
}

// stubRuleLookup 은 RuleLookup 인터페이스의 mock 구현입니다 (이슈 #463).
//
// concrete *Resolver 대신 mock 주입으로 Parser 단위 테스트 가능 — interface 추상화 후
// 가능해진 새 패턴.
type stubRuleLookup struct {
	rule *model.ParserRuleRecord
	err  error
}

func (s *stubRuleLookup) ResolveByURL(_ context.Context, _ string, _ model.TargetType) (*model.ParserRuleRecord, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.rule, nil
}

// TestParser_RuleLookupMock_DirectInjection 은 RuleLookup 인터페이스 추상화로 mock 주입이
// 가능함을 검증합니다 (이슈 #463 — interface 의존 전환의 핵심 가치).
//
// 기존 테스트는 *Resolver concrete + fake repo 패턴 사용 — Resolver 의 cache / Redis /
// 비교 로직까지 거쳐야 했음. 본 테스트는 RuleLookup mock 으로 그 의존 일체 제거.
func TestParser_RuleLookupMock_DirectInjection(t *testing.T) {
	mockLookup := &stubRuleLookup{rule: pageRule()}
	p, err := rule.NewParser(mockLookup)
	require.NoError(t, err)

	page, err := p.ParsePage(context.Background(), makeRaw("https://example.com/article/1", articleHTML))
	require.NoError(t, err)
	assert.Equal(t, "Breaking News Today", page.Title)
}

// TestParser_RuleLookupMock_LookupError 는 mock 의 error 가 그대로 호출자에 propagate
// 되는지 검증.
func TestParser_RuleLookupMock_LookupError(t *testing.T) {
	expectedErr := errors.New("synthetic lookup failure")
	mockLookup := &stubRuleLookup{err: expectedErr}
	p, err := rule.NewParser(mockLookup)
	require.NoError(t, err)

	_, err = p.ParsePage(context.Background(), makeRaw("https://example.com/article/1", articleHTML))
	require.Error(t, err)
	assert.ErrorIs(t, err, expectedErr)
}

// TestParser_RuleLookupMock_NilRuleSuccess_NoPanic 은 RuleLookup 이 (nil, nil) 반환 시
// dereferencing panic 대신 ErrNoRule 로 안전하게 처리되는지 검증 (coderabbit-review PR #464).
func TestParser_RuleLookupMock_NilRuleSuccess_NoPanic(t *testing.T) {
	mockLookup := &stubRuleLookup{rule: nil, err: nil}
	p, err := rule.NewParser(mockLookup)
	require.NoError(t, err)

	// ParsePage
	_, err = p.ParsePage(context.Background(), makeRaw("https://example.com/a", articleHTML))
	require.Error(t, err)
	var rerr *rule.Error
	require.ErrorAs(t, err, &rerr)
	assert.Equal(t, rule.ErrNoRule, rerr.Code)

	// ParseLinks
	_, err = p.ParseLinks(context.Background(), makeRaw("https://example.com/list", listHTML))
	require.Error(t, err)
	require.ErrorAs(t, err, &rerr)
	assert.Equal(t, rule.ErrNoRule, rerr.Code)
}

// ─────────────────────────────────────────────────────────────────────────────
// rule.Error 의 Host 필드 — 이슈 #508
//
// parser.go 의 모든 Error 발산이 raw.URL 에서 canonical host (lowercase hostname, port 제거) 를
// 채워야 worker 의 staleCounter / failureCounter 가 올바른 host 키로 누적됨.
// ─────────────────────────────────────────────────────────────────────────────

func TestParser_ParsePage_ErrorHost_EmptySelector(t *testing.T) {
	r := pageRule()
	r.Selectors.Title = nil
	repo := &fakeRepo{rules: []*model.ParserRuleRecord{r}}
	res, _ := rule.NewResolver(repo)
	p, _ := rule.NewParser(res)

	_, err := p.ParsePage(context.Background(), makeRaw("https://news.example.com/x", articleHTML))
	var rerr *rule.Error
	require.ErrorAs(t, err, &rerr)
	assert.Equal(t, rule.ErrEmptySelector, rerr.Code)
	assert.Equal(t, "news.example.com", rerr.Host, "Host 필드는 raw.URL 의 canonical host 와 일치해야 함")
}

func TestParser_ParsePage_ErrorHost_ParseFailure(t *testing.T) {
	r := pageRule()
	r.Selectors.MainContent = &model.FieldSelector{CSS: "div.no-such-class", Multi: true}
	repo := &fakeRepo{rules: []*model.ParserRuleRecord{r}}
	res, _ := rule.NewResolver(repo)
	p, _ := rule.NewParser(res)

	_, err := p.ParsePage(context.Background(), makeRaw("https://news.example.com/x", articleHTML))
	var rerr *rule.Error
	require.ErrorAs(t, err, &rerr)
	assert.Equal(t, rule.ErrParseFailure, rerr.Code)
	assert.Equal(t, "news.example.com", rerr.Host)
}

func TestParser_ParsePage_ErrorHost_EmptyRaw_ParseFailure(t *testing.T) {
	repo := &fakeRepo{rules: []*model.ParserRuleRecord{pageRule()}}
	res, _ := rule.NewResolver(repo)
	p, _ := rule.NewParser(res)

	_, err := p.ParsePage(context.Background(), &core.RawContent{URL: "https://news.example.com/x", HTML: ""})
	var rerr *rule.Error
	require.ErrorAs(t, err, &rerr)
	assert.Equal(t, rule.ErrParseFailure, rerr.Code)
	assert.Equal(t, "news.example.com", rerr.Host, "validateRaw 가 발산한 Error 도 Host 필드를 채워야 함")
}

func TestParser_ParsePage_ErrorHost_LowercasesHost(t *testing.T) {
	r := pageRule()
	r.Selectors.Title = nil
	repo := &fakeRepo{rules: []*model.ParserRuleRecord{r}}
	res, _ := rule.NewResolver(repo)
	p, _ := rule.NewParser(res)

	// 대문자 host — errorHost 가 lowercase 로 정규화해야 staleCounter 키가 resolver 와 일치.
	_, err := p.ParsePage(context.Background(), makeRaw("https://NEWS.EXAMPLE.COM/x", articleHTML))
	var rerr *rule.Error
	require.ErrorAs(t, err, &rerr)
	assert.Equal(t, "news.example.com", rerr.Host, "Host 는 소문자로 정규화되어야 함")
}

func TestParser_ParsePage_ErrorHost_StripsPort(t *testing.T) {
	r := pageRule()
	r.Selectors.Title = nil
	r.HostPattern = "news.example.com"
	repo := &fakeRepo{rules: []*model.ParserRuleRecord{r}}
	res, _ := rule.NewResolver(repo)
	p, _ := rule.NewParser(res)

	_, err := p.ParsePage(context.Background(), makeRaw("https://news.example.com:8080/x", articleHTML))
	var rerr *rule.Error
	require.ErrorAs(t, err, &rerr)
	assert.Equal(t, "news.example.com", rerr.Host, "Host 는 port 제거된 hostname")
}

func TestParser_ParseLinks_ErrorHost_EmptySelector(t *testing.T) {
	r := listRule()
	r.Selectors.ItemContainer = nil
	repo := &fakeRepo{rules: []*model.ParserRuleRecord{r}}
	res, _ := rule.NewResolver(repo)
	p, _ := rule.NewParser(res)

	_, err := p.ParseLinks(context.Background(), makeRaw("https://news.example.com/category/politics", listHTML))
	var rerr *rule.Error
	require.ErrorAs(t, err, &rerr)
	assert.Equal(t, rule.ErrEmptySelector, rerr.Code)
	assert.Equal(t, "news.example.com", rerr.Host)
}

func TestParser_ParseLinks_ErrorHost_ParseFailure(t *testing.T) {
	r := listRule()
	r.Selectors.ItemContainer = &model.FieldSelector{CSS: "ul.no-such-class li"}
	repo := &fakeRepo{rules: []*model.ParserRuleRecord{r}}
	res, _ := rule.NewResolver(repo)
	p, _ := rule.NewParser(res)

	_, err := p.ParseLinks(context.Background(), makeRaw("https://news.example.com/category/politics", listHTML))
	var rerr *rule.Error
	require.ErrorAs(t, err, &rerr)
	assert.Equal(t, rule.ErrParseFailure, rerr.Code)
	assert.Equal(t, "news.example.com", rerr.Host)
}

func TestParser_NilRuleSuccess_ErrorHost(t *testing.T) {
	mockLookup := &stubRuleLookup{rule: nil, err: nil}
	p, _ := rule.NewParser(mockLookup)

	// ParsePage nil-rule
	_, err := p.ParsePage(context.Background(), makeRaw("https://example.com/a", articleHTML))
	var rerr *rule.Error
	require.ErrorAs(t, err, &rerr)
	assert.Equal(t, "example.com", rerr.Host, "nil-rule ErrNoRule 도 Host 채워야 함")

	// ParseLinks nil-rule
	_, err = p.ParseLinks(context.Background(), makeRaw("https://example.com/list", listHTML))
	require.ErrorAs(t, err, &rerr)
	assert.Equal(t, "example.com", rerr.Host)
}
