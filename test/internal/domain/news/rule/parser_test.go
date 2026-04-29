package rule_test

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/crawler/core"
	"issuetracker/internal/crawler/domain/news/rule"
	"issuetracker/internal/storage"
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
      <p class="sum">Summary 1</p>
    </li>
    <li class="item">
      <a class="lnk" href="/article/2">Second Item Title</a>
      <p class="sum">Summary 2</p>
    </li>
    <li class="item">
      <a class="lnk" href="https://news.example.com/article/3">Third (already absolute)</a>
    </li>
  </ul>
</body></html>
`

func articleRule() *storage.ParsingRuleRecord {
	return &storage.ParsingRuleRecord{
		ID:          1,
		SourceName:  "test",
		HostPattern: "news.example.com",
		TargetType:  storage.TargetTypeArticle,
		Version:     1,
		Enabled:     true,
		Selectors: storage.SelectorMap{
			Title:     &storage.FieldSelector{CSS: "h1.hl"},
			Author:    &storage.FieldSelector{CSS: "div.byline"},
			Category:  &storage.FieldSelector{CSS: "span.cat"},
			Date:      &storage.FieldSelector{CSS: "time", Attribute: "datetime"},
			Body:      &storage.FieldSelector{CSS: "article p", Multi: true},
			Tags:      &storage.FieldSelector{CSS: "ul.tags li a"},
			ImageURLs: &storage.FieldSelector{CSS: "div.images img", Attribute: "src"},
		},
	}
}

func listRule() *storage.ParsingRuleRecord {
	return &storage.ParsingRuleRecord{
		ID:          2,
		SourceName:  "test",
		HostPattern: "news.example.com",
		TargetType:  storage.TargetTypeList,
		Version:     1,
		Enabled:     true,
		Selectors: storage.SelectorMap{
			ItemContainer: &storage.FieldSelector{CSS: "ul.list li.item"},
			ItemLink:      &storage.FieldSelector{CSS: "a.lnk", Attribute: "href"},
			ItemTitle:     &storage.FieldSelector{CSS: "a.lnk"},
			ItemSummary:   &storage.FieldSelector{CSS: "p.sum"},
		},
	}
}

func makeRaw(url, html string) *core.RawContent {
	return &core.RawContent{URL: url, HTML: html}
}

// ─────────────────────────────────────────────────────────────────────────────
// ParseArticle
// ─────────────────────────────────────────────────────────────────────────────

func TestParser_ParseArticle_Success(t *testing.T) {
	repo := &fakeRepo{rules: []*storage.ParsingRuleRecord{articleRule()}}
	p := rule.NewParser(rule.NewResolver(repo))

	article, err := p.ParseArticle(makeRaw("https://news.example.com/article/1", articleHTML))
	require.NoError(t, err)

	assert.Equal(t, "Breaking News Today", article.Title)
	assert.Equal(t, "Reporter Kim, Reporter Park", article.Author)
	assert.Equal(t, "Politics", article.Category)
	assert.Equal(t, "First paragraph of the body.\nSecond paragraph with more detail.", article.Body)
	assert.Equal(t, []string{"politics", "election"}, article.Tags)
	assert.Equal(t, []string{"https://cdn.example.com/img1.jpg", "https://cdn.example.com/img2.jpg"}, article.ImageURLs)

	// Date — datetime attribute "2026-04-29T10:30:00+09:00"
	expected, _ := time.Parse(time.RFC3339, "2026-04-29T10:30:00+09:00")
	assert.True(t, article.PublishedAt.Equal(expected), "PublishedAt mismatch: got %v want %v", article.PublishedAt, expected)
}

func TestParser_ParseArticle_NoRule_ReturnsErrNoRule(t *testing.T) {
	repo := &fakeRepo{notFound: true}
	p := rule.NewParser(rule.NewResolver(repo))

	_, err := p.ParseArticle(makeRaw("https://nope.example.com/x", articleHTML))
	require.Error(t, err)
	assert.True(t, errors.Is(err, &rule.Error{Code: rule.ErrNoRule}))
}

func TestParser_ParseArticle_MissingTitleSelector_EmptySelector(t *testing.T) {
	r := articleRule()
	r.Selectors.Title = nil // 필수 필드 누락
	repo := &fakeRepo{rules: []*storage.ParsingRuleRecord{r}}
	p := rule.NewParser(rule.NewResolver(repo))

	_, err := p.ParseArticle(makeRaw("https://news.example.com/x", articleHTML))
	require.Error(t, err)
	var rerr *rule.Error
	require.ErrorAs(t, err, &rerr)
	assert.Equal(t, rule.ErrEmptySelector, rerr.Code)
}

func TestParser_ParseArticle_BodySelectorMatchesNothing_ParseFailure(t *testing.T) {
	r := articleRule()
	r.Selectors.Body = &storage.FieldSelector{CSS: "div.no-such-class", Multi: true}
	repo := &fakeRepo{rules: []*storage.ParsingRuleRecord{r}}
	p := rule.NewParser(rule.NewResolver(repo))

	_, err := p.ParseArticle(makeRaw("https://news.example.com/x", articleHTML))
	require.Error(t, err)
	var rerr *rule.Error
	require.ErrorAs(t, err, &rerr)
	assert.Equal(t, rule.ErrParseFailure, rerr.Code)
}

func TestParser_ParseArticle_EmptyRaw_ParseFailure(t *testing.T) {
	repo := &fakeRepo{rules: []*storage.ParsingRuleRecord{articleRule()}}
	p := rule.NewParser(rule.NewResolver(repo))

	_, err := p.ParseArticle(&core.RawContent{URL: "https://news.example.com/x", HTML: ""})
	require.Error(t, err)
	var rerr *rule.Error
	require.ErrorAs(t, err, &rerr)
	assert.Equal(t, rule.ErrParseFailure, rerr.Code)
}

// ─────────────────────────────────────────────────────────────────────────────
// ParseList
// ─────────────────────────────────────────────────────────────────────────────

func TestParser_ParseList_Success_AbsolutizesURLs(t *testing.T) {
	repo := &fakeRepo{rules: []*storage.ParsingRuleRecord{listRule()}}
	p := rule.NewParser(rule.NewResolver(repo))

	items, err := p.ParseList(makeRaw("https://news.example.com/category/politics", listHTML))
	require.NoError(t, err)
	require.Len(t, items, 3)

	assert.Equal(t, "https://news.example.com/article/1", items[0].URL, "상대 경로는 base 로 절대 URL 화")
	assert.Equal(t, "First Item Title", items[0].Title)
	assert.Equal(t, "Summary 1", items[0].Summary)

	assert.Equal(t, "https://news.example.com/article/2", items[1].URL)
	assert.Equal(t, "https://news.example.com/article/3", items[2].URL, "이미 절대 URL 은 그대로")
}

func TestParser_ParseList_MissingItemContainer_EmptySelector(t *testing.T) {
	r := listRule()
	r.Selectors.ItemContainer = nil
	repo := &fakeRepo{rules: []*storage.ParsingRuleRecord{r}}
	p := rule.NewParser(rule.NewResolver(repo))

	_, err := p.ParseList(makeRaw("https://news.example.com/x", listHTML))
	require.Error(t, err)
	var rerr *rule.Error
	require.ErrorAs(t, err, &rerr)
	assert.Equal(t, rule.ErrEmptySelector, rerr.Code)
}

func TestParser_ParseList_MissingItemLink_EmptySelector(t *testing.T) {
	r := listRule()
	r.Selectors.ItemLink = nil
	repo := &fakeRepo{rules: []*storage.ParsingRuleRecord{r}}
	p := rule.NewParser(rule.NewResolver(repo))

	_, err := p.ParseList(makeRaw("https://news.example.com/x", listHTML))
	require.Error(t, err)
	var rerr *rule.Error
	require.ErrorAs(t, err, &rerr)
	assert.Equal(t, rule.ErrEmptySelector, rerr.Code)
}

func TestParser_ParseList_NoRule_ReturnsErrNoRule(t *testing.T) {
	repo := &fakeRepo{notFound: true}
	p := rule.NewParser(rule.NewResolver(repo))

	_, err := p.ParseList(makeRaw("https://nope.example.com/list", listHTML))
	require.Error(t, err)
	assert.True(t, errors.Is(err, &rule.Error{Code: rule.ErrNoRule}))
}

func TestNewParser_NilResolver_Panics(t *testing.T) {
	assert.PanicsWithValue(t, "rule: NewParser requires non-nil resolver", func() {
		rule.NewParser(nil)
	})
}
