package sources_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/processor/fetcher/domain/general/sources"
	"issuetracker/internal/storage"
)

// TestAnalyzeSources_MultiHostDifferentBaseURLs_Pass — 이슈 #347 의 핵심 회귀 테스트.
// 같은 source_name 의 host 들이 서로 다른 base_url 을 가져도 RegisterAll 이 boot fail 하지 않아야 함.
//
// 예: dcinside / reddit / slashdot 의 dual-host seed (PR #334/#335 의 의도).
func TestAnalyzeSources_MultiHostDifferentBaseURLs_Pass(t *testing.T) {
	t.Parallel()
	rules := []*storage.FetcherRuleRecord{
		{
			SourceName: "dcinside", HostPattern: "gall.dcinside.com",
			SourceType: "community", Country: "KR", Language: "ko",
			BaseURL: "https://gall.dcinside.com", RequestsPerHour: 100,
		},
		{
			SourceName: "dcinside", HostPattern: "gallery.dcinside.com",
			SourceType: "community", Country: "KR", Language: "ko",
			BaseURL:         "https://gallery.dcinside.com", // 다른 BaseURL — 이전에는 reject
			RequestsPerHour: 100,
		},
	}
	bySource, hostsBySource, baseURLsBySource, err := sources.AnalyzeSources(rules)
	require.NoError(t, err, "BaseURL 만 다른 경우 boot fail 하면 안 됨 (이슈 #347)")
	assert.Contains(t, bySource, "dcinside")
	assert.ElementsMatch(t, []string{"gall.dcinside.com", "gallery.dcinside.com"}, hostsBySource["dcinside"])
	// 두 base_url 모두 set 에 캡쳐 — 운영 로그용
	assert.Len(t, baseURLsBySource["dcinside"], 2)
}

// TestAnalyzeSources_CanonicalSelection_PrefersBaseURLHostMatch — canonical 선택 동작 보존.
// base_url hostname == host_pattern 인 row 가 canonical 로 선택되어야 함 (HealthCheck 사용).
func TestAnalyzeSources_CanonicalSelection_PrefersBaseURLHostMatch(t *testing.T) {
	t.Parallel()
	rules := []*storage.FetcherRuleRecord{
		// 첫 row 는 base_url 이 다른 host 를 가리킴 (non-canonical)
		{
			SourceName: "slashdot", HostPattern: "news.slashdot.org",
			SourceType: "community", Country: "US", Language: "en",
			BaseURL: "https://slashdot.org", RequestsPerHour: 200,
		},
		// 둘째 row 는 base_url hostname 이 host_pattern 과 일치 — canonical
		{
			SourceName: "slashdot", HostPattern: "slashdot.org",
			SourceType: "community", Country: "US", Language: "en",
			BaseURL: "https://slashdot.org", RequestsPerHour: 200,
		},
	}
	bySource, _, _, err := sources.AnalyzeSources(rules)
	require.NoError(t, err)
	assert.Equal(t, "slashdot.org", bySource["slashdot"].Rec.HostPattern, "canonical 선택 = base_url hostname == host_pattern")
}

// TestAnalyzeSources_RPHMismatch_Rejected — RequestsPerHour 불일치는 여전히 reject.
// IP-bucket race 보호를 위해 유지 (이슈 #347 분석 참고).
func TestAnalyzeSources_RPHMismatch_Rejected(t *testing.T) {
	t.Parallel()
	rules := []*storage.FetcherRuleRecord{
		{
			SourceName: "dcinside", HostPattern: "gall.dcinside.com",
			SourceType: "community", Country: "KR", Language: "ko",
			BaseURL: "https://gall.dcinside.com", RequestsPerHour: 100,
		},
		{
			SourceName: "dcinside", HostPattern: "gallery.dcinside.com",
			SourceType: "community", Country: "KR", Language: "ko",
			BaseURL: "https://gallery.dcinside.com", RequestsPerHour: 200, // mismatch
		},
	}
	_, _, _, err := sources.AnalyzeSources(rules)
	require.Error(t, err, "RPH 불일치는 reject")
	assert.Contains(t, err.Error(), "inconsistent ")
}

// TestAnalyzeSources_CountryMismatch_Rejected — 의미론적 metadata 불일치는 reject.
func TestAnalyzeSources_CountryMismatch_Rejected(t *testing.T) {
	t.Parallel()
	rules := []*storage.FetcherRuleRecord{
		{SourceName: "foo", HostPattern: "a.com", SourceType: "news", Country: "KR", Language: "ko", BaseURL: "https://a.com", RequestsPerHour: 100},
		{SourceName: "foo", HostPattern: "b.com", SourceType: "news", Country: "US", Language: "ko", BaseURL: "https://b.com", RequestsPerHour: 100},
	}
	_, _, _, err := sources.AnalyzeSources(rules)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "inconsistent ")
}

// TestAnalyzeSources_LanguageMismatch_Rejected — Language 불일치 reject.
func TestAnalyzeSources_LanguageMismatch_Rejected(t *testing.T) {
	t.Parallel()
	rules := []*storage.FetcherRuleRecord{
		{SourceName: "foo", HostPattern: "a.com", SourceType: "news", Country: "KR", Language: "ko", BaseURL: "https://a.com", RequestsPerHour: 100},
		{SourceName: "foo", HostPattern: "b.com", SourceType: "news", Country: "KR", Language: "en", BaseURL: "https://b.com", RequestsPerHour: 100},
	}
	_, _, _, err := sources.AnalyzeSources(rules)
	require.Error(t, err)
}

// TestAnalyzeSources_SourceTypeMismatch_Rejected — SourceType 불일치 reject.
func TestAnalyzeSources_SourceTypeMismatch_Rejected(t *testing.T) {
	t.Parallel()
	rules := []*storage.FetcherRuleRecord{
		{SourceName: "foo", HostPattern: "a.com", SourceType: "news", Country: "KR", Language: "ko", BaseURL: "https://a.com", RequestsPerHour: 100},
		{SourceName: "foo", HostPattern: "b.com", SourceType: "community", Country: "KR", Language: "ko", BaseURL: "https://b.com", RequestsPerHour: 100},
	}
	_, _, _, err := sources.AnalyzeSources(rules)
	require.Error(t, err)
}

// TestAnalyzeSources_EmptySourceNameSkipped — source_name 이 빈 row 는 skip (legacy).
func TestAnalyzeSources_EmptySourceNameSkipped(t *testing.T) {
	t.Parallel()
	rules := []*storage.FetcherRuleRecord{
		{SourceName: "", HostPattern: "x.com"}, // skip
		{SourceName: "foo", HostPattern: "y.com", SourceType: "news", Country: "KR", Language: "ko", BaseURL: "https://y.com", RequestsPerHour: 100},
	}
	bySource, hostsBySource, _, err := sources.AnalyzeSources(rules)
	require.NoError(t, err)
	assert.Len(t, bySource, 1)
	assert.NotContains(t, hostsBySource, "")
}

// TestAnalyzeSources_BaseURLsSetCapturesAllVariants — baseURLsBySource 가 모든 variant 캡쳐.
func TestAnalyzeSources_BaseURLsSetCapturesAllVariants(t *testing.T) {
	t.Parallel()
	rules := []*storage.FetcherRuleRecord{
		{SourceName: "reddit", HostPattern: "www.reddit.com", SourceType: "community", Country: "US", Language: "en", BaseURL: "https://www.reddit.com", RequestsPerHour: 200},
		{SourceName: "reddit", HostPattern: "old.reddit.com", SourceType: "community", Country: "US", Language: "en", BaseURL: "https://old.reddit.com", RequestsPerHour: 200},
	}
	_, _, baseURLsBySource, err := sources.AnalyzeSources(rules)
	require.NoError(t, err)
	urls := baseURLsBySource["reddit"]
	assert.Len(t, urls, 2)
	_, hasWww := urls["https://www.reddit.com"]
	_, hasOld := urls["https://old.reddit.com"]
	assert.True(t, hasWww)
	assert.True(t, hasOld)
}

// TestAnalyzeSources_SingleHostUniformBaseURLsSet — 단일 host 경우 set size=1.
func TestAnalyzeSources_SingleHostUniformBaseURLsSet(t *testing.T) {
	t.Parallel()
	rules := []*storage.FetcherRuleRecord{
		{SourceName: "cnn", HostPattern: "edition.cnn.com", SourceType: "news", Country: "US", Language: "en", BaseURL: "https://edition.cnn.com", RequestsPerHour: 100},
	}
	_, _, baseURLsBySource, err := sources.AnalyzeSources(rules)
	require.NoError(t, err)
	assert.Len(t, baseURLsBySource["cnn"], 1)
}
