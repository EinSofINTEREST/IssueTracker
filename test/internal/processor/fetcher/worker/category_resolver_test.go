package worker_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"issuetracker/internal/processor/fetcher/core"
	"issuetracker/internal/processor/fetcher/worker"
	"issuetracker/pkg/categories"
)

// ─────────────────────────────────────────────────────────────────────────────
// RetryPriorityResolver (이슈 #381)
// ─────────────────────────────────────────────────────────────────────────────

func TestRetryPriorityResolver_CanResolve(t *testing.T) {
	r := worker.NewRetryPriorityResolver()

	tests := []struct {
		name       string
		retryCount int
		want       bool
	}{
		{"RetryCount=0 → 위임 (다음 resolver)", 0, false},
		{"RetryCount=1 → 흡수 (Normal)", 1, true},
		{"RetryCount=5 → 흡수", 5, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			job := &core.CrawlJob{RetryCount: tt.retryCount}
			assert.Equal(t, tt.want, r.CanResolve(job))
		})
	}
}

func TestRetryPriorityResolver_Resolve_AlwaysNormal(t *testing.T) {
	r := worker.NewRetryPriorityResolver()

	// CanResolve=true 인 케이스만 Resolve 호출됨 — 항상 Normal 반환.
	job := &core.CrawlJob{RetryCount: 3}
	assert.Equal(t, core.PriorityNormal, r.Resolve(job))
}

// ─────────────────────────────────────────────────────────────────────────────
// CategoryBasedResolver (이슈 #381)
// ─────────────────────────────────────────────────────────────────────────────

func newJobWithCategory(targetType core.TargetType, cat string) *core.CrawlJob {
	job := &core.CrawlJob{
		Target: core.Target{Type: targetType},
	}
	if cat != "" {
		job.Target.Metadata = map[string]interface{}{
			categories.MetadataKey: cat,
		}
	}
	return job
}

func TestCategoryBasedResolver_Traversal_ForcesLow(t *testing.T) {
	r := worker.NewCategoryBasedResolver()

	tests := []struct {
		name string
		tt   core.TargetType
	}{
		{"sitemap → Low", core.TargetTypeSitemap},
		{"category → Low", core.TargetTypeCategory},
		{"search_results → Low", core.TargetTypeSearchResults},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// traversal type 은 metadata 무관하게 Low + 항상 결정 가능
			job := newJobWithCategory(tt.tt, "politics") // category hint 있어도 무시
			assert.True(t, r.CanResolve(job))
			assert.Equal(t, core.PriorityLow, r.Resolve(job))
		})
	}
}

func TestCategoryBasedResolver_Article_ByCategory(t *testing.T) {
	r := worker.NewCategoryBasedResolver()

	tests := []struct {
		name string
		cat  string
		want core.Priority
	}{
		{"politics → High", "politics", core.PriorityHigh},
		{"economy → High", "economy", core.PriorityHigh},
		{"society → High", "society", core.PriorityHigh},
		{"sports → Normal", "sports", core.PriorityNormal},
		{"tech → Normal", "tech", core.PriorityNormal},
		{"community → Normal", "community", core.PriorityNormal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			job := newJobWithCategory(core.TargetTypeArticle, tt.cat)
			assert.True(t, r.CanResolve(job), "알려진 카테고리는 결정 가능해야 함")
			assert.Equal(t, tt.want, r.Resolve(job))
		})
	}
}

func TestCategoryBasedResolver_Article_UnknownCategory_Delegates(t *testing.T) {
	r := worker.NewCategoryBasedResolver()

	tests := []struct {
		name string
		meta map[string]interface{}
	}{
		{"Metadata 자체 nil", nil},
		{"category 키 없음", map[string]interface{}{"other": "x"}},
		{"빈 문자열 category", map[string]interface{}{categories.MetadataKey: ""}},
		{"미등록 category", map[string]interface{}{categories.MetadataKey: "unknown_topic"}},
		{"비-문자열 category", map[string]interface{}{categories.MetadataKey: 42}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			job := &core.CrawlJob{
				Target: core.Target{
					Type:     core.TargetTypeArticle,
					Metadata: tt.meta,
				},
			}
			assert.False(t, r.CanResolve(job),
				"미지정/미등록 카테고리는 다음 resolver 로 위임해야 함")
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// CompositeResolver 체인 통합 검증 (이슈 #381 의 chain 순서)
// ─────────────────────────────────────────────────────────────────────────────

func TestCompositeChain_RetryAndCategory_LowFallback(t *testing.T) {
	// cmd/issuetracker/main.go 의 신규 chain 과 동일 구성:
	// Retry → CategoryBased → Source → Rule → Default(Low)
	composite := worker.NewCompositeResolver(core.PriorityLow)
	composite.Add(worker.NewRetryPriorityResolver())
	composite.Add(worker.NewCategoryBasedResolver())
	composite.Add(worker.NewSourcePriorityResolver(core.PriorityNormal))
	composite.Add(worker.NewRuleBasedPriorityResolver(core.PriorityNormal))

	tests := []struct {
		name string
		job  *core.CrawlJob
		want core.Priority
	}{
		{
			name: "retry 우선 — politics article 이라도 RetryCount>0 → Normal",
			job: &core.CrawlJob{
				RetryCount: 2,
				Target: core.Target{
					Type:     core.TargetTypeArticle,
					Metadata: map[string]interface{}{categories.MetadataKey: "politics"},
				},
			},
			want: core.PriorityNormal,
		},
		{
			name: "초기 fetch — politics article → High",
			job: &core.CrawlJob{
				Target: core.Target{
					Type:     core.TargetTypeArticle,
					Metadata: map[string]interface{}{categories.MetadataKey: "politics"},
				},
			},
			want: core.PriorityHigh,
		},
		{
			name: "category page → Low (traversal)",
			job: &core.CrawlJob{
				Target: core.Target{Type: core.TargetTypeCategory},
			},
			want: core.PriorityLow,
		},
		{
			name: "미분류 article → Low (cold-start fallback)",
			job: &core.CrawlJob{
				Target: core.Target{Type: core.TargetTypeArticle},
			},
			want: core.PriorityLow,
		},
		{
			name: "sports article → Normal",
			job: &core.CrawlJob{
				Target: core.Target{
					Type:     core.TargetTypeArticle,
					Metadata: map[string]interface{}{categories.MetadataKey: "sports"},
				},
			},
			want: core.PriorityNormal,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, composite.Resolve(tt.job))
		})
	}
}
