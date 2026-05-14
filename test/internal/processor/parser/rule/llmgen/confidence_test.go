package llmgen_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"issuetracker/internal/processor/parser/rule/llmgen"
	"issuetracker/internal/storage/model"
)

const confSampleHTML = `<!DOCTYPE html>
<html><body>
<h1 class="article-title">Sample Headline</h1>
<article><p>Body paragraph one.</p><p>Body paragraph two.</p></article>
<span class="author">Jane Doe</span>
<time datetime="2026-05-07T10:30:00Z">2026-05-07</time>
<meta name="description" content="A short summary of the article.">
</body></html>`

// ─────────────────────────────────────────────────────────────────────────────
// ComputeFieldConfidence
// ─────────────────────────────────────────────────────────────────────────────

func TestComputeFieldConfidence_AllSelectorsMatch(t *testing.T) {
	sm := model.SelectorMap{
		Title:       &model.FieldSelector{CSS: "h1.article-title"},
		MainContent: &model.FieldSelector{CSS: "article p", Multi: true},
		Author:      &model.FieldSelector{CSS: "span.author"},
		PublishedAt: &model.FieldSelector{CSS: "time", Attribute: "datetime"},
		Summary:     &model.FieldSelector{CSS: "meta[name=description]", Attribute: "content"},
	}
	got := llmgen.ComputeFieldConfidence(sm, confSampleHTML)

	assert.Equal(t, 1.0, got["title"].HitRate)
	assert.Equal(t, 1.0, got["main_content"].HitRate)
	assert.Equal(t, 1.0, got["author"].HitRate)
	assert.Equal(t, 1.0, got["published_at"].HitRate, "datetime attribute 가 RFC3339 parse 가능")
	assert.Equal(t, 1.0, got["summary"].HitRate)
	for _, c := range got {
		assert.Equal(t, 1, c.SampleCount, "단일 sample 환경 — sample_count=1")
	}
}

func TestComputeFieldConfidence_MissingSelectorReturnsZero(t *testing.T) {
	sm := model.SelectorMap{
		Title:       &model.FieldSelector{CSS: "h1.does-not-exist"},
		MainContent: &model.FieldSelector{CSS: "article p"},
	}
	got := llmgen.ComputeFieldConfidence(sm, confSampleHTML)

	assert.Equal(t, 0.0, got["title"].HitRate, "selector 매칭 0 → hit_rate=0")
	assert.Equal(t, 1.0, got["main_content"].HitRate)
}

func TestComputeFieldConfidence_PublishedAtUnparseableText_ReturnsZero(t *testing.T) {
	// time element 는 매칭되나 text 가 \"2026-05-07\" 이 아니라 random 문자열인 케이스.
	html := `<html><body><time class="ts">not a date</time></body></html>`
	sm := model.SelectorMap{
		PublishedAt: &model.FieldSelector{CSS: "time.ts"}, // attribute 없음 → text() 사용
	}
	got := llmgen.ComputeFieldConfidence(sm, html)
	assert.Equal(t, 0.0, got["published_at"].HitRate, "selector 매칭하나 text 가 date parse 실패 → hit_rate=0")
}

func TestComputeFieldConfidence_PublishedAtVariousFormats(t *testing.T) {
	cases := []struct {
		name string
		html string
		want float64
	}{
		{"ISO 8601 RFC3339", `<time>2026-05-07T10:30:00Z</time>`, 1.0},
		{"date only", `<time>2026-05-07</time>`, 1.0},
		{"slash-separated", `<time>2026/05/07 10:30:00</time>`, 1.0},
		{"dot-separated", `<time>2026.05.07</time>`, 1.0},
		{"english long", `<time>January 2, 2026</time>`, 1.0},
		{"random text", `<time>hello world</time>`, 0.0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			full := "<html><body>" + tc.html + "</body></html>"
			sm := model.SelectorMap{PublishedAt: &model.FieldSelector{CSS: "time"}}
			got := llmgen.ComputeFieldConfidence(sm, full)
			assert.Equal(t, tc.want, got["published_at"].HitRate)
		})
	}
}

func TestComputeFieldConfidence_EmptyExtractedValue_ReturnsZero(t *testing.T) {
	// element 는 매칭되나 추출 값이 비어있는 케이스 (PR #293 CodeRabbit Major).
	cases := []struct {
		name string
		html string
		fs   *model.FieldSelector
	}{
		{
			name: "text selector 매칭하나 element 비어있음",
			html: `<html><body><h1 class="title"></h1></body></html>`,
			fs:   &model.FieldSelector{CSS: "h1.title"},
		},
		{
			name: "attribute selector — attribute 부재",
			html: `<html><body><a class="link">text</a></body></html>`,
			fs:   &model.FieldSelector{CSS: "a.link", Attribute: "href"},
		},
		{
			name: "attribute selector — attribute 빈 문자열",
			html: `<html><body><a class="link" href="">text</a></body></html>`,
			fs:   &model.FieldSelector{CSS: "a.link", Attribute: "href"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sm := model.SelectorMap{Title: tc.fs}
			got := llmgen.ComputeFieldConfidence(sm, tc.html)
			assert.Equal(t, 0.0, got["title"].HitRate, "추출 값이 빈 문자열이면 hit_rate=0")
		})
	}
}

func TestComputeFieldConfidence_NilSelectorIgnored(t *testing.T) {
	sm := model.SelectorMap{
		Title: &model.FieldSelector{CSS: "h1.article-title"},
		// MainContent / Author / 기타 nil
	}
	got := llmgen.ComputeFieldConfidence(sm, confSampleHTML)
	assert.Equal(t, 1.0, got["title"].HitRate)
	_, mainExists := got["main_content"]
	assert.False(t, mainExists, "nil selector 는 confidence map 에 entry 없음")
}

func TestComputeFieldConfidence_HTMLParseError_ReturnsEmptyMap(t *testing.T) {
	// goquery 는 거의 모든 입력을 파싱 — 빈 string 도 정상 파싱하므로 실제 파싱 에러
	// 시뮬레이션이 어려움. 빈 SelectorMap 으로 빈 map 반환만 확인.
	got := llmgen.ComputeFieldConfidence(model.SelectorMap{}, confSampleHTML)
	assert.Empty(t, got, "selector 자체가 모두 nil 이면 빈 map")
}

// ─────────────────────────────────────────────────────────────────────────────
// ApplyConfidenceFilter
// ─────────────────────────────────────────────────────────────────────────────

func TestApplyConfidenceFilter_DropsLowConfidenceSelectors(t *testing.T) {
	sm := model.SelectorMap{
		Title:       &model.FieldSelector{CSS: "h1"},
		MainContent: &model.FieldSelector{CSS: "article"},
		PublishedAt: &model.FieldSelector{CSS: "time"}, // 신뢰도 0
	}
	conf := map[string]model.FieldConfidence{
		"title":        {HitRate: 1.0, SampleCount: 1},
		"main_content": {HitRate: 1.0, SampleCount: 1},
		"published_at": {HitRate: 0.0, SampleCount: 1}, // drop 대상
	}
	got := llmgen.ApplyConfidenceFilter(sm, conf)
	assert.NotNil(t, got.Title, "신뢰도 1.0 — selector 보존")
	assert.NotNil(t, got.MainContent)
	assert.Nil(t, got.PublishedAt, "신뢰도 0.0 — selector drop")
}

func TestApplyConfidenceFilter_ConfidenceMissingForField_KeepsSelector(t *testing.T) {
	sm := model.SelectorMap{
		Title: &model.FieldSelector{CSS: "h1"},
	}
	got := llmgen.ApplyConfidenceFilter(sm, map[string]model.FieldConfidence{})
	assert.NotNil(t, got.Title, "confidence map 에 entry 없으면 보수적으로 보존")
}

func TestApplyConfidenceFilter_AllAboveThreshold_KeepsAll(t *testing.T) {
	sm := model.SelectorMap{
		Title:       &model.FieldSelector{CSS: "h1"},
		MainContent: &model.FieldSelector{CSS: "article"},
	}
	conf := map[string]model.FieldConfidence{
		"title":        {HitRate: 1.0, SampleCount: 1},
		"main_content": {HitRate: 0.6, SampleCount: 1}, // 임계값 0.5 위
	}
	got := llmgen.ApplyConfidenceFilter(sm, conf)
	assert.NotNil(t, got.Title)
	assert.NotNil(t, got.MainContent)
}
