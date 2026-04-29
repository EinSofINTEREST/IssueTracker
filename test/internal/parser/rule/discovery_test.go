package rule_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/crawler/parser/rule"
	"issuetracker/internal/storage"
)

// fullPageHTML 은 카테고리 페이지를 모사 — 헤더 / 본문 / 사이드바 / 푸터에
// article 링크가 흩어져있고, ItemContainer 한 개로 잡기 어려운 구조.
const fullPageHTML = `
<html><body>
  <nav>
    <a href="/">Home</a>
    <a href="/category/politics">Politics</a>
  </nav>

  <main>
    <a href="/article/2026/04/29/headline-one">Top Story</a>
    <a href="/article/2026/04/29/headline-two">Second Story</a>
  </main>

  <aside class="sidebar">
    <a href="/article/2026/04/28/related-one">Related One</a>
    <a href="/article/2026/04/28/related-two">Related Two</a>
  </aside>

  <section class="related">
    <a href="https://news.example.com/article/2026/04/27/cross">Cross-link Absolute</a>
    <a href="https://other.example.com/article/2026/04/27/external">External Site</a>
  </section>

  <footer>
    <a href="/about">About</a>
    <a href="/login">Login</a>
    <a href="javascript:void(0)">JS Link</a>
    <a href="mailto:hi@example.com">Contact</a>
  </footer>
</body></html>
`

func discoveryRule(cfg *storage.LinkDiscoveryConfig) *storage.ParsingRuleRecord {
	return &storage.ParsingRuleRecord{
		ID:          3,
		SourceName:  "test",
		HostPattern: "news.example.com",
		TargetType:  storage.TargetTypeList,
		Version:     1,
		Enabled:     true,
		Selectors: storage.SelectorMap{
			LinkDiscovery: cfg,
		},
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// PageLinkDiscovery 직접 호출
// ─────────────────────────────────────────────────────────────────────────────

func TestPageLinkDiscovery_BasicRegexFilter(t *testing.T) {
	d := rule.NewPageLinkDiscovery()
	cfg := &storage.LinkDiscoveryConfig{
		ArticleURLPattern: `^https?://news\.example\.com/article/\d{4}/\d{2}/\d{2}/`,
		SameOriginOnly:    true,
	}

	items, err := d.Discover(makeRaw("https://news.example.com/category/politics", fullPageHTML), cfg)
	require.NoError(t, err)

	// main / sidebar / cross-link 5개 article 모두 매칭 (본문 + 사이드바 + 절대 cross-link).
	// nav / footer / external / mailto / javascript 는 제외.
	require.Len(t, items, 5)

	gotURLs := make([]string, len(items))
	for i, it := range items {
		gotURLs[i] = it.URL
	}
	assert.ElementsMatch(t, []string{
		"https://news.example.com/article/2026/04/29/headline-one",
		"https://news.example.com/article/2026/04/29/headline-two",
		"https://news.example.com/article/2026/04/28/related-one",
		"https://news.example.com/article/2026/04/28/related-two",
		"https://news.example.com/article/2026/04/27/cross",
	}, gotURLs)
}

func TestPageLinkDiscovery_SameOriginOnly_ExcludesExternal(t *testing.T) {
	d := rule.NewPageLinkDiscovery()
	cfg := &storage.LinkDiscoveryConfig{
		ArticleURLPattern: `/article/\d{4}/\d{2}/\d{2}/`,
		SameOriginOnly:    true,
	}

	items, err := d.Discover(makeRaw("https://news.example.com/category/politics", fullPageHTML), cfg)
	require.NoError(t, err)

	for _, it := range items {
		assert.NotContains(t, it.URL, "other.example.com", "외부 origin 은 SameOriginOnly 로 차단됨")
	}
}

func TestPageLinkDiscovery_SameOriginOff_AllowsExternal(t *testing.T) {
	d := rule.NewPageLinkDiscovery()
	cfg := &storage.LinkDiscoveryConfig{
		ArticleURLPattern: `/article/\d{4}/\d{2}/\d{2}/`,
		SameOriginOnly:    false,
	}

	items, err := d.Discover(makeRaw("https://news.example.com/category/politics", fullPageHTML), cfg)
	require.NoError(t, err)

	hasExternal := false
	for _, it := range items {
		if it.URL == "https://other.example.com/article/2026/04/27/external" {
			hasExternal = true
		}
	}
	assert.True(t, hasExternal, "SameOriginOnly=false 면 외부 origin 도 통과")
}

func TestPageLinkDiscovery_PathPrefixesActAsCutoff(t *testing.T) {
	d := rule.NewPageLinkDiscovery()
	cfg := &storage.LinkDiscoveryConfig{
		ArticleURLPattern: `/\d{4}/\d{2}/\d{2}/`,
		SameOriginOnly:    true,
		PathPrefixes:      []string{"/article/"}, // /category/, /about, /login 등 제외
	}

	items, err := d.Discover(makeRaw("https://news.example.com/category/politics", fullPageHTML), cfg)
	require.NoError(t, err)
	require.NotEmpty(t, items)

	for _, it := range items {
		assert.Contains(t, it.URL, "/article/", "PathPrefixes 가 1차 cutoff 로 동작")
	}
}

func TestPageLinkDiscovery_ExcludePatterns(t *testing.T) {
	d := rule.NewPageLinkDiscovery()
	cfg := &storage.LinkDiscoveryConfig{
		ArticleURLPattern: `/article/`,
		SameOriginOnly:    true,
		ExcludePatterns:   []string{"/2026/04/28/"}, // 4월 28일 article 만 제외
	}

	items, err := d.Discover(makeRaw("https://news.example.com/category/politics", fullPageHTML), cfg)
	require.NoError(t, err)

	for _, it := range items {
		assert.NotContains(t, it.URL, "/2026/04/28/", "ExcludePatterns 매칭은 모두 차단")
	}
}

func TestPageLinkDiscovery_MaxLinksPerPage(t *testing.T) {
	d := rule.NewPageLinkDiscovery()
	cfg := &storage.LinkDiscoveryConfig{
		ArticleURLPattern: `/article/`,
		SameOriginOnly:    true,
		MaxLinksPerPage:   2,
	}

	items, err := d.Discover(makeRaw("https://news.example.com/category/politics", fullPageHTML), cfg)
	require.NoError(t, err)
	assert.Len(t, items, 2, "MaxLinksPerPage cap 적용")
}

func TestPageLinkDiscovery_NoMatch_ReturnsParseFailure(t *testing.T) {
	d := rule.NewPageLinkDiscovery()
	cfg := &storage.LinkDiscoveryConfig{
		ArticleURLPattern: `/never-matches-anything/`,
		SameOriginOnly:    true,
	}

	_, err := d.Discover(makeRaw("https://news.example.com/category/politics", fullPageHTML), cfg)
	require.Error(t, err)
	var rerr *rule.Error
	require.ErrorAs(t, err, &rerr)
	assert.Equal(t, rule.ErrParseFailure, rerr.Code, "0건 매칭 = pattern stale 진단")
}

// TestPageLinkDiscovery_EmptyPattern_AllPass 는 빈 ArticleURLPattern 이 all-pass 모드로
// 동작함을 검증합니다 (이슈 #148). pkg/links.Extractor 의 기본 제외 패턴 (javascript:/mailto:/tel:/login 등)
// 만 적용되고 그 외 모든 (a href) 가 통과.
func TestPageLinkDiscovery_EmptyPattern_AllPass(t *testing.T) {
	d := rule.NewPageLinkDiscovery()
	cfg := &storage.LinkDiscoveryConfig{
		ArticleURLPattern: "",
		SameOriginOnly:    true,
	}

	items, err := d.Discover(makeRaw("https://news.example.com/category/politics", fullPageHTML), cfg)
	require.NoError(t, err, "all-pass 모드는 정상 통과")

	// nav, /category/politics, main article × 2, sidebar × 2 = 6 (footer/external/login/mailto/javascript 제외)
	require.GreaterOrEqual(t, len(items), 6, "fullPageHTML 의 same-origin 통과 가능 링크 모두 포함")

	// 명시적으로 article 만 외 nav/category 도 포함됐는지 확인
	urls := make(map[string]bool)
	for _, it := range items {
		urls[it.URL] = true
	}
	assert.True(t, urls["https://news.example.com/category/politics"], "category 링크 포함")
	assert.True(t, urls["https://news.example.com/article/2026/04/29/headline-one"], "article 링크 포함")
	assert.False(t, urls["https://news.example.com/login"], "기본 제외 패턴 (/login) 은 차단")
}

// TestPageLinkDiscovery_EmptyPattern_ExcludeStillApplies 는 all-pass 모드에서도
// ExcludePatterns 가 정상 동작함을 검증합니다.
func TestPageLinkDiscovery_EmptyPattern_ExcludeStillApplies(t *testing.T) {
	d := rule.NewPageLinkDiscovery()
	cfg := &storage.LinkDiscoveryConfig{
		ArticleURLPattern: "",
		SameOriginOnly:    true,
		ExcludePatterns:   []string{"/category/", "/about"},
	}

	items, err := d.Discover(makeRaw("https://news.example.com/category/politics", fullPageHTML), cfg)
	require.NoError(t, err)

	for _, it := range items {
		assert.NotContains(t, it.URL, "/category/", "all-pass 모드에서도 ExcludePatterns 적용")
		assert.NotContains(t, it.URL, "/about", "운영자 정의 제외 패턴 적용")
	}
}

func TestPageLinkDiscovery_NilConfig_ReturnsEmptySelector(t *testing.T) {
	d := rule.NewPageLinkDiscovery()

	_, err := d.Discover(makeRaw("https://news.example.com/x", fullPageHTML), nil)
	require.Error(t, err)
	var rerr *rule.Error
	require.ErrorAs(t, err, &rerr)
	assert.Equal(t, rule.ErrEmptySelector, rerr.Code)
}

func TestPageLinkDiscovery_InvalidRegex_ReturnsEmptySelector(t *testing.T) {
	d := rule.NewPageLinkDiscovery()
	cfg := &storage.LinkDiscoveryConfig{
		ArticleURLPattern: `[invalid(regex`,
	}

	_, err := d.Discover(makeRaw("https://news.example.com/x", fullPageHTML), cfg)
	require.Error(t, err)
	var rerr *rule.Error
	require.ErrorAs(t, err, &rerr)
	assert.Equal(t, rule.ErrEmptySelector, rerr.Code, "compile 실패는 운영자 입력 오류 → ErrEmptySelector")
}

func TestPageLinkDiscovery_EmptyRaw_ReturnsParseFailure(t *testing.T) {
	d := rule.NewPageLinkDiscovery()
	cfg := &storage.LinkDiscoveryConfig{ArticleURLPattern: `/article/`}

	_, err := d.Discover(makeRaw("https://news.example.com/x", ""), cfg)
	require.Error(t, err)
	var rerr *rule.Error
	require.ErrorAs(t, err, &rerr)
	assert.Equal(t, rule.ErrParseFailure, rerr.Code)
}

// TestPageLinkDiscovery_ExtractorFailure_ReturnsParseFailure 는 pkg/links.Extractor.Extract
// 가 내부적으로 실패하는 케이스를 다룹니다 (Coderabbit 피드백 — 에러 경로 100% 커버).
//
// Extract 는 url.Parse(raw.URL) 가 실패할 때 에러 반환 — invalid base URL 로 트리거.
func TestPageLinkDiscovery_ExtractorFailure_ReturnsParseFailure(t *testing.T) {
	d := rule.NewPageLinkDiscovery()
	cfg := &storage.LinkDiscoveryConfig{ArticleURLPattern: `/article/`}

	// raw.URL 이 control character 포함 → url.Parse 실패 → extractor 의 resolveBase 실패
	_, err := d.Discover(makeRaw("http://example.com/\x7f", fullPageHTML), cfg)
	require.Error(t, err)
	var rerr *rule.Error
	require.ErrorAs(t, err, &rerr)
	assert.Equal(t, rule.ErrParseFailure, rerr.Code, "extractor 내부 실패는 ErrParseFailure 로 전파")
}

// ─────────────────────────────────────────────────────────────────────────────
// rule.Parser.ParseLinks 분기 — LinkDiscovery vs ItemContainer
// ─────────────────────────────────────────────────────────────────────────────

func TestParser_ParseLinks_RoutesToDiscoveryWhenPatternSet(t *testing.T) {
	cfg := &storage.LinkDiscoveryConfig{
		ArticleURLPattern: `/article/\d{4}/\d{2}/\d{2}/`,
		SameOriginOnly:    true,
	}
	repo := &fakeRepo{rules: []*storage.ParsingRuleRecord{discoveryRule(cfg)}}
	p := rule.NewParser(rule.NewResolver(repo))

	items, err := p.ParseLinks(context.Background(), makeRaw("https://news.example.com/category/politics", fullPageHTML))
	require.NoError(t, err)
	assert.Len(t, items, 5, "LinkDiscovery 모드로 5개 article 발견")
}

func TestParser_ParseLinks_FallsBackToItemContainerWhenPatternEmpty(t *testing.T) {
	// LinkDiscovery 가 nil → 기존 ItemContainer 경로 사용
	repo := &fakeRepo{rules: []*storage.ParsingRuleRecord{listRule()}}
	p := rule.NewParser(rule.NewResolver(repo))

	items, err := p.ParseLinks(context.Background(), makeRaw("https://news.example.com/category", listHTML))
	require.NoError(t, err)
	assert.Len(t, items, 3, "ItemContainer 경로로 3개 추출 (기존 동작 회귀 없음)")
}

// TestParser_ParseLinks_LinkDiscoveryWithEmptyPattern_AllPassDiscovery 는 이슈 #148 변경 검증:
// LinkDiscovery 객체가 채워져 있으면 ArticleURLPattern 이 빈 문자열이어도 discovery 모드 진입.
// ItemContainer fallback 은 LinkDiscovery 자체가 nil 일 때만.
func TestParser_ParseLinks_LinkDiscoveryWithEmptyPattern_AllPassDiscovery(t *testing.T) {
	// LinkDiscovery 객체는 있지만 pattern 비어있음 → all-pass discovery (ItemContainer 사용 안 함)
	r := listRule()
	r.Selectors.LinkDiscovery = &storage.LinkDiscoveryConfig{
		ArticleURLPattern: "",
		SameOriginOnly:    true,
	}
	repo := &fakeRepo{rules: []*storage.ParsingRuleRecord{r}}
	p := rule.NewParser(rule.NewResolver(repo))

	items, err := p.ParseLinks(context.Background(), makeRaw("https://news.example.com/category", listHTML))
	require.NoError(t, err)
	require.Len(t, items, 3, "all-pass 모드 — listHTML 의 a href 3개 모두 통과")
}
