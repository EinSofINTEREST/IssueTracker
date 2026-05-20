// RuleBasedPriorityResolver 의 host/path 룰 라우팅 검증 (이슈 #521).
//
// 기존 PriorityRule (함수형) 은 기존 흐름 보존을 위해 별도 테스트 — 본 파일은 신규 host/path
// hydrate 경로 + matchHostPath 위주.
package bus_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"issuetracker/internal/bus"
	"issuetracker/internal/processor/fetcher/core"
)

func makeJobWithURL(url string) *core.CrawlJob {
	return &core.CrawlJob{
		Target: core.Target{
			URL: url,
		},
	}
}

func TestRuleBasedPriorityResolver_HostPathRule_ExactHostAndPathMatch_ReturnsPriority(t *testing.T) {
	r := bus.NewRuleBasedPriorityResolver(core.PriorityNormal)
	r.SetHostPathRules([]bus.HostPathPriorityRule{
		{HostPattern: "breaking.example.com", PathPattern: "^/news/", Priority: core.PriorityHigh},
	})

	job := makeJobWithURL("https://breaking.example.com/news/headline-123")

	assert.True(t, r.CanResolve(job))
	assert.Equal(t, core.PriorityHigh, r.Resolve(job))
}

func TestRuleBasedPriorityResolver_HostPathRule_HostMatchPathMismatch_FallsThrough(t *testing.T) {
	// path regex 가 매칭 안 되면 룰 미적용 → CanResolve=false (다음 chain 으로 위임).
	r := bus.NewRuleBasedPriorityResolver(core.PriorityNormal)
	r.SetHostPathRules([]bus.HostPathPriorityRule{
		{HostPattern: "breaking.example.com", PathPattern: "^/news/", Priority: core.PriorityHigh},
	})

	job := makeJobWithURL("https://breaking.example.com/sports/different")

	assert.False(t, r.CanResolve(job))
	// fallback (PriorityNormal) 반환 — CanResolve=false 임에도 Resolve 가 fallback 보장.
	assert.Equal(t, core.PriorityNormal, r.Resolve(job))
}

func TestRuleBasedPriorityResolver_HostPathRule_EmptyPath_MatchesAllPaths(t *testing.T) {
	// path_pattern 이 빈 문자열이면 host catch-all 룰 — 모든 path 매칭.
	r := bus.NewRuleBasedPriorityResolver(core.PriorityNormal)
	r.SetHostPathRules([]bus.HostPathPriorityRule{
		{HostPattern: "archive.example.com", PathPattern: "", Priority: core.PriorityLow},
	})

	cases := []string{
		"https://archive.example.com/",
		"https://archive.example.com/old/article",
		"https://archive.example.com/anything",
	}
	for _, url := range cases {
		job := makeJobWithURL(url)
		assert.True(t, r.CanResolve(job), "URL %s should match host catch-all", url)
		assert.Equal(t, core.PriorityLow, r.Resolve(job), "URL %s should resolve to Low", url)
	}
}

func TestRuleBasedPriorityResolver_HostPathRule_HostMismatch_FallsThrough(t *testing.T) {
	r := bus.NewRuleBasedPriorityResolver(core.PriorityNormal)
	r.SetHostPathRules([]bus.HostPathPriorityRule{
		{HostPattern: "breaking.example.com", PathPattern: "", Priority: core.PriorityHigh},
	})

	job := makeJobWithURL("https://other.example.com/news/abc")

	assert.False(t, r.CanResolve(job))
	assert.Equal(t, core.PriorityNormal, r.Resolve(job))
}

func TestRuleBasedPriorityResolver_HostPathRule_InvalidRegex_SilentlySkipped(t *testing.T) {
	// path_pattern 이 RE2 컴파일 실패하면 해당 룰만 제외하고 나머지는 유지.
	r := bus.NewRuleBasedPriorityResolver(core.PriorityNormal)
	r.SetHostPathRules([]bus.HostPathPriorityRule{
		{HostPattern: "bad.example.com", PathPattern: "[invalid(", Priority: core.PriorityHigh},
		{HostPattern: "good.example.com", PathPattern: "^/x/", Priority: core.PriorityHigh},
	})

	// bad rule 은 skip — host 만 매칭으로는 fallback.
	badJob := makeJobWithURL("https://bad.example.com/anywhere")
	assert.False(t, r.CanResolve(badJob))

	// good rule 은 정상 동작.
	goodJob := makeJobWithURL("https://good.example.com/x/y")
	assert.True(t, r.CanResolve(goodJob))
	assert.Equal(t, core.PriorityHigh, r.Resolve(goodJob))
}

func TestRuleBasedPriorityResolver_HostPathRule_MalformedURL_FallsThrough(t *testing.T) {
	r := bus.NewRuleBasedPriorityResolver(core.PriorityNormal)
	r.SetHostPathRules([]bus.HostPathPriorityRule{
		{HostPattern: "x", PathPattern: "", Priority: core.PriorityHigh},
	})

	// 빈 URL — url.Parse 가 host 빈 문자열 반환 → matchHostPath false.
	job := makeJobWithURL("")
	assert.False(t, r.CanResolve(job))
	assert.Equal(t, core.PriorityNormal, r.Resolve(job))
}

func TestRuleBasedPriorityResolver_HostPathRule_AtomicReplace(t *testing.T) {
	// SetHostPathRules 가 atomic 교체 — 두 번째 호출이 첫 번째 룰을 완전히 대체.
	r := bus.NewRuleBasedPriorityResolver(core.PriorityNormal)
	r.SetHostPathRules([]bus.HostPathPriorityRule{
		{HostPattern: "first.example.com", PathPattern: "", Priority: core.PriorityHigh},
	})

	firstJob := makeJobWithURL("https://first.example.com/")
	assert.True(t, r.CanResolve(firstJob))

	// 두 번째 호출 — first 룰 제거.
	r.SetHostPathRules([]bus.HostPathPriorityRule{
		{HostPattern: "second.example.com", PathPattern: "", Priority: core.PriorityLow},
	})

	// first 는 더 이상 매칭 안 됨.
	assert.False(t, r.CanResolve(firstJob))

	// second 는 매칭.
	secondJob := makeJobWithURL("https://second.example.com/x")
	assert.True(t, r.CanResolve(secondJob))
	assert.Equal(t, core.PriorityLow, r.Resolve(secondJob))
}

func TestRuleBasedPriorityResolver_HostPathRule_NilOrEmpty_NoOp(t *testing.T) {
	r := bus.NewRuleBasedPriorityResolver(core.PriorityNormal)
	// hostPathPtr 미초기화 상태.
	job := makeJobWithURL("https://any.example.com/")
	assert.False(t, r.CanResolve(job))
	assert.Equal(t, core.PriorityNormal, r.Resolve(job))

	// 빈 슬라이스로 SetHostPathRules — 동일하게 매칭 없음.
	r.SetHostPathRules(nil)
	assert.False(t, r.CanResolve(job))

	r.SetHostPathRules([]bus.HostPathPriorityRule{})
	assert.False(t, r.CanResolve(job))
}

func TestRuleBasedPriorityResolver_FunctionalRulesEvaluatedBeforeHostPath(t *testing.T) {
	// AddRule (함수형) 이 SetHostPathRules (host/path) 보다 먼저 평가됨 — 정합 일관.
	r := bus.NewRuleBasedPriorityResolver(core.PriorityNormal)
	r.AddRule(func(job *core.CrawlJob) bool {
		return job.CrawlerName == "vip"
	}, core.PriorityHigh)
	r.SetHostPathRules([]bus.HostPathPriorityRule{
		{HostPattern: "any.example.com", PathPattern: "", Priority: core.PriorityLow},
	})

	// CrawlerName=vip + host=any.example.com — 함수형 룰 우선.
	job := &core.CrawlJob{
		CrawlerName: "vip",
		Target:      core.Target{URL: "https://any.example.com/"},
	}
	assert.Equal(t, core.PriorityHigh, r.Resolve(job))
}
