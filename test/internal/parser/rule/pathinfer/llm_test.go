package pathinfer_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"issuetracker/internal/crawler/parser/rule/pathinfer"
)

// ─────────────────────────────────────────────────────────────────────────────
// Mock LLMClient
// ─────────────────────────────────────────────────────────────────────────────

// fakeLLM 은 응답과 에러를 미리 설정해 반환하는 LLMClient 입니다.
type fakeLLM struct {
	mu       sync.Mutex
	resp     string
	err      error
	calls    int
	lastSys  string
	lastUser string
}

func (f *fakeLLM) Generate(_ context.Context, system, user string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.lastSys = system
	f.lastUser = user
	return f.resp, f.err
}

func (f *fakeLLM) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// ─────────────────────────────────────────────────────────────────────────────
// 정상 케이스
// ─────────────────────────────────────────────────────────────────────────────

func TestInferLLM_HappyPath(t *testing.T) {
	llm := &fakeLLM{resp: `^/article/\d+$`}
	samples := pathinfer.LLMSamples{
		Articles:    []string{"/article/1", "/article/200", "/article/9999"},
		NonArticles: []string{"/about", "/category/sports"},
	}

	regex, ok, err := pathinfer.InferLLM(context.Background(), samples, llm)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, `^/article/\d+$`, regex)
	assert.Equal(t, 1, llm.callCount())
}

// markdown 펜스 안에 들어있는 패턴 추출.
func TestInferLLM_ExtractFromMarkdownFence(t *testing.T) {
	llm := &fakeLLM{resp: "```regex\n^/article/\\d+$\n```"}
	samples := pathinfer.LLMSamples{
		Articles: []string{"/article/1", "/article/2", "/article/30"},
	}
	regex, ok, err := pathinfer.InferLLM(context.Background(), samples, llm)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, `^/article/\d+$`, regex)
}

// 응답에 prose 가 같이 있어도 첫 비어있지 않은 라인 사용.
func TestInferLLM_ExtractFirstLineSkipsEmpty(t *testing.T) {
	llm := &fakeLLM{resp: "\n\n^/news/\\d+$\nThis is the regex."}
	samples := pathinfer.LLMSamples{
		Articles: []string{"/news/1", "/news/2", "/news/30"},
	}
	regex, ok, err := pathinfer.InferLLM(context.Background(), samples, llm)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, `^/news/\d+$`, regex)
}

// LLM 이 prose 를 먼저 출력하고 fenced regex 로 감싸는 패턴 cover (PR #187 CodeRabbit 피드백).
// 기존 로직은 prose 첫 줄을 반환하던 회귀 케이스.
func TestInferLLM_ExtractFromMidResponseFence(t *testing.T) {
	llm := &fakeLLM{resp: "Here is the regex you requested:\n\n```\n^/article/\\d+$\n```\n\nLet me know if you need adjustments."}
	samples := pathinfer.LLMSamples{
		Articles: []string{"/article/1", "/article/2", "/article/30"},
	}
	regex, ok, err := pathinfer.InferLLM(context.Background(), samples, llm)
	require.NoError(t, err)
	require.True(t, ok, "fenced block 이 응답 중간에 있어도 안의 regex 추출")
	assert.Equal(t, `^/article/\d+$`, regex)
}

// negative 가 있어도 정상 매칭 + 거부 동작.
func TestInferLLM_NegativeRejected(t *testing.T) {
	// LLM 이 잘못된 (너무 broad) 패턴 반환 — negative match 시 거부
	llm := &fakeLLM{resp: `^/.+$`}
	samples := pathinfer.LLMSamples{
		Articles:    []string{"/article/1", "/article/2", "/article/3"},
		NonArticles: []string{"/about"}, // 이 path 가 ^/.+$ 에 매칭 → 거부
	}
	regex, ok, err := pathinfer.InferLLM(context.Background(), samples, llm)
	require.NoError(t, err)
	assert.False(t, ok, "negative 매칭 시 거부")
	assert.Empty(t, regex)
}

// ─────────────────────────────────────────────────────────────────────────────
// 거부 케이스
// ─────────────────────────────────────────────────────────────────────────────

func TestInferLLM_TooFewSamples(t *testing.T) {
	llm := &fakeLLM{resp: `^/article/\d+$`}
	samples := pathinfer.LLMSamples{Articles: []string{"/article/1", "/article/2"}}

	_, ok, err := pathinfer.InferLLM(context.Background(), samples, llm)
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Equal(t, 0, llm.callCount(), "sample 부족 시 LLM 호출 안 함")
}

func TestInferLLM_NilClient(t *testing.T) {
	samples := pathinfer.LLMSamples{Articles: []string{"/a/1", "/a/2", "/a/3"}}
	_, ok, err := pathinfer.InferLLM(context.Background(), samples, nil)
	require.Error(t, err)
	assert.False(t, ok)
}

func TestInferLLM_LLMError(t *testing.T) {
	llm := &fakeLLM{err: errors.New("api timeout")}
	samples := pathinfer.LLMSamples{Articles: []string{"/a/1", "/a/2", "/a/3"}}

	_, ok, err := pathinfer.InferLLM(context.Background(), samples, llm)
	require.Error(t, err)
	assert.False(t, ok)
}

// LLM 응답이 RE2 컴파일 실패 시 거부.
func TestInferLLM_InvalidRegexRejected(t *testing.T) {
	llm := &fakeLLM{resp: `[unclosed-bracket`}
	samples := pathinfer.LLMSamples{Articles: []string{"/a/1", "/a/2", "/a/3"}}

	_, ok, err := pathinfer.InferLLM(context.Background(), samples, llm)
	require.NoError(t, err)
	assert.False(t, ok)
}

// positive 를 매칭 못 하는 패턴 거부.
func TestInferLLM_PositiveMissmatchRejected(t *testing.T) {
	llm := &fakeLLM{resp: `^/news/\d+$`}
	samples := pathinfer.LLMSamples{
		Articles: []string{"/article/1", "/article/2", "/article/3"}, // /article 인데 regex 는 /news
	}
	_, ok, err := pathinfer.InferLLM(context.Background(), samples, llm)
	require.NoError(t, err)
	assert.False(t, ok)
}

// 빈 응답 거부.
func TestInferLLM_EmptyResponseRejected(t *testing.T) {
	llm := &fakeLLM{resp: ""}
	samples := pathinfer.LLMSamples{Articles: []string{"/a/1", "/a/2", "/a/3"}}
	_, ok, err := pathinfer.InferLLM(context.Background(), samples, llm)
	require.NoError(t, err)
	assert.False(t, ok)
}

// ─────────────────────────────────────────────────────────────────────────────
// trivially-broad 거부
// ─────────────────────────────────────────────────────────────────────────────

func TestInferLLM_TriviallyBroadRejected(t *testing.T) {
	cases := []string{
		".*",
		".+",
		"/.*",
		"/.+",
		"^.*$",
		"^.+$",
		"^/$",
		"^/.*$",
		"^/.+$",
		"^/(.*)$",
		"^/(.+)$",
	}
	for _, p := range cases {
		t.Run(p, func(t *testing.T) {
			llm := &fakeLLM{resp: p}
			samples := pathinfer.LLMSamples{
				Articles: []string{"/a/1", "/a/2", "/a/3"},
			}
			_, ok, err := pathinfer.InferLLM(context.Background(), samples, llm)
			require.NoError(t, err)
			assert.False(t, ok, "trivially-broad %q 는 거부되어야 함", p)
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Option 동작 — InferHeuristic 과 일관성
// ─────────────────────────────────────────────────────────────────────────────

func TestInferLLM_WithMinSamples_Override(t *testing.T) {
	llm := &fakeLLM{resp: `^/a/\d+$`}
	samples := pathinfer.LLMSamples{Articles: []string{"/a/1", "/a/2"}} // 2개

	// default 3 → 거부, LLM 호출 안 함
	_, ok, _ := pathinfer.InferLLM(context.Background(), samples, llm)
	assert.False(t, ok)
	assert.Equal(t, 0, llm.callCount())

	// override 2 → LLM 호출 + 통과
	regex, ok, err := pathinfer.InferLLM(context.Background(), samples, llm, pathinfer.WithMinSamples(2))
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, `^/a/\d+$`, regex)
	assert.Equal(t, 1, llm.callCount())
}

// ─────────────────────────────────────────────────────────────────────────────
// 프롬프트 검증 — system / user 가 정확히 전달되는지
// ─────────────────────────────────────────────────────────────────────────────

func TestInferLLM_PromptIncludesSamples(t *testing.T) {
	llm := &fakeLLM{resp: `^/a/\d+$`}
	samples := pathinfer.LLMSamples{
		Articles:    []string{"/a/1", "/a/2", "/a/3"},
		NonArticles: []string{"/about", "/category"},
	}
	_, _, _ = pathinfer.InferLLM(context.Background(), samples, llm)

	assert.Contains(t, llm.lastSys, "RE2", "system 에 RE2 명시")
	for _, a := range samples.Articles {
		assert.Contains(t, llm.lastUser, a, "user 에 article path 포함: %s", a)
	}
	for _, n := range samples.NonArticles {
		assert.Contains(t, llm.lastUser, n, "user 에 non-article path 포함: %s", n)
	}
}

// NonArticles 가 비어있으면 "(none)" 표기.
func TestInferLLM_PromptShowsNoneForEmpty(t *testing.T) {
	llm := &fakeLLM{resp: `^/a/\d+$`}
	samples := pathinfer.LLMSamples{Articles: []string{"/a/1", "/a/2", "/a/3"}}
	_, _, _ = pathinfer.InferLLM(context.Background(), samples, llm)
	assert.Contains(t, llm.lastUser, "(none)", "NonArticles 비어있으면 (none) 표기")
}
