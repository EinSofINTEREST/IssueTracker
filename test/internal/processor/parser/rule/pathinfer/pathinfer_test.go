package pathinfer_test

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/processor/parser/rule/pathinfer"
)

// 결과 regex 가 입력 sample 들 모두 매칭하는지 확인하는 헬퍼.
func assertMatchesAll(t *testing.T, regex string, samples []string) {
	t.Helper()
	re, err := regexp.Compile(regex)
	require.NoError(t, err, "regex %q must compile", regex)
	for _, s := range samples {
		assert.True(t, re.MatchString(s), "regex %q must match sample %q", regex, s)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 정상 케이스 — 단일 변수 segment
// ─────────────────────────────────────────────────────────────────────────────

func TestInferHeuristic_NumericID(t *testing.T) {
	samples := []string{"/article/12345", "/article/67890", "/article/120394"}
	regex, ok := pathinfer.InferHeuristic(pathinfer.PathSamples{Articles: samples})
	require.True(t, ok)
	assert.Equal(t, `^/article/(\d+)$`, regex)
	assertMatchesAll(t, regex, samples)
}

func TestInferHeuristic_UUID(t *testing.T) {
	samples := []string{
		"/post/01234567-89ab-cdef-0123-456789abcdef",
		"/post/abcdef01-2345-6789-abcd-ef0123456789",
		"/post/fedcba98-7654-3210-fedc-ba9876543210",
	}
	regex, ok := pathinfer.InferHeuristic(pathinfer.PathSamples{Articles: samples})
	require.True(t, ok)
	assert.Contains(t, regex, "[0-9a-f]{8}-")
	assertMatchesAll(t, regex, samples)
}

func TestInferHeuristic_Slug(t *testing.T) {
	samples := []string{
		"/news/breaking-story-2024",
		"/news/special-coverage-update",
		"/news/world-economy-report",
	}
	regex, ok := pathinfer.InferHeuristic(pathinfer.PathSamples{Articles: samples})
	require.True(t, ok)
	assert.Equal(t, `^/news/([a-z0-9-]+)$`, regex)
	assertMatchesAll(t, regex, samples)
}

// ─────────────────────────────────────────────────────────────────────────────
// 정상 케이스 — 다중 변수 (연도/월/ID 조합)
// ─────────────────────────────────────────────────────────────────────────────

func TestInferHeuristic_YearMonthID(t *testing.T) {
	samples := []string{
		"/news/2024/04/12345",
		"/news/2023/12/67890",
		"/news/2025/01/55555",
	}
	regex, ok := pathinfer.InferHeuristic(pathinfer.PathSamples{Articles: samples})
	require.True(t, ok)
	// 정확한 regex 형태는 구현 내부 — 핵심은 모든 sample 이 매칭
	assertMatchesAll(t, regex, samples)
	// 다른 연도/월/ID 조합도 통과해야 함
	re, _ := regexp.Compile(regex)
	assert.True(t, re.MatchString("/news/2022/06/99999"))
	// 잘못된 형식 (월이 13) 은 거부 — Year/Month 패턴이 1900-2099 / 01-12 만 통과하므로
	assert.False(t, re.MatchString("/news/2024/13/12345"), "월 13 은 매칭되면 안 됨")
}

// 정적 prefix + 동적 ID 의 다층 path
func TestInferHeuristic_NestedPath(t *testing.T) {
	samples := []string{
		"/category/sports/article/1",
		"/category/sports/article/200",
		"/category/sports/article/9999",
	}
	regex, ok := pathinfer.InferHeuristic(pathinfer.PathSamples{Articles: samples})
	require.True(t, ok)
	assert.Equal(t, `^/category/sports/article/(\d+)$`, regex)
	assertMatchesAll(t, regex, samples)
}

// ─────────────────────────────────────────────────────────────────────────────
// 거부 케이스 — ok=false 반환
// ─────────────────────────────────────────────────────────────────────────────

func TestInferHeuristic_TooFewSamples(t *testing.T) {
	samples := []string{"/article/1", "/article/2"} // 2개 < minSamplesForInference=3
	_, ok := pathinfer.InferHeuristic(pathinfer.PathSamples{Articles: samples})
	assert.False(t, ok, "sample 3개 미만은 거부")
}

func TestInferHeuristic_DifferentSegmentLengths(t *testing.T) {
	samples := []string{
		"/article/1",
		"/category/sports/article/2", // segment 길이 다름
		"/article/3",
	}
	_, ok := pathinfer.InferHeuristic(pathinfer.PathSamples{Articles: samples})
	assert.False(t, ok, "segment 개수가 다르면 거부")
}

func TestInferHeuristic_VariablePartUnknown(t *testing.T) {
	// 변수 부분이 모두 다른 종류 (numeric / slug / 다른 형식 혼재)
	samples := []string{
		"/article/12345",
		"/article/abc",   // numeric 도 slug (4자 미만 + 하이픈 없음) 도 아님
		"/article/world", // numeric 도 slug (하이픈 없음) 도 아님
	}
	_, ok := pathinfer.InferHeuristic(pathinfer.PathSamples{Articles: samples})
	assert.False(t, ok, "공통 패턴 못 찾으면 거부")
}

func TestInferHeuristic_EmptyInput(t *testing.T) {
	_, ok := pathinfer.InferHeuristic(pathinfer.PathSamples{Articles: nil})
	assert.False(t, ok, "빈 입력은 거부")
}

// ─────────────────────────────────────────────────────────────────────────────
// 결정성 + race 검증
// ─────────────────────────────────────────────────────────────────────────────

// 동일 입력 → 동일 출력 — 운영 디버깅 / 테스트 친화 검증.
func TestInferHeuristic_Deterministic(t *testing.T) {
	samples := []string{"/news/2024/04/12345", "/news/2023/12/67890", "/news/2025/01/55555"}
	first, ok1 := pathinfer.InferHeuristic(pathinfer.PathSamples{Articles: samples})
	second, ok2 := pathinfer.InferHeuristic(pathinfer.PathSamples{Articles: samples})
	assert.True(t, ok1)
	assert.True(t, ok2)
	assert.Equal(t, first, second, "동일 입력은 동일 출력")
}

// concurrent 호출이 안전 — 사전 컴파일 패턴 + stateless 함수.
func TestInferHeuristic_ConcurrentSafe(t *testing.T) {
	samples := []string{"/article/1", "/article/2", "/article/3"}
	const concurrency = 50
	results := make(chan string, concurrency)

	for i := 0; i < concurrency; i++ {
		go func() {
			r, _ := pathinfer.InferHeuristic(pathinfer.PathSamples{Articles: samples})
			results <- r
		}()
	}

	first := ""
	for i := 0; i < concurrency; i++ {
		r := <-results
		if first == "" {
			first = r
		} else {
			assert.Equal(t, first, r, "concurrent 호출도 동일 결과")
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Validation 검증 — 결과 regex 가 입력 samples 모두 매칭
// ─────────────────────────────────────────────────────────────────────────────

// trailing slash 가 있는 path 도 정규화 후 검증되어 ok=true 반환 (PR #183 gemini 피드백).
// 운영 환경에서는 Normalizer 가 trailing slash 를 제거하지만, 정규화 미적용 input 도 방어.
func TestInferHeuristic_TrailingSlashTolerated(t *testing.T) {
	samples := []string{"/article/1/", "/article/2/", "/article/30/"}
	regex, ok := pathinfer.InferHeuristic(pathinfer.PathSamples{Articles: samples})
	require.True(t, ok, "trailing slash 가 있어도 정규화 후 매칭")
	assert.Equal(t, `^/article/(\d+)$`, regex)
}

// 연도 패턴이 외곽 capturing group 으로 4자리 전체를 capture (PR #183 gemini 피드백).
func TestInferHeuristic_YearFullCapture(t *testing.T) {
	samples := []string{"/news/2024/04/12345", "/news/2023/12/67890", "/news/2025/01/55555"}
	regex, ok := pathinfer.InferHeuristic(pathinfer.PathSamples{Articles: samples})
	require.True(t, ok)

	re, err := regexp.Compile(regex)
	require.NoError(t, err)
	matches := re.FindStringSubmatch("/news/2024/04/12345")
	require.NotNil(t, matches)
	assert.Equal(t, "2024", matches[1], "첫 capturing group 은 4자리 연도 전체")
}

// ─────────────────────────────────────────────────────────────────────────────
// Option / config 동작
// ─────────────────────────────────────────────────────────────────────────────

// WithMinSamples 로 default(3) 보다 높게 override 시 sample 수 부족 거부.
func TestInferHeuristic_WithMinSamples_HigherThreshold(t *testing.T) {
	samples := []string{"/article/1", "/article/2", "/article/3"} // 3개 — default OK
	_, ok := pathinfer.InferHeuristic(pathinfer.PathSamples{Articles: samples})
	require.True(t, ok, "default 3 으로는 3개 OK")

	// override 5 → 거부
	_, ok = pathinfer.InferHeuristic(pathinfer.PathSamples{Articles: samples}, pathinfer.WithMinSamples(5))
	assert.False(t, ok, "WithMinSamples(5) 로 3개는 거부")
}

// WithMinSamples 로 default 보다 낮게 override 시 더 적은 sample 도 통과.
func TestInferHeuristic_WithMinSamples_LowerThreshold(t *testing.T) {
	samples := []string{"/article/1", "/article/2"} // 2개 — default 거부
	_, ok := pathinfer.InferHeuristic(pathinfer.PathSamples{Articles: samples})
	require.False(t, ok, "default 3 으로는 2개 거부")

	// override 2 → 통과
	regex, ok := pathinfer.InferHeuristic(pathinfer.PathSamples{Articles: samples}, pathinfer.WithMinSamples(2))
	assert.True(t, ok, "WithMinSamples(2) 로 2개 통과")
	assert.Equal(t, `^/article/(\d+)$`, regex)
}

// WithMinSamples 0 이하는 무시되고 default 유지 (방어 동작).
func TestInferHeuristic_WithMinSamples_ZeroIgnored(t *testing.T) {
	samples := []string{"/article/1", "/article/2"}
	// 0 이하 → 무시됨, default(3) 유지 → 2개는 거부
	_, ok := pathinfer.InferHeuristic(pathinfer.PathSamples{Articles: samples}, pathinfer.WithMinSamples(0))
	assert.False(t, ok, "WithMinSamples(0) 무시 — default 3 유지")

	_, ok = pathinfer.InferHeuristic(pathinfer.PathSamples{Articles: samples}, pathinfer.WithMinSamples(-5))
	assert.False(t, ok, "WithMinSamples(-5) 무시 — default 3 유지")
}

// 모든 정상 케이스에서 결과 regex 가 입력 sample 전체 매칭 보장.
// (개별 케이스에서 assertMatchesAll 로 검증 — 본 테스트는 대표 케이스만 추가 확인)
func TestInferHeuristic_ResultMatchesAllSamples(t *testing.T) {
	cases := []struct {
		name    string
		samples []string
	}{
		{"numeric", []string{"/a/1", "/a/2", "/a/30"}},
		{"slug", []string{"/b/foo-bar-baz", "/b/hello-world-test", "/b/some-other-thing"}},
		{"year-month-id", []string{"/c/2024/04/1", "/c/2023/12/200", "/c/2025/01/55555"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			regex, ok := pathinfer.InferHeuristic(pathinfer.PathSamples{Articles: tc.samples})
			require.True(t, ok)
			assertMatchesAll(t, regex, tc.samples)
		})
	}
}
