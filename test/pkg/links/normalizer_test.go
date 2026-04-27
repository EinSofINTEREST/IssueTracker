package links_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/pkg/links"
)

// TestNormalizeURL_Defaults는 패키지 레벨 NormalizeURL의 기본 동작을 검증합니다.
// 기본 옵션: forceHTTPS=true, stripWWW=false, stripTrailingSlash=true,
// stripFragment=true, query 화이트리스트 비어있음(전부 제거).
func TestNormalizeURL_Defaults(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "empty input returns empty",
			in:   "",
			want: "",
		},
		{
			name: "http upgraded to https",
			in:   "http://example.com/article/123",
			want: "https://example.com/article/123",
		},
		{
			name: "tracking params stripped (utm/fbclid/gclid/ref)",
			in:   "https://example.com/a?utm_source=tw&utm_medium=social&fbclid=x&gclid=y&ref=feed",
			want: "https://example.com/a",
		},
		{
			name: "all params stripped by default (no whitelist)",
			in:   "https://example.com/a?id=42&page=2",
			want: "https://example.com/a",
		},
		{
			name: "trailing slash stripped from non-root path",
			in:   "https://example.com/article/",
			want: "https://example.com/article",
		},
		{
			name: "root trailing slash preserved",
			in:   "https://example.com/",
			want: "https://example.com/",
		},
		{
			name: "fragment stripped",
			in:   "https://example.com/a#section-2",
			want: "https://example.com/a",
		},
		{
			name: "host lowercased",
			in:   "https://Example.COM/Path",
			want: "https://example.com/Path",
		},
		{
			name: "www preserved by default",
			in:   "https://www.example.com/a",
			want: "https://www.example.com/a",
		},
		{
			name: "https preserved",
			in:   "https://example.com/a",
			want: "https://example.com/a",
		},
		{
			name: "compound: tracking + fragment + trailing slash",
			in:   "http://Example.com/article/?utm_source=x&utm_campaign=y#top",
			want: "https://example.com/article",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := links.NormalizeURL(tt.in)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestNormalizeURL_InvalidURL_ReturnsError:
// 파싱 불가능한 URL은 에러 반환을 보장합니다.
func TestNormalizeURL_InvalidURL_ReturnsError(t *testing.T) {
	// net/url 은 매우 관대해서 대부분 입력을 받아들임 — control character 정도가 실패 케이스
	_, err := links.NormalizeURL("ht\x00tp://broken")
	assert.Error(t, err)
}

// TestNormalizeURL_RelativeURL_ReturnsAsIs:
// host 가 없는 상대 URL 은 정규화 대상이 아니므로 변경 없이 반환합니다.
func TestNormalizeURL_RelativeURL_ReturnsAsIs(t *testing.T) {
	got, err := links.NormalizeURL("/article/123")
	require.NoError(t, err)
	assert.Equal(t, "/article/123", got)
}

// TestNormalizer_WithAllowedParams_KeepsWhitelistedKeys:
// 사이트별 화이트리스트에 등록된 파라미터는 보존되고, 나머지는 제거됩니다.
// 동일 호스트에 대해 여러 번 등록 시 키 집합이 누적됩니다.
func TestNormalizer_WithAllowedParams_KeepsWhitelistedKeys(t *testing.T) {
	n := links.NewNormalizer(
		links.WithAllowedParams("news.naver.com", "article_id"),
		links.WithAllowedParams("news.naver.com", "office_id"), // 누적 등록
	)

	got, err := n.Normalize("https://news.naver.com/main/read.naver?article_id=123&office_id=001&utm_source=feed")
	require.NoError(t, err)
	assert.Equal(t, "https://news.naver.com/main/read.naver?article_id=123&office_id=001", got)
}

// TestNormalizer_WithAllowedParams_QuerySorted:
// 동일 컨텐츠에 대해 항상 동일한 정규형을 보장하기 위해
// 보존된 query 키는 사전순으로 정렬되어야 합니다.
func TestNormalizer_WithAllowedParams_QuerySorted(t *testing.T) {
	n := links.NewNormalizer(
		links.WithAllowedParams("youtube.com", "v", "t"),
	)

	// 입력 순서와 무관하게 동일 결과
	a, err := n.Normalize("https://youtube.com/watch?t=10&v=abc")
	require.NoError(t, err)
	b, err := n.Normalize("https://youtube.com/watch?v=abc&t=10")
	require.NoError(t, err)

	assert.Equal(t, a, b)
	assert.Equal(t, "https://youtube.com/watch?t=10&v=abc", a)
}

// TestNormalizer_WithAllowedParams_HostMatchingCaseInsensitive:
// 호스트 매칭은 case-insensitive 이며 "www." 접두사가 자동 정규화됩니다.
// 등록 시 host 와 입력 URL의 host 가 대소문자/www 차이에도 매칭되어야 합니다.
func TestNormalizer_WithAllowedParams_HostMatchingCaseInsensitive(t *testing.T) {
	n := links.NewNormalizer(
		links.WithAllowedParams("Example.com", "id"), // 등록 시 대문자 + www 없음
	)

	// 입력은 소문자 + www. 접두사
	got, err := n.Normalize("https://www.example.com/a?id=42&utm_source=x")
	require.NoError(t, err)
	assert.Equal(t, "https://www.example.com/a?id=42", got)
}

// TestNormalizer_WithAllowedParams_OtherHostStripsAll:
// 화이트리스트에 등록되지 않은 호스트는 기본 동작(전부 제거)을 따릅니다.
func TestNormalizer_WithAllowedParams_OtherHostStripsAll(t *testing.T) {
	n := links.NewNormalizer(
		links.WithAllowedParams("youtube.com", "v"),
	)

	got, err := n.Normalize("https://other.com/a?v=abc&id=42")
	require.NoError(t, err)
	assert.Equal(t, "https://other.com/a", got)
}

// TestNormalizer_WithStripWWW_RemovesWWWPrefix:
// 옵션을 활성화하면 "www." 접두사가 제거됩니다.
func TestNormalizer_WithStripWWW_RemovesWWWPrefix(t *testing.T) {
	n := links.NewNormalizer(links.WithStripWWW())

	got, err := n.Normalize("https://www.example.com/a")
	require.NoError(t, err)
	assert.Equal(t, "https://example.com/a", got)
}

// TestNormalizer_WithKeepHTTP_DoesNotUpgradeScheme:
// 옵션을 활성화하면 http 스킴이 그대로 유지됩니다.
func TestNormalizer_WithKeepHTTP_DoesNotUpgradeScheme(t *testing.T) {
	n := links.NewNormalizer(links.WithKeepHTTP())

	got, err := n.Normalize("http://example.com/a")
	require.NoError(t, err)
	assert.Equal(t, "http://example.com/a", got)
}

// TestNormalizer_WithKeepTrailingSlash_PreservesSlash:
// 옵션을 활성화하면 경로 끝 "/" 가 보존됩니다.
func TestNormalizer_WithKeepTrailingSlash_PreservesSlash(t *testing.T) {
	n := links.NewNormalizer(links.WithKeepTrailingSlash())

	got, err := n.Normalize("https://example.com/article/")
	require.NoError(t, err)
	assert.Equal(t, "https://example.com/article/", got)
}

// TestNormalizer_WithKeepFragment_PreservesFragment:
// 옵션을 활성화하면 fragment("#...") 가 보존됩니다.
func TestNormalizer_WithKeepFragment_PreservesFragment(t *testing.T) {
	n := links.NewNormalizer(links.WithKeepFragment())

	got, err := n.Normalize("https://example.com/a#section")
	require.NoError(t, err)
	assert.Equal(t, "https://example.com/a#section", got)
}

// TestNormalizer_Idempotent: 정규화 결과를 다시 정규화해도 동일한 결과여야 합니다.
// 멱등성은 다회 적용되는 파이프라인 (예: 재시도, 캐시)에서 일관성 보장에 필수입니다.
func TestNormalizer_Idempotent(t *testing.T) {
	n := links.NewNormalizer(
		links.WithStripWWW(),
		links.WithAllowedParams("example.com", "id"),
	)

	first, err := n.Normalize("http://Www.Example.com/article/?id=1&utm=x#top")
	require.NoError(t, err)
	second, err := n.Normalize(first)
	require.NoError(t, err)

	assert.Equal(t, first, second)
}

// TestNormalizer_DedupesEquivalentURLs:
// 정규화의 핵심 효과 — tracking 파라미터만 다른 두 URL은 동일한 정규형을 가져야 함.
// 이 테스트는 중복 탐지/파티션 키 일관성의 회귀를 차단합니다.
func TestNormalizer_DedupesEquivalentURLs(t *testing.T) {
	n := links.NewNormalizer()

	a, err := n.Normalize("https://example.com/article/123?utm_source=twitter")
	require.NoError(t, err)
	b, err := n.Normalize("https://example.com/article/123?utm_source=facebook&fbclid=xyz")
	require.NoError(t, err)
	c, err := n.Normalize("http://example.com/article/123/#comments")
	require.NoError(t, err)

	assert.Equal(t, a, b, "동일 기사의 추적 파라미터 변형은 같은 정규형이어야 함")
	assert.Equal(t, a, c, "scheme/trailing slash/fragment 차이도 같은 정규형이어야 함")
}
